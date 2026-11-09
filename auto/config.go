package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
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
		MaxIPs:         envInt("CFNEXT_MAX_IPS", 100),
		SetSubMode:     envBool("CFNEXT_SET_SUBMODE", true),
		HTTPTimeout:    envDur("HTTP_TIMEOUT", 30*time.Second),
	}

	switch c.ScanMode {
	case "official", "nsb":
	default:
		return c, fmt.Errorf("SCAN_MODE 仅支持 official / nsb，当前=%q", c.ScanMode)
	}
	switch c.Channel {
	case "auto", "kv", "panel", "dry":
	default:
		return c, fmt.Errorf("CFNEXT_CHANNEL 仅支持 auto / kv / panel / dry，当前=%q", c.Channel)
	}
	if c.AutoAt != "" {
		if _, _, ok := parseHHMM(c.AutoAt); !ok {
			return c, fmt.Errorf("AUTO_AT 格式应为 HH:MM，当前=%q", c.AutoAt)
		}
	}
	if c.Channel == "kv" || c.Channel == "auto" {
		if c.CFAccount == "" || c.CFNamespace == "" || c.CFToken == "" {
			return c, fmt.Errorf("CFNEXT_CHANNEL=%s 需要 CF_ACCOUNT_ID / CF_KV_NAMESPACE_ID / CF_API_TOKEN", c.Channel)
		}
	}
	if c.Channel == "panel" {
		if c.PanelURL == "" || c.PanelPassword == "" {
			return c, fmt.Errorf("CFNEXT_CHANNEL=panel 需要 CFNEXT_PANEL_URL / CFNEXT_PASSWORD")
		}
	}
	return c, nil
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
