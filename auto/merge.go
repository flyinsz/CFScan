package main

import (
	"encoding/json"
	"fmt"
	"net"
	"strconv"
	"strings"
)

// PreferredIP matches CFNext KV config.preferredIPs entries: {ip, port, name}.
type PreferredIP struct {
	IP   string `json:"ip"`
	Port int    `json:"port"`
	Name string `json:"name"`
}

// CFNextConfig keeps every original field as raw JSON so a full write never drops settings.
type CFNextConfig map[string]json.RawMessage

func decodeConfig(data []byte) (CFNextConfig, error) {
	var cfg CFNextConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("解析 CFNext config 失败: %w", err)
	}
	if cfg == nil {
		return nil, fmt.Errorf("CFNext config 不是 JSON 对象")
	}
	return cfg, nil
}

func getPreferredIPs(cfg CFNextConfig) ([]PreferredIP, error) {
	raw, ok := cfg["preferredIPs"]
	if !ok || string(raw) == "null" {
		return nil, nil
	}
	var ips []PreferredIP
	if err := json.Unmarshal(raw, &ips); err != nil {
		return nil, fmt.Errorf("解析 preferredIPs 失败: %w", err)
	}
	return ips, nil
}

func setPreferredIPs(cfg CFNextConfig, ips []PreferredIP, setSubMode bool) error {
	b, err := json.Marshal(ips)
	if err != nil {
		return err
	}
	cfg["preferredIPs"] = json.RawMessage(b)
	if setSubMode {
		// CFNext 3.x reads optimizer.subMode; keep the legacy top-level key too.
		cfg["subMode"] = json.RawMessage(`"custom"`)
		opt := map[string]json.RawMessage{}
		if raw, ok := cfg["optimizer"]; ok && string(raw) != "null" {
			if err := json.Unmarshal(raw, &opt); err != nil || opt == nil {
				opt = map[string]json.RawMessage{}
			}
		}
		opt["subMode"] = json.RawMessage(`"custom"`)
		ob, err := json.Marshal(opt)
		if err != nil {
			return err
		}
		cfg["optimizer"] = ob
	}
	return nil
}

// parseScanResults accepts CFScan TXT output: one "ip:port" per line,
// optional "#备注", bare IP, bracketed IPv6, BOM, comments and table headers.
func parseScanResults(data []byte) ([]PreferredIP, error) {
	s := strings.TrimPrefix(string(data), "\ufeff")
	// env values often carry a literal "\n"; also accept comma/semicolon separated lists.
	s = strings.ReplaceAll(s, `\n`, "\n")
	s = strings.NewReplacer(",", "\n", ";", "\n").Replace(s)
	seen := make(map[string]bool)
	out := make([]PreferredIP, 0, 32)
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(strings.TrimPrefix(line, "\ufeff"))
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if i := strings.IndexByte(line, '#'); i >= 0 {
			line = strings.TrimSpace(line[:i])
		}
		line = strings.Fields(line)[0]
		ip, port, ok := extractIPPort(line)
		if !ok {
			continue
		}
		key := ip + ":" + strconv.Itoa(port)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, PreferredIP{IP: ip, Port: port})
	}
	return out, nil
}

func extractIPPort(s string) (string, int, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", 0, false
	}
	// TXT with multiple fields uses "ip:port#extra"; separators may follow the address.
	for _, sep := range []string{"\t", " ", ",", ";"} {
		if i := strings.Index(s, sep); i >= 0 {
			s = s[:i]
			break
		}
	}
	if s == "" {
		return "", 0, false
	}
	if host, portStr, err := net.SplitHostPort(s); err == nil {
		if ip := net.ParseIP(strings.Trim(host, "[]")); ip != nil {
			port, err := strconv.Atoi(portStr)
			if err == nil && port > 0 && port <= 65535 {
				return ip.String(), port, true
			}
		}
	}
	if ip := net.ParseIP(s); ip != nil {
		return ip.String(), 443, true
	}
	return "", 0, false
}

// mergePreferredIPs puts the latest scan results at the head of preferredIPs.
// CFNext truncates the node list (cpuLimit / nodeLimit / 内置·保底 fill), so only
// the first ~20 entries ever reach a subscription. Appending fresh results and
// truncating the tail silently discarded every newly scanned IP.
// The second return value is how many fresh entries survived the final cap.
func mergePreferredIPs(remote, fresh []PreferredIP, max int, overwrite bool) ([]PreferredIP, int) {
	keyOf := func(p PreferredIP) string { return p.IP + ":" + strconv.Itoa(p.Port) }
	names := make(map[string]string, len(remote))
	for _, p := range remote {
		names[keyOf(p)] = p.Name
	}
	seen := make(map[string]bool, len(remote)+len(fresh))
	freshKeys := make(map[string]bool, len(fresh))
	out := make([]PreferredIP, 0, len(remote)+len(fresh))
	add := func(p PreferredIP) bool {
		key := keyOf(p)
		if seen[key] {
			return false
		}
		seen[key] = true
		out = append(out, p)
		return true
	}
	for i, p := range fresh {
		key := keyOf(p)
		freshKeys[key] = true
		if p.Name == "" {
			p.Name = names[key] // keep the existing label when the IP is already known
		}
		if p.Name == "" {
			p.Name = fmt.Sprintf("优选%d", i+1)
		}
		add(p)
	}
	if !overwrite {
		for _, p := range remote {
			add(p)
		}
	}
	if max > 0 && len(out) > max {
		out = out[:max]
	}
	kept := 0
	for _, p := range out {
		if freshKeys[keyOf(p)] {
			kept++
		}
	}
	return out, kept
}
