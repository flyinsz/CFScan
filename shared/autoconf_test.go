package autoconf

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestStoreEncryptsSecrets(t *testing.T) {
	s := Store{Dir: t.TempDir()}
	v := Defaults()
	v.CFAccount = "test-account"
	v.CFNamespace = "test-namespace"
	v.PanelPassword = "988998"
	v.CFToken = "secret-token"
	v.PanelAPIKey = "gate-key"
	if err := s.Save(v); err != nil {
		t.Fatalf("Save: %v", err)
	}
	raw, err := os.ReadFile(s.ConfigPath())
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"988998", "secret-token", "gate-key"} {
		if strings.Contains(string(raw), secret) {
			t.Fatalf("secret %q leaked into config file", secret)
		}
	}
	got, exists, err := s.Load()
	if err != nil || !exists {
		t.Fatalf("Load: exists=%v err=%v", exists, err)
	}
	if got.PanelPassword != "988998" || got.CFToken != "secret-token" || got.PanelAPIKey != "gate-key" {
		t.Fatalf("roundtrip mismatch: %+v", got)
	}
	if got.MaxIPs != 100 || got.AutoInterval != "6h" {
		t.Fatalf("non-secret fields lost: %+v", got)
	}
}

func TestMaskAndResolve(t *testing.T) {
	cur := Defaults()
	cur.PanelPassword = "abc"
	cur.CFToken = "tok"
	m := MaskSecrets(cur)
	if m.PanelPassword != Mask || m.CFToken != Mask || m.PanelAPIKey != "" {
		t.Fatalf("mask wrong: %+v", m)
	}
	in := m
	in.Channel = "panel"
	got := ResolveSecrets(in, cur)
	if got.PanelPassword != "abc" || got.CFToken != "tok" || got.Channel != "panel" {
		t.Fatalf("resolve wrong: %+v", got)
	}
	in.CFToken = ""
	got = ResolveSecrets(in, cur)
	if got.CFToken != "" {
		t.Fatalf("explicit clear should work, got %q", got.CFToken)
	}
}

func TestValidate(t *testing.T) {
	v := Defaults()
	v.CFAccount, v.CFNamespace, v.CFToken = "a", "n", "t"
	if err := Validate(v); err != nil {
		t.Fatalf("valid kv: %v", err)
	}
	bad := v
	bad.AutoAt = "25:99"
	if err := Validate(bad); err == nil {
		t.Fatal("bad AutoAt accepted")
	}
	bad = v
	bad.Channel = "kv"
	bad.CFToken = ""
	if err := Validate(bad); err == nil {
		t.Fatal("kv without token accepted")
	}
	bad = v
	bad.Channel = "panel"
	bad.PanelURL = ""
	if err := Validate(bad); err == nil {
		t.Fatal("panel without URL accepted")
	}
}

func TestCountIPsUnwrapsPanelEnvelope(t *testing.T) {
	raw := []byte(`{"ok":true,"data":{"version":"3.1.1","preferredIPs":[{"ip":"1.1.1.1"},{"ip":"2.2.2.2"}]}}`)
	n, ver, err := countIPs(raw)
	if err != nil || n != 2 || ver != "3.1.1" {
		t.Fatalf("n=%d ver=%s err=%v", n, ver, err)
	}
	plain := []byte(`{"preferredIPs":[]}`)
	n, _, err = countIPs(plain)
	if err != nil || n != 0 {
		t.Fatalf("plain: n=%d err=%v", n, err)
	}
}

func TestDiscoverAPIBase(t *testing.T) {
	const uuid = "/3108745c-test"
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/donny-admin":
			if err := r.ParseForm(); err != nil || r.Form.Get("password") != "pw" {
				w.WriteHeader(403)
				return
			}
			http.SetCookie(w, &http.Cookie{Name: "luma_auth", Value: "ok", Path: "/"})
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
		case r.URL.Path == "/donny-admin":
			_, _ = w.Write([]byte(`<script>var APIPATH = "` + uuid + `"; var CFG = null;</script>`))
		case r.URL.Path == uuid+"/api/config":
			_, _ = w.Write([]byte(`{"ok":true,"data":{"preferredIPs":[{"ip":"1.1.1.1"}]}}`))
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()
	client, err := probeClient(Defaults())
	if err != nil {
		t.Fatal(err)
	}
	got, err := DiscoverAPIBase(context.Background(), client, srv.URL+"/donny-admin", "pw", "")
	if err != nil || got != srv.URL+uuid {
		t.Fatalf("discover got=%q err=%v want=%q", got, err, srv.URL+uuid)
	}
	v := Defaults()
	v.Channel = "panel"
	v.PanelURL = srv.URL + "/donny-admin"
	v.PanelPassword = "pw"
	v.APIBase = got
	msg, err := Probe(context.Background(), v)
	if err != nil || !strings.Contains(msg, "1 个优选 IP") {
		t.Fatalf("Probe msg=%q err=%v", msg, err)
	}
}

func TestLoadFallsBackToEnv(t *testing.T) {
	t.Setenv("AUTO_INTERVAL", "3h")
	t.Setenv("CFNEXT_CHANNEL", "dry")
	v, exists, err := (Store{Dir: t.TempDir()}).Load()
	if err != nil || exists {
		t.Fatalf("exists=%v err=%v", exists, err)
	}
	if v.AutoInterval != "3h" || v.Channel != "dry" {
		t.Fatalf("env fallback wrong: %+v", v)
	}
}
