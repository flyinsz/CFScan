package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type Reporter struct {
	cfg    Config
	client *http.Client
}

func NewReporter(c Config) (*Reporter, error) {
	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, err
	}
	return &Reporter{cfg: c, client: &http.Client{Timeout: c.HTTPTimeout, Jar: jar}}, nil
}

func (r *Reporter) do(ctx context.Context, method, u string, body []byte, headers map[string]string) ([]byte, int, error) {
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, rd)
	if err != nil {
		return nil, 0, err
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := r.client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, resp.StatusCode, err
	}
	return data, resp.StatusCode, nil
}

// Report dispatches to panel HTTP API, Cloudflare KV API, or both (auto fallback).
func (r *Reporter) Report(ctx context.Context, fresh []PreferredIP) (string, error) {
	switch r.cfg.Channel {
	case "dry":
		return fmt.Sprintf("dry-run: 待上报 %d 条，未写入 CFNext", len(fresh)), nil
	case "kv":
		return r.reportKV(ctx, fresh)
	case "panel":
		return r.reportPanel(ctx, fresh)
	default:
		if r.cfg.PanelURL != "" && r.cfg.PanelPassword != "" {
			s, err := r.reportPanel(ctx, fresh)
			if err == nil {
				return s, nil
			}
			log.Printf("[cfnext] 面板通道失败，回退 KV API: %v", err)
		}
		return r.reportKV(ctx, fresh)
	}
}

func unwrapKVValue(b []byte) []byte {
	var env struct {
		Success bool            `json:"success"`
		Result  json.RawMessage `json:"result"`
	}
	if json.Unmarshal(b, &env) == nil && env.Success && len(env.Result) > 0 {
		return env.Result
	}
	return b
}

func (r *Reporter) kvURL() string {
	return fmt.Sprintf("https://api.cloudflare.com/client/v4/accounts/%s/storage/kv/namespaces/%s/values/config",
		url.PathEscape(r.cfg.CFAccount), url.PathEscape(r.cfg.CFNamespace))
}

func (r *Reporter) kvHeaders() map[string]string {
	h := map[string]string{"Authorization": "Bearer " + r.cfg.CFToken}
	if r.cfg.PanelAPIKey != "" {
		h["X-CF-Api"] = r.cfg.PanelAPIKey
	}
	return h
}

func (r *Reporter) reportKV(ctx context.Context, fresh []PreferredIP) (string, error) {
	if r.cfg.CFAccount == "" || r.cfg.CFNamespace == "" || r.cfg.CFToken == "" {
		return "", fmt.Errorf("KV 通道缺少 CF_ACCOUNT_ID / CF_KV_NAMESPACE_ID / CF_API_TOKEN")
	}
	u := r.kvURL()
	h := r.kvHeaders()

	raw, status, err := r.do(ctx, http.MethodGet, u, nil, h)
	if err != nil {
		return "", fmt.Errorf("读取 CFNext KV 失败: %w", err)
	}
	if status != http.StatusOK {
		return "", fmt.Errorf("读取 CFNext KV 返回 HTTP %d: %s", status, brief(raw))
	}
	original := unwrapKVValue(raw)
	cfg, err := decodeConfig(original)
	if err != nil {
		return "", err
	}
	remote, err := getPreferredIPs(cfg)
	if err != nil {
		return "", err
	}
	merged, added := mergePreferredIPs(remote, fresh, r.cfg.MaxIPs, false)
	if err := setPreferredIPs(cfg, merged, r.cfg.SetSubMode); err != nil {
		return "", err
	}
	updated, err := json.Marshal(cfg)
	if err != nil {
		return "", err
	}

	saveBackup(r.cfg.DataDir, original)
	resp, status, err := r.do(ctx, http.MethodPut, u, updated, h)
	if err != nil {
		return "", fmt.Errorf("写入 CFNext KV 失败: %w", err)
	}
	if status < 200 || status >= 300 {
		return "", fmt.Errorf("写入 CFNext KV 返回 HTTP %d: %s", status, brief(resp))
	}

	if err := r.verifyKV(ctx, merged, u, h); err != nil {
		// KV is eventually consistent; do not roll back automatically. Backup is on disk.
		return fmt.Sprintf("kv: 已提交，原有 %d 条 / 新增 %d 条 / 共 %d 条；回读校验: %v（KV 最终一致，稍后可复核）",
			len(remote), added, len(merged), err), nil
	}
	return fmt.Sprintf("kv: 原有 %d 条 / 新增 %d 条 / 共 %d 条，回读校验通过", len(remote), added, len(merged)), nil
}

func (r *Reporter) verifyKV(ctx context.Context, want []PreferredIP, u string, h map[string]string) error {
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(2 * time.Second):
			}
		}
		raw, status, err := r.do(ctx, http.MethodGet, u, nil, h)
		if err != nil {
			lastErr = err
			continue
		}
		if status != http.StatusOK {
			lastErr = fmt.Errorf("HTTP %d", status)
			continue
		}
		cfg, err := decodeConfig(unwrapKVValue(raw))
		if err != nil {
			lastErr = err
			continue
		}
		got, err := getPreferredIPs(cfg)
		if err != nil {
			lastErr = err
			continue
		}
		if len(got) == len(want) && len(want) > 0 && got[len(got)-1].IP == want[len(want)-1].IP {
			return nil
		}
		lastErr = fmt.Errorf("回读 %d 条，期望 %d 条", len(got), len(want))
	}
	return lastErr
}

func (r *Reporter) reportPanel(ctx context.Context, fresh []PreferredIP) (string, error) {
	base := r.cfg.PanelURL
	if base == "" || r.cfg.PanelPassword == "" {
		return "", fmt.Errorf("面板通道缺少 CFNEXT_PANEL_URL / CFNEXT_PASSWORD")
	}
	apiHeaders := map[string]string{}
	if r.cfg.PanelAPIKey != "" {
		apiHeaders["X-CF-Api"] = r.cfg.PanelAPIKey
	}
	form := url.Values{"password": {r.cfg.PanelPassword}}.Encode()
	loginBody, status, err := r.do(ctx, http.MethodPost, base+"/login", []byte(form), map[string]string{
		"Content-Type": "application/x-www-form-urlencoded",
	})
	if err != nil {
		return "", fmt.Errorf("面板登录失败: %w", err)
	}
	if status < 200 || status >= 300 {
		return "", fmt.Errorf("面板登录返回 HTTP %d: %s", status, brief(loginBody))
	}

	apiURL := base + "/api/config"
	raw, status, err := r.do(ctx, http.MethodGet, apiURL, nil, apiHeaders)
	if err != nil {
		return "", fmt.Errorf("读取面板配置失败: %w", err)
	}
	if status != http.StatusOK {
		return "", fmt.Errorf("读取面板配置返回 HTTP %d: %s", status, brief(raw))
	}
	configBytes := extractPanelConfig(raw)
	cfg, err := decodeConfig(configBytes)
	if err != nil {
		return "", err
	}
	remote, err := getPreferredIPs(cfg)
	if err != nil {
		return "", err
	}
	merged, added := mergePreferredIPs(remote, fresh, r.cfg.MaxIPs, false)
	if err := setPreferredIPs(cfg, merged, r.cfg.SetSubMode); err != nil {
		return "", err
	}
	updated, err := json.Marshal(cfg)
	if err != nil {
		return "", err
	}
	saveBackup(r.cfg.DataDir, configBytes)
	postHeaders := map[string]string{"Content-Type": "application/json"}
	for k, v := range apiHeaders {
		postHeaders[k] = v
	}
	resp, status, err := r.do(ctx, http.MethodPost, apiURL, updated, postHeaders)
	if err != nil {
		return "", fmt.Errorf("写入面板配置失败: %w", err)
	}
	if status < 200 || status >= 300 {
		return "", fmt.Errorf("写入面板配置返回 HTTP %d: %s", status, brief(resp))
	}
	var ack struct {
		OK  bool   `json:"ok"`
		Msg string `json:"msg"`
	}
	_ = json.Unmarshal(resp, &ack)
	if !ack.OK && strings.TrimSpace(string(resp)) != "" {
		log.Printf("[cfnext] 面板保存返回未确认: %s", brief(resp))
	}
	return fmt.Sprintf("panel: 原有 %d 条 / 新增 %d 条 / 共 %d 条", len(remote), added, len(merged)), nil
}

func extractPanelConfig(raw []byte) []byte {
	var env struct {
		OK   bool            `json:"ok"`
		Data json.RawMessage `json:"data"`
	}
	if json.Unmarshal(raw, &env) == nil && len(env.Data) > 0 {
		return env.Data
	}
	return raw
}

func brief(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > 180 {
		s = s[:180] + "..."
	}
	return strings.ReplaceAll(s, "\n", " ")
}

func saveBackup(dir string, original []byte) {
	backupDir := filepath.Join(dir, "backups")
	if err := os.MkdirAll(backupDir, 0700); err != nil {
		log.Printf("[cfnext] 备份目录创建失败: %v", err)
		return
	}
	name := fmt.Sprintf("config-%s.json", time.Now().UTC().Format("20060102-150405"))
	path := filepath.Join(backupDir, name)
	if err := os.WriteFile(path, original, 0600); err != nil {
		log.Printf("[cfnext] 备份写入失败: %v", err)
		return
	}
	log.Printf("[cfnext] 写入前配置已备份: %s", path)
}
