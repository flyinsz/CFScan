package main

import (
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
	"time"

	autoconf "cfscan/shared"
)

type Config struct {
	WebEnabled bool
	WebPort    string
	WebUser    string
	WebPass    string
	DataDir    string
	BinaryPath string

	AutoEnabled  bool
	AutoInterval time.Duration
	AutoAt       string
	RunOnStart   bool
	RunOnce      bool

	ScanMode       string
	ScanIPType     string
	ScanPort       string
	ScanThreads    string
	ScanDelay      string
	ScanSpeedLimit string
	ScanSpeedMin   string
	ScanRows       string
	ScanTimeout    time.Duration
	ScanDryRun     bool
	TestIPs        string

	Channel       string
	CFAccount     string
	CFNamespace   string
	CFToken       string
	PanelURL      string
	PanelPassword string
	PanelAPIKey   string
	APIBase       string
	MaxIPs        int
	SetSubMode    bool
	HTTPTimeout   time.Duration
}

func env(key, def string) string {
	if v, ok := os.LookupEnv(key); ok && strings.TrimSpace(v) != "" {
		return strings.TrimSpace(v)
	}
	return def
}

func envBool(key string, def bool) bool {
	switch strings.ToLower(env(key, "")) {
	case "":
		return def
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func envInt(key string, def int) int {
	v := env(key, "")
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		return def
	}
	return n
}

func envDur(key string, def time.Duration) time.Duration {
	v := env(key, "")
	if v == "" {
		return def
	}
	d, err := time.ParseDuration(v)
	if err != nil || d <= 0 {
		return def
	}
	return d
}

func durOr(s string, cur time.Duration) time.Duration {
	d, err := time.ParseDuration(s)
	if err != nil || d <= 0 {
		return cur
	}
	return d
}

func intOr(s string, cur int) int {
	n, err := strconv.Atoi(s)
	if err != nil || n <= 0 {
		return cur
	}
	return n
}

// loadConfig reads env defaults, then overlays the UI-managed config file if
// present. The scheduler calls it again on every hot reload.
func loadConfig() (Config, error) {
	c := Config{
		WebEnabled:     envBool("WEB_ENABLED", true),
		WebPort:        env("WEB_PORT", "13335"),
		WebUser:        env("WEB_USER", ""),
		WebPass:        env("WEB_PASS", ""),
		DataDir:        env("DATA_DIR", "/data"),
		BinaryPath:     env("CFSCAN_BIN", "/usr/local/bin/cfscan"),
		AutoEnabled:    envBool("AUTO_ENABLED", true),
		AutoInterval:   envDur("AUTO_INTERVAL", 6*time.Hour),
		AutoAt:         env("AUTO_AT", ""),
		RunOnStart:     envBool("AUTO_RUN_ON_START", true),
		RunOnce:        envBool("AUTO_RUN_ONCE", false),
		ScanMode:       strings.ToLower(env("SCAN_MODE", "official")),
		ScanIPType:     env("SCAN_IPTYPE", "4"),
		ScanPort:       env("SCAN_PORT", "443"),
		ScanThreads:    env("SCAN_THREADS", "100"),
		ScanDelay:      env("SCAN_DELAY", "300"),
		ScanSpeedLimit: env("SCAN_SPEED_LIMIT", "10"),
		ScanSpeedMin:   env("SCAN_SPEED_MIN", "1"),
		ScanRows:       env("SCAN_ROWS", "20"),
		ScanTimeout:    envDur("SCAN_TIMEOUT", 45*time.Minute),
		ScanDryRun:     envBool("SCAN_DRY_RUN", false),
		TestIPs:        env("SCAN_TEST_IPS", ""),
		Channel:        strings.ToLower(env("CFNEXT_CHANNEL", "auto")),
		CFAccount:      env("CF_ACCOUNT_ID", ""),
		CFNamespace:    env("CF_KV_NAMESPACE_ID", ""),
		CFToken:        env("CF_API_TOKEN", ""),
		PanelURL:       strings.TrimRight(env("CFNEXT_PANEL_URL", ""), "/"),
		PanelPassword:  env("CFNEXT_PASSWORD", ""),
		PanelAPIKey:    env("CFNEXT_API_KEY", ""),
		APIBase:        strings.TrimRight(env("CFNEXT_API_BASE", ""), "/"),
		MaxIPs:         envInt("CFNEXT_MAX_IPS", 100),
		SetSubMode:     envBool("CFNEXT_SET_SUBMODE", true),
		HTTPTimeout:    envDur("HTTP_TIMEOUT", 30*time.Second),
	}

	store := autoconf.Store{Dir: c.DataDir}
	if saved, exists, err := store.Load(); err != nil {
		log.Printf("[init] UI 配置读取失败，使用环境变量: %v", err)
	} else if exists {
		applySettings(&c, saved)
	}
	return c, validate(c)
}

// applySettings overlays UI-managed settings. Invalid numeric strings keep the
// env defaults so a bad form submission can never crash the scheduler.
func applySettings(c *Config, s autoconf.Settings) {
	c.AutoEnabled = s.AutoEnabled
	c.AutoInterval = durOr(s.AutoInterval, c.AutoInterval)
	c.AutoAt = s.AutoAt
	c.RunOnStart = s.RunOnStart
	c.ScanMode = strings.ToLower(s.ScanMode)
	c.ScanIPType = s.ScanIPType
	c.ScanPort = s.ScanPort
	c.ScanThreads = s.ScanThreads
	c.ScanDelay = s.ScanDelay
	c.ScanSpeedLimit = s.ScanSpeedLimit
	c.ScanSpeedMin = s.ScanSpeedMin
	c.ScanRows = s.ScanRows
	c.ScanTimeout = durOr(s.ScanTimeout, c.ScanTimeout)
	c.ScanDryRun = s.ScanDryRun
	c.Channel = strings.ToLower(s.Channel)
	c.CFAccount = s.CFAccount
	c.CFNamespace = s.CFNamespace
	c.CFToken = s.CFToken
	c.PanelURL = strings.TrimRight(s.PanelURL, "/")
	c.PanelPassword = s.PanelPassword
	c.PanelAPIKey = s.PanelAPIKey
	c.APIBase = strings.TrimRight(s.APIBase, "/")
	c.MaxIPs = intOr(strconv.Itoa(s.MaxIPs), c.MaxIPs)
	c.SetSubMode = s.SetSubMode
	c.HTTPTimeout = durOr(s.HTTPTimeout, c.HTTPTimeout)
}

func validate(c Config) error {
	switch c.ScanMode {
	case "official", "nsb":
	default:
		return fmt.Errorf("SCAN_MODE 仅支持 official / nsb，当前=%q", c.ScanMode)
	}
	switch c.Channel {
	case "auto", "kv", "panel", "dry":
	default:
		return fmt.Errorf("CFNEXT_CHANNEL 仅支持 auto / kv / panel / dry，当前=%q", c.Channel)
	}
	if c.AutoAt != "" {
		if _, _, ok := parseHHMM(c.AutoAt); !ok {
			return fmt.Errorf("AUTO_AT 格式应为 HH:MM，当前=%q", c.AutoAt)
		}
	}
	if c.Channel == "kv" || c.Channel == "auto" {
		if c.CFAccount == "" || c.CFNamespace == "" || c.CFToken == "" {
			return fmt.Errorf("CFNEXT_CHANNEL=%s 需要 CF_ACCOUNT_ID / CF_KV_NAMESPACE_ID / CF_API_TOKEN", c.Channel)
		}
	}
	if c.Channel == "panel" {
		if c.PanelURL == "" || c.PanelPassword == "" {
			return fmt.Errorf("CFNEXT_CHANNEL=panel 需要 CFNEXT_PANEL_URL / CFNEXT_PASSWORD")
		}
	}
	return nil
}

func parseHHMM(s string) (int, int, bool) {
	parts := strings.Split(s, ":")
	if len(parts) != 2 {
		return 0, 0, false
	}
	h, err1 := strconv.Atoi(parts[0])
	m, err2 := strconv.Atoi(parts[1])
	if err1 != nil || err2 != nil || h < 0 || h > 23 || m < 0 || m > 59 {
		return 0, 0, false
	}
	return h, m, true
}
