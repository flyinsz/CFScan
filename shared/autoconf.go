// Package autoconf holds the runtime settings shared by cfscan-auto (scheduler)
// and the cfscan Web UI. Secrets are AES-GCM encrypted at rest in /data.
package autoconf

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Mask is what the UI sends back for a secret it did not change.
const Mask = "********"

const (
	configFile = "auto-config.json"
	keyFile    = ".auto.key"
	triggerFile = "run.trigger"
	stateFile  = "auto-state.json"
	statusFile = "last-run.json"
	encPrefix  = "enc:"
)

// Settings is the complete editable configuration. Secret fields are plaintext
// in memory and encrypted only when written to disk by Store.
type Settings struct {
	AutoEnabled  bool   `json:"auto_enabled"`
	AutoInterval string `json:"auto_interval"`
	AutoAt       string `json:"auto_at"`
	RunOnStart   bool   `json:"run_on_start"`

	ScanMode       string `json:"scan_mode"`
	ScanIPType     string `json:"scan_iptype"`
	ScanPort       string `json:"scan_port"`
	ScanThreads    string `json:"scan_threads"`
	ScanDelay      string `json:"scan_delay"`
	ScanSpeedLimit string `json:"scan_speed_limit"`
	ScanSpeedMin   string `json:"scan_speed_min"`
	ScanRows       string `json:"scan_rows"`
	ScanTimeout    string `json:"scan_timeout"`
	ScanDryRun     bool   `json:"scan_dry_run"`

	Channel       string `json:"channel"`
	PanelURL      string `json:"panel_url"`
	PanelPassword string `json:"panel_password"`
	PanelAPIKey   string `json:"panel_api_key"`
	APIBase       string `json:"api_base"`
	CFAccount     string `json:"cf_account_id"`
	CFNamespace   string `json:"cf_namespace_id"`
	CFToken       string `json:"cf_api_token"`
	MaxIPs        int    `json:"max_ips"`
	SetSubMode    bool   `json:"set_submode"`
	HTTPTimeout   string `json:"http_timeout"`
}

func Defaults() Settings {
	return Settings{
		AutoEnabled: true, AutoInterval: "6h", AutoAt: "", RunOnStart: false,
		ScanMode: "official", ScanIPType: "4", ScanPort: "443", ScanThreads: "100",
		ScanDelay: "300", ScanSpeedLimit: "10", ScanSpeedMin: "1", ScanRows: "20",
		ScanTimeout: "45m", ScanDryRun: false,
		Channel: "kv", PanelURL: "https://cfn.vipchanel.com/donny-admin",
		MaxIPs: 20, SetSubMode: false, HTTPTimeout: "30s",
	}
}

func envOr(key, def string) string {
	if v, ok := os.LookupEnv(key); ok && strings.TrimSpace(v) != "" {
		return strings.TrimSpace(v)
	}
	return def
}

func envBoolOr(key string, def bool) bool {
	v, ok := os.LookupEnv(key)
	if !ok || strings.TrimSpace(v) == "" {
		return def
	}
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

func envIntOr(key string, def int) int {
	v, ok := os.LookupEnv(key)
	if !ok || strings.TrimSpace(v) == "" {
		return def
	}
	n, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil || n <= 0 {
		return def
	}
	return n
}

// FromEnv builds the initial settings from container environment variables.
func FromEnv() Settings {
	d := Defaults()
	return Settings{
		AutoEnabled:  envBoolOr("AUTO_ENABLED", d.AutoEnabled),
		AutoInterval: envOr("AUTO_INTERVAL", d.AutoInterval),
		AutoAt:       envOr("AUTO_AT", d.AutoAt),
		RunOnStart:   envBoolOr("AUTO_RUN_ON_START", d.RunOnStart),
		ScanMode:     envOr("SCAN_MODE", d.ScanMode),
		ScanIPType:   envOr("SCAN_IPTYPE", d.ScanIPType),
		ScanPort:     envOr("SCAN_PORT", d.ScanPort),
		ScanThreads:  envOr("SCAN_THREADS", d.ScanThreads),
		ScanDelay:    envOr("SCAN_DELAY", d.ScanDelay),
		ScanSpeedLimit: envOr("SCAN_SPEED_LIMIT", d.ScanSpeedLimit),
		ScanSpeedMin: envOr("SCAN_SPEED_MIN", d.ScanSpeedMin),
		ScanRows:     envOr("SCAN_ROWS", d.ScanRows),
		ScanTimeout:  envOr("SCAN_TIMEOUT", d.ScanTimeout),
		ScanDryRun:   envBoolOr("SCAN_DRY_RUN", d.ScanDryRun),
		Channel:      envOr("CFNEXT_CHANNEL", d.Channel),
		PanelURL:     envOr("CFNEXT_PANEL_URL", d.PanelURL),
		PanelPassword: envOr("CFNEXT_PASSWORD", ""),
		PanelAPIKey:  envOr("CFNEXT_API_KEY", ""),
		APIBase:      envOr("CFNEXT_API_BASE", ""),
		CFAccount:    envOr("CF_ACCOUNT_ID", ""),
		CFNamespace:  envOr("CF_KV_NAMESPACE_ID", ""),
		CFToken:      envOr("CF_API_TOKEN", ""),
		MaxIPs:       envIntOr("CFNEXT_MAX_IPS", d.MaxIPs),
		SetSubMode:   envBoolOr("CFNEXT_SET_SUBMODE", d.SetSubMode),
		HTTPTimeout:  envOr("HTTP_TIMEOUT", d.HTTPTimeout),
	}
}

// Store persists settings in the shared data volume.
type Store struct{ Dir string }

func (s Store) ConfigPath() string  { return filepath.Join(s.Dir, configFile) }
func (s Store) KeyPath() string     { return filepath.Join(s.Dir, keyFile) }
func (s Store) TriggerPath() string { return filepath.Join(s.Dir, triggerFile) }
func (s Store) StatePath() string   { return filepath.Join(s.Dir, stateFile) }
func (s Store) StatusPath() string  { return filepath.Join(s.Dir, statusFile) }

func (s Store) key() ([]byte, error) {
	if err := os.MkdirAll(s.Dir, 0700); err != nil {
		return nil, err
	}
	if b, err := os.ReadFile(s.KeyPath()); err == nil && len(b) >= 32 {
		return b[:32], nil
	}
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(s.KeyPath(), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err == nil {
		defer f.Close()
		if _, err = f.Write(b); err != nil {
			return nil, err
		}
		return b, nil
	}
	if errors.Is(err, os.ErrExist) {
		if b2, err := os.ReadFile(s.KeyPath()); err == nil && len(b2) >= 32 {
			return b2[:32], nil
		}
	}
	return nil, err
}

func seal(key []byte, plain string) (string, error) {
	if plain == "" {
		return "", nil
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err = rand.Read(nonce); err != nil {
		return "", err
	}
	ct := gcm.Seal(nil, nonce, []byte(plain), nil)
	return encPrefix + base64.StdEncoding.EncodeToString(append(nonce, ct...)), nil
}

func open(key []byte, stored string) (string, error) {
	if stored == "" || !strings.HasPrefix(stored, encPrefix) {
		return stored, nil // legacy plaintext
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(stored, encPrefix))
	if err != nil {
		return "", err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	if len(raw) < gcm.NonceSize() {
		return "", errors.New("密文过短")
	}
	plain, err := gcm.Open(nil, raw[:gcm.NonceSize()], raw[gcm.NonceSize():], nil)
	if err != nil {
		return "", err
	}
	return string(plain), nil
}

// Load returns decrypted settings. exists=false means no saved file yet; the
// returned settings are then the environment defaults.
func (s Store) Load() (Settings, bool, error) {
	b, err := os.ReadFile(s.ConfigPath())
	if errors.Is(err, os.ErrNotExist) {
		return FromEnv(), false, nil
	}
	if err != nil {
		return Settings{}, false, err
	}
	var v Settings
	if err := json.Unmarshal(b, &v); err != nil {
		return Settings{}, true, fmt.Errorf("配置文件解析失败: %w", err)
	}
	key, err := s.key()
	if err != nil {
		return Settings{}, true, err
	}
	for _, p := range []*string{&v.PanelPassword, &v.PanelAPIKey, &v.CFToken} {
		plain, err := open(key, *p)
		if err != nil {
			return Settings{}, true, fmt.Errorf("密钥解密失败（密钥文件可能被替换）: %w", err)
		}
		*p = plain
	}
	return v, true, nil
}

// Save validates, encrypts secrets and atomically replaces the config file.
func (s Store) Save(v Settings) error {
	if err := Validate(v); err != nil {
		return err
	}
	key, err := s.key()
	if err != nil {
		return err
	}
	enc := v
	if enc.PanelPassword, err = seal(key, v.PanelPassword); err != nil {
		return err
	}
	if enc.PanelAPIKey, err = seal(key, v.PanelAPIKey); err != nil {
		return err
	}
	if enc.CFToken, err = seal(key, v.CFToken); err != nil {
		return err
	}
	b, err := json.MarshalIndent(enc, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.ConfigPath() + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0600); err != nil {
		return err
	}
	return os.Rename(tmp, s.ConfigPath())
}

// MaskSecrets returns a UI-safe copy; secrets are never sent to the browser.
func MaskSecrets(v Settings) Settings {
	out := v
	for _, p := range []*string{&out.PanelPassword, &out.PanelAPIKey, &out.CFToken} {
		if *p != "" {
			*p = Mask
		}
	}
	return out
}

// ResolveSecrets keeps stored secrets when the UI echoes the mask.
func ResolveSecrets(incoming, current Settings) Settings {
	out := incoming
	pairs := [][2]*string{
		{&out.PanelPassword, &current.PanelPassword},
		{&out.PanelAPIKey, &current.PanelAPIKey},
		{&out.CFToken, &current.CFToken},
	}
	for _, pair := range pairs {
		if *pair[0] == Mask {
			*pair[0] = *pair[1]
		}
	}
	return out
}

// Validate checks every field the scheduler consumes.
func Validate(v Settings) error {
	switch strings.ToLower(strings.TrimSpace(v.Channel)) {
	case "auto", "kv", "panel", "dry":
	default:
		return fmt.Errorf("上报通道仅支持 auto / kv / panel / dry")
	}
	switch strings.ToLower(strings.TrimSpace(v.ScanMode)) {
	case "official", "nsb":
	default:
		return fmt.Errorf("扫描方式仅支持 official / nsb")
	}
	if v.AutoAt != "" {
		parts := strings.Split(v.AutoAt, ":")
		if len(parts) != 2 {
			return fmt.Errorf("每日时刻格式应为 HH:MM")
		}
		h, e1 := strconv.Atoi(parts[0])
		m, e2 := strconv.Atoi(parts[1])
		if e1 != nil || e2 != nil || h < 0 || h > 23 || m < 0 || m > 59 {
			return fmt.Errorf("每日时刻格式应为 HH:MM")
		}
	}
	for _, d := range []struct{ name, val string }{
		{"扫描间隔", v.AutoInterval}, {"扫描超时", v.ScanTimeout}, {"HTTP 超时", v.HTTPTimeout},
	} {
		if d.val == "" {
			continue
		}
		if _, err := time.ParseDuration(d.val); err != nil || d.val == "" {
			return fmt.Errorf("%s 不是合法时长（如 6h / 30m / 45m）", d.name)
		}
	}
	for _, n := range []struct{ name, val string }{
		{"端口", v.ScanPort}, {"线程", v.ScanThreads}, {"延迟", v.ScanDelay},
		{"结果行数", v.ScanRows}, {"速度上限", v.ScanSpeedLimit}, {"速度下限", v.ScanSpeedMin},
	} {
		if n.val == "" {
			continue
		}
		i, err := strconv.Atoi(n.val)
		if err != nil || i < 0 {
			return fmt.Errorf("%s 必须是数字", n.name)
		}
	}
	if v.MaxIPs <= 0 {
		return fmt.Errorf("最大 IP 数必须大于 0")
	}
	if ch := strings.ToLower(v.Channel); ch == "kv" || ch == "auto" {
		if v.CFAccount == "" || v.CFNamespace == "" || v.CFToken == "" {
			return fmt.Errorf("KV 通道需要 CF 账户 ID / KV 命名空间 ID / API Token")
		}
	}
	if strings.EqualFold(v.Channel, "panel") && (v.PanelURL == "" || v.PanelPassword == "") {
		return fmt.Errorf("面板通道需要面板地址与管理员密码")
	}
	return nil
}
