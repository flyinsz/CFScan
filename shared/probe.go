package autoconf

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"regexp"
	"strings"
	"time"
)

var apiPathPattern = regexp.MustCompile(`var\s+APIPATH\s*=\s*["']([^"']+)["']`)

// Probe performs a READ-ONLY connectivity check against the selected channel.
func Probe(ctx context.Context, v Settings) (string, error) {
	switch strings.ToLower(strings.TrimSpace(v.Channel)) {
	case "dry":
		return "干跑模式：只扫描，不写入 CFNext", nil
	case "kv":
		return probeKV(ctx, v)
	case "panel":
		return probePanel(ctx, v)
	default:
		if s, err := probePanel(ctx, v); err == nil {
			return s + "（auto：面板可用）", nil
		}
		return probeKV(ctx, v)
	}
}

func probeClient(v Settings) (*http.Client, error) {
	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, err
	}
	d, err := time.ParseDuration(v.HTTPTimeout)
	if err != nil || d <= 0 {
		d = 30 * time.Second
	}
	return &http.Client{Jar: jar, Timeout: d}, nil
}

func do(client *http.Client, ctx context.Context, method, u, body string, headers map[string]string) ([]byte, int, error) {
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, rd)
	if err != nil {
		return nil, 0, err
	}
	for k, val := range headers {
		req.Header.Set(k, val)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	return b, resp.StatusCode, err
}

type configShape struct {
	OK          bool            `json:"ok"`
	Success     bool            `json:"success"`
	Data        json.RawMessage `json:"data"`
	Result      json.RawMessage `json:"result"`
	Version     string          `json:"version"`
	PreferredIP json.RawMessage `json:"preferredIPs"`
}

func countIPs(raw []byte) (int, string, error) {
	var env configShape
	if json.Unmarshal(raw, &env) == nil {
		if len(env.Data) > 0 {
			raw = env.Data
		} else if env.Success && len(env.Result) > 0 {
			raw = env.Result
		}
	}
	var cfg map[string]json.RawMessage
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return 0, "", fmt.Errorf("配置不是 JSON 对象（可能返回了登录页）")
	}
	var ips []json.RawMessage
	if p, ok := cfg["preferredIPs"]; ok && string(p) != "null" {
		if err := json.Unmarshal(p, &ips); err != nil {
			return 0, "", fmt.Errorf("preferredIPs 解析失败")
		}
	}
	var version string
	_ = json.Unmarshal(cfg["version"], &version)
	if version == "" {
		version = env.Version
	}
	return len(ips), version, nil
}

func probeKV(ctx context.Context, v Settings) (string, error) {
	client, err := probeClient(v)
	if err != nil {
		return "", err
	}
	u := fmt.Sprintf("https://api.cloudflare.com/client/v4/accounts/%s/storage/kv/namespaces/%s/values/config",
		url.PathEscape(v.CFAccount), url.PathEscape(v.CFNamespace))
	h := map[string]string{"Authorization": "Bearer " + v.CFToken}
	if v.PanelAPIKey != "" {
		h["X-CF-Api"] = v.PanelAPIKey
	}
	b, status, err := do(client, ctx, http.MethodGet, u, "", h)
	if err != nil {
		return "", fmt.Errorf("KV API 连接失败: %w", err)
	}
	if status != http.StatusOK {
		return "", fmt.Errorf("KV API 返回 HTTP %d", status)
	}
	n, ver, err := countIPs(b)
	if err != nil {
		return "", fmt.Errorf("KV 配置读取失败: %w", err)
	}
	return fmt.Sprintf("KV API 连接成功：当前 %d 个优选 IP，配置版本 %s", n, emptyAs(ver, "未知")), nil
}

// DiscoverAPIBase logs into the panel and reads APIPATH from the console HTML.
// CFNext serves the admin UI at /<L> but the API at /<uuid>/api/....
// When explicit is non-empty it is used instead of discovery, but login still runs
// so the returned client keeps the session cookie.
func DiscoverAPIBase(ctx context.Context, client *http.Client, panelURL, password, explicit string) (string, error) {
	base := strings.TrimRight(panelURL, "/")
	if base == "" {
		return "", fmt.Errorf("面板地址为空")
	}
	form := url.Values{"password": {password}, "next": {base}}.Encode()
	h := map[string]string{"Content-Type": "application/x-www-form-urlencoded"}
	b, status, err := do(client, ctx, http.MethodPost, base, form, h)
	if err != nil {
		return "", fmt.Errorf("面板登录请求失败: %w", err)
	}
	if status < 200 || status >= 300 {
		return "", fmt.Errorf("面板登录返回 HTTP %d: %s", status, brief(b))
	}
	var ack struct {
		OK bool `json:"ok"`
	}
	if json.Unmarshal(b, &ack) == nil && !ack.OK {
		// Some builds accept the POST on /login instead of the entry path.
		b2, st2, err2 := do(client, ctx, http.MethodPost, base+"/login", form, h)
		if err2 != nil || st2 < 200 || st2 >= 300 {
			return "", fmt.Errorf("面板密码被拒绝或登录路径不匹配")
		}
		b = b2
	}
	if strings.TrimSpace(explicit) != "" {
		return strings.TrimRight(strings.TrimSpace(explicit), "/"), nil
	}
	page, _, err := do(client, ctx, http.MethodGet, base, "", nil)
	if err != nil {
		return "", fmt.Errorf("读取控制台页面失败: %w", err)
	}
	m := apiPathPattern.FindSubmatch(page)
	if len(m) < 2 {
		return "", fmt.Errorf("未能在控制台页面找到 APIPATH（面板可能不是 CFNext 3.x）")
	}
	pu, err := url.Parse(base)
	if err != nil {
		return "", err
	}
	p := strings.TrimRight(string(m[1]), "/")
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	return pu.Scheme + "://" + pu.Host + p, nil
}

func probePanel(ctx context.Context, v Settings) (string, error) {
	client, err := probeClient(v)
	if err != nil {
		return "", err
	}
	apiBase, err := DiscoverAPIBase(ctx, client, v.PanelURL, v.PanelPassword, v.APIBase)
	if err != nil {
		return "", err
	}
	h := map[string]string{}
	if v.PanelAPIKey != "" {
		h["X-CF-Api"] = v.PanelAPIKey
	}
	b, status, err := do(client, ctx, http.MethodGet, apiBase+"/api/config", "", h)
	if err != nil {
		return "", fmt.Errorf("面板 API 连接失败: %w", err)
	}
	if status != http.StatusOK {
		return "", fmt.Errorf("面板 API 返回 HTTP %d（入口 %s）", status, apiBase)
	}
	n, ver, err := countIPs(b)
	if err != nil {
		return "", fmt.Errorf("面板 API 未返回配置: %w", err)
	}
	return fmt.Sprintf("面板连接成功：入口 %s，当前 %d 个优选 IP，版本 %s", apiBase, n, emptyAs(ver, "3.x")), nil
}

func brief(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > 120 {
		s = s[:120] + "..."
	}
	return strings.ReplaceAll(s, "\n", " ")
}

func emptyAs(s, def string) string {
	if strings.TrimSpace(s) == "" {
		return def
	}
	return s
}
