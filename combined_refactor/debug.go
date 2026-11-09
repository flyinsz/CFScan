package main

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const debugLogFileName = "cfdata-debug.log"
const debugLogMaxBytes int64 = 50 * 1024 * 1024
const debugLogKeepBytes int64 = 25 * 1024 * 1024

var debugLogMutex sync.Mutex

func recordDebugError(source string, detail interface{}) {
	recordDebugByLevel("error", source, detail)
}

func recordProgramDebugError(source string, detail interface{}) {
	recordDebugByLevel("error", source, detail)
}

func recordAllDebugError(source string, detail interface{}) {
	recordDebugByLevel("all", source, detail)
}

func recordDebugByLevel(level string, source string, detail interface{}) {
	if !debugMode {
		return
	}
	if !debugLevelEnabled(level) {
		return
	}
	text := strings.TrimSpace(fmt.Sprint(detail))
	if text == "" || text == "<nil>" {
		return
	}
	line := fmt.Sprintf("[debug-error] %s source=%s detail=%s", time.Now().Format(time.RFC3339Nano), source, text)
	fmt.Println(line)
	appendDebugLogLine(line)
}

func debugLevelEnabled(level string) bool {
	configured := normalizeDebugLevel(debugLevel)
	level = normalizeDebugLevel(level)
	if configured == "all" {
		return true
	}
	return level == "error"
}

func normalizeDebugLevel(level string) string {
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "error", "err", "program":
		return "error"
	case "all", "full", "全部":
		return "all"
	default:
		return "error"
	}
}

func setDebugFlag(value string) error {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" || value == "1" || value == "true" || value == "yes" || value == "y" || value == "on" {
		debugMode = true
		debugLevel = "error"
		return nil
	}
	if value == "0" || value == "false" || value == "no" || value == "n" || value == "off" {
		debugMode = false
		debugLevel = "error"
		return nil
	}
	debugMode = true
	debugLevel = normalizeDebugLevel(value)
	return nil
}

func recordDebugNotice(source string, detail interface{}) {
	if !debugMode {
		return
	}
	text := strings.TrimSpace(fmt.Sprint(detail))
	if text == "" || !looksLikeProblem(text) {
		return
	}
	recordDebugByLevel("error", source, text)
}

func appendDebugLogLine(line string) {
	if !debugMode {
		return
	}
	debugLogMutex.Lock()
	defer debugLogMutex.Unlock()
	path := defaultDebugLogPath()
	rotateDebugLogIfNeeded(path)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		fmt.Printf("[debug-error] %s source=debug_log detail=写入 debug log 失败: %v\n", time.Now().Format(time.RFC3339Nano), err)
		return
	}
	defer f.Close()
	_, _ = f.WriteString(line + "\n")
}

func rotateDebugLogIfNeeded(path string) {
	info, err := os.Stat(path)
	if err != nil || info.Size() <= debugLogMaxBytes {
		return
	}
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()
	if _, err := f.Seek(-debugLogKeepBytes, 2); err != nil {
		return
	}
	buf, err := io.ReadAll(f)
	if err != nil {
		return
	}
	marker := fmt.Sprintf("[debug-error] %s source=debug_log detail=日志超过 50MB，已截断保留最近 25MB\n", time.Now().Format(time.RFC3339Nano))
	_ = os.WriteFile(path, append([]byte(marker), buf...), 0644)
}

func defaultDebugLogPath() string {
	if cwd, err := os.Getwd(); err == nil && strings.TrimSpace(cwd) != "" {
		return filepath.Join(cwd, debugLogFileName)
	}
	exe, err := os.Executable()
	if err != nil || strings.TrimSpace(exe) == "" {
		return debugLogFileName
	}
	return filepath.Join(filepath.Dir(exe), debugLogFileName)
}

type debugRoundTripper struct {
	name string
	next http.RoundTripper
}

type debugFlagValue struct{}

func (debugFlagValue) String() string {
	if !debugMode {
		return "false"
	}
	return normalizeDebugLevel(debugLevel)
}

func (debugFlagValue) Set(value string) error {
	return setDebugFlag(value)
}

func (debugFlagValue) IsBoolFlag() bool {
	return true
}

func (t debugRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	next := t.next
	if next == nil {
		next = http.DefaultTransport
	}
	resp, err := next.RoundTrip(req)
	if err != nil {
		recordDebugByLevel(httpDebugLevel(t.name), "http_error", fmt.Sprintf("client=%s method=%s url=%s err=%v", t.name, req.Method, req.URL.String(), err))
		return resp, err
	}
	if resp != nil && resp.StatusCode >= http.StatusMultipleChoices {
		recordDebugByLevel(httpDebugLevel(t.name), "http_status", fmt.Sprintf("client=%s method=%s url=%s status=%s", t.name, req.Method, req.URL.String(), resp.Status))
	}
	return resp, err
}

func httpDebugLevel(clientName string) string {
	if strings.Contains(clientName, "speed") || strings.Contains(clientName, "trace") {
		return "all"
	}
	return "error"
}

func wrapDebugTransport(name string, next http.RoundTripper) http.RoundTripper {
	return debugRoundTripper{name: name, next: next}
}

func looksLikeProblem(text string) bool {
	lower := strings.ToLower(text)
	keywords := []string{
		"错误", "失败", "异常", "超时", "终止", "跳过", "未找到", "未发现", "无效", "为空", "不匹配", "无法", "损坏", "重试", "未就绪", "警告", "提醒", "⚠", "panic",
		"error", "fail", "failed", "timeout", "invalid", "empty", "not found", "skip", "retry", "warning", "warn", "panic", "refused", "reset", "denied", "429", "500", "502", "503", "504",
	}
	for _, keyword := range keywords {
		if strings.Contains(lower, keyword) {
			return true
		}
	}
	return false
}
