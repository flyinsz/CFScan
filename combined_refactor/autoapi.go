package main

// Auto-task API: the Web UI edits /data/auto-config.json through these handlers;
// cfscan-auto hot-reloads the file and honours /data/run.trigger.

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	autoconf "cfscan/shared"
)

func registerAutoAPI() {
	http.HandleFunc("/api/auto/config", requireAuth(handleAutoConfig))
	http.HandleFunc("/api/auto/status", requireAuth(handleAutoStatus))
	http.HandleFunc("/api/auto/run", requireAuth(handleAutoRun))
	http.HandleFunc("/api/auto/test", requireAuth(handleAutoTest))
}

func autoStore() autoconf.Store {
	dir := strings.TrimSpace(os.Getenv("DATA_DIR"))
	if dir == "" {
		dir = "/data"
	}
	return autoconf.Store{Dir: dir}
}

func writeAutoJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func autoErr(w http.ResponseWriter, status int, err error) {
	writeAutoJSON(w, status, map[string]any{"ok": false, "error": err.Error()})
}

func handleAutoConfig(w http.ResponseWriter, r *http.Request) {
	st := autoStore()
	switch r.Method {
	case http.MethodGet:
		v, exists, err := st.Load()
		if err != nil {
			autoErr(w, http.StatusInternalServerError, err)
			return
		}
		source := "env"
		if exists {
			source = "file"
		}
		writeAutoJSON(w, http.StatusOK, map[string]any{
			"ok": true, "settings": autoconf.MaskSecrets(v), "source": source,
		})
	case http.MethodPost:
		var in autoconf.Settings
		dec := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
		if err := dec.Decode(&in); err != nil {
			autoErr(w, http.StatusBadRequest, err)
			return
		}
		cur, _, err := st.Load()
		if err != nil {
			autoErr(w, http.StatusInternalServerError, err)
			return
		}
		merged := autoconf.ResolveSecrets(in, cur)
		if err := st.Save(merged); err != nil {
			autoErr(w, http.StatusBadRequest, err)
			return
		}
		writeAutoJSON(w, http.StatusOK, map[string]any{
			"ok": true, "message": "已保存并通过 AES-GCM 加密落盘；调度器 2 秒内热加载，无需重启容器",
		})
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func handleAutoStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	st := autoStore()
	state := map[string]any{}
	if b, err := os.ReadFile(st.StatePath()); err == nil {
		_ = json.Unmarshal(b, &state)
	}
	var last map[string]any
	if b, err := os.ReadFile(st.StatusPath()); err == nil {
		_ = json.Unmarshal(b, &last)
	}
	_, pendingErr := os.Stat(st.TriggerPath())
	writeAutoJSON(w, http.StatusOK, map[string]any{
		"ok": true, "state": state, "last_run": last,
		"trigger_pending": pendingErr == nil,
	})
}

func handleAutoRun(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	st := autoStore()
	if err := os.MkdirAll(st.Dir, 0700); err != nil {
		autoErr(w, http.StatusInternalServerError, err)
		return
	}
	payload := []byte(time.Now().Format(time.RFC3339))
	if err := os.WriteFile(st.TriggerPath(), payload, 0600); err != nil {
		autoErr(w, http.StatusInternalServerError, err)
		return
	}
	writeAutoJSON(w, http.StatusOK, map[string]any{
		"ok": true, "message": "已排队：调度器将在 2 秒内执行一次「扫描 + 上报」",
	})
}

func handleAutoTest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	st := autoStore()
	cur, _, err := st.Load()
	if err != nil {
		autoErr(w, http.StatusInternalServerError, err)
		return
	}
	v := cur
	var in autoconf.Settings
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&in); err == nil && strings.TrimSpace(in.Channel) != "" {
		v = autoconf.ResolveSecrets(in, cur)
	}
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	msg, err := autoconf.Probe(ctx, v)
	if err != nil {
		autoErr(w, http.StatusBadRequest, err)
		return
	}
	writeAutoJSON(w, http.StatusOK, map[string]any{"ok": true, "message": msg})
}
