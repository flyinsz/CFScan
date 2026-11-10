package main

import (
	"encoding/json"
	"fmt"
	"reflect"
	"testing"
)

func TestParseScanResults(t *testing.T) {
	in := "\ufeff1.1.1.1:443\n1.0.0.1:443#cloudflare\n[2606:4700::1111]:8443\n104.26.228.95\n\n# comment\nnot-an-ip\n1.1.1.1:443\n"
	got, err := parseScanResults([]byte(in))
	if err != nil {
		t.Fatal(err)
	}
	want := []PreferredIP{
		{IP: "1.1.1.1", Port: 443},
		{IP: "1.0.0.1", Port: 443},
		{IP: "2606:4700::1111", Port: 8443},
		{IP: "104.26.228.95", Port: 443},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("parse mismatch\n got=%+v\nwant=%+v", got, want)
	}
}

func TestParseScanResultsEnvLiteralNewline(t *testing.T) {
	got, err := parseScanResults([]byte(`104.16.0.1:443\n104.17.0.1:443`))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].IP != "104.16.0.1" || got[1].IP != "104.17.0.1" {
		t.Fatalf("literal newline not parsed: %+v", got)
	}
}

func TestMergeFreshFirstAndDedup(t *testing.T) {
	remote := []PreferredIP{{IP: "1.1.1.1", Port: 443, Name: "优选1"}}
	fresh := []PreferredIP{{IP: "1.1.1.1", Port: 443}, {IP: "1.0.0.1", Port: 443}}
	got, kept := mergePreferredIPs(remote, fresh, 100, false)
	if kept != 2 || len(got) != 2 {
		t.Fatalf("kept=%d len=%d got=%+v", kept, len(got), got)
	}
	if got[0].IP != "1.1.1.1" || got[0].Name != "优选1" {
		t.Fatalf("fresh entry must be first and keep its name: %+v", got[0])
	}
	if got[1].IP != "1.0.0.1" || got[1].Name != "优选2" {
		t.Fatalf("unexpected entry: %+v", got[1])
	}
}

func TestMergeMaxAndOverwrite(t *testing.T) {
	remote := []PreferredIP{{IP: "1.1.1.1", Port: 443, Name: "old"}}
	fresh := []PreferredIP{{IP: "2.2.2.2", Port: 443}, {IP: "3.3.3.3", Port: 443}}
	got, kept := mergePreferredIPs(remote, fresh, 2, false)
	if len(got) != 2 || kept != 2 || got[0].IP != "2.2.2.2" || got[1].IP != "3.3.3.3" {
		t.Fatalf("cap must keep the fresh head, not the old tail: %+v kept=%d", got, kept)
	}
	got, kept = mergePreferredIPs(remote, fresh, 0, true)
	if len(got) != 2 || kept != 2 || got[0].IP != "2.2.2.2" {
		t.Fatalf("overwrite failed: %+v kept=%d", got, kept)
	}
}

// Regression: 100 stale entries + 20 freshly scanned entries with a cap of 100
// used to discard all 20 new IPs (out[:max] kept the old head).
func TestMergeCapKeepsFreshResults(t *testing.T) {
	remote := make([]PreferredIP, 100)
	for i := range remote {
		remote[i] = PreferredIP{IP: fmt.Sprintf("10.0.%d.%d", i/250, i%250+1), Port: 443, Name: fmt.Sprintf("优选%d", i+1)}
	}
	fresh := make([]PreferredIP, 20)
	for i := range fresh {
		fresh[i] = PreferredIP{IP: fmt.Sprintf("104.19.%d.%d", i, i+1), Port: 443}
	}
	got, kept := mergePreferredIPs(remote, fresh, 100, false)
	if kept != 20 {
		t.Fatalf("all 20 fresh IPs must survive the cap, kept=%d", kept)
	}
	for i := range fresh {
		if got[i].IP != fresh[i].IP {
			t.Fatalf("index %d: got %s, want fresh %s", i, got[i].IP, fresh[i].IP)
		}
	}
}

func TestConfigRoundTripKeepsUnknownFields(t *testing.T) {
	raw := []byte(`{"uuid":"abc","admin":"secret","nodeLimitCount":300,"preferredIPs":[],"nested":{"x":1}}`)
	cfg, err := decodeConfig(raw)
	if err != nil {
		t.Fatal(err)
	}
	if err := setPreferredIPs(cfg, []PreferredIP{{IP: "1.1.1.1", Port: 443, Name: "优选1"}}, true); err != nil {
		t.Fatal(err)
	}
	out, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	var back map[string]any
	if err := json.Unmarshal(out, &back); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"uuid", "admin", "nodeLimitCount", "nested", "subMode"} {
		if _, ok := back[k]; !ok {
			t.Fatalf("field %s lost after rewrite: %s", k, out)
		}
	}
	if back["subMode"] != "custom" {
		t.Fatalf("subMode not set: %s", out)
	}
}
