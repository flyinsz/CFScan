package main

import (
	"context"
	"encoding/json"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"
	_ "time/tzdata"

	autoconf "cfscan/shared"
)

type RunStatus struct {
	Time     string `json:"time"`
	Sequence int    `json:"sequence"`
	ScanIPs  int    `json:"scan_ips"`
	Report   string `json:"report,omitempty"`
	Error    string `json:"error,omitempty"`
	Duration string `json:"duration"`
}

// AutoState is polled by the Web UI.
type AutoState struct {
	NextRun     string `json:"next_run,omitempty"`
	AutoEnabled bool   `json:"auto_enabled"`
	Channel     string `json:"channel"`
	Running     bool   `json:"running"`
	UpdatedAt   string `json:"updated_at"`
}

type runtimeState struct {
	cfg Config
	rep *Reporter
}

func newRuntimeState() (*runtimeState, error) {
	cfg, err := loadConfig()
	if err != nil {
		return nil, err
	}
	rep, err := NewReporter(cfg)
	if err != nil {
		return nil, err
	}
	return &runtimeState{cfg: cfg, rep: rep}, nil
}

func main() {
	log.SetFlags(log.LstdFlags | log.LUTC)
	_ = os.MkdirAll("/tmp", 0777)
	if tz := os.Getenv("TZ"); tz != "" {
		if loc, err := time.LoadLocation(tz); err == nil {
			time.Local = loc
		}
	}

	rt, err := newRuntimeState()
	if err != nil {
		log.Fatalf("[init] 配置错误: %v", err)
	}
	cfg := rt.cfg
	if err := os.MkdirAll(cfg.DataDir, 0755); err != nil {
		log.Fatalf("[init] 数据目录创建失败: %v", err)
	}
	if err := ensureCLIConfig(cfg.DataDir); err != nil {
		log.Fatalf("[init] CLI 配置初始化失败: %v", err)
	}

	log.Printf("[init] CFScan-Docker: web=%v port=%s auto=%v interval=%s at=%q scan=%s channel=%s dryRun=%v",
		cfg.WebEnabled, cfg.WebPort, cfg.AutoEnabled, cfg.AutoInterval, cfg.AutoAt, cfg.ScanMode, cfg.Channel, cfg.ScanDryRun)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if cfg.RunOnce {
		log.Printf("[init] 单次任务模式（不启动 Web / 不进入调度循环）")
		runOnce(ctx, cfg, rt.rep, 1)
		return
	}

	var web *exec.Cmd
	if cfg.WebEnabled {
		web = startWeb(cfg)
	}

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sig
		log.Printf("[init] 收到退出信号")
		cancel()
		if web != nil && web.Process != nil {
			_ = web.Process.Signal(syscall.SIGTERM)
		}
	}()

	store := autoconf.Store{Dir: cfg.DataDir}
	confMod := currentModTime(store)
	seq := 0
	if cfg.AutoEnabled && cfg.RunOnStart {
		seq++
		runOnce(ctx, rt.cfg, rt.rep, seq)
	}

	// Unified loop: recompute the next run on every config change, and keep
	// serving manual triggers even when the schedule is disabled.
	for {
		var next time.Time
		if rt.cfg.AutoEnabled {
			next = nextRun(rt.cfg, time.Now())
			log.Printf("[auto] 下次任务: %s", next.Format(time.RFC3339))
		} else {
			next = time.Now().Add(24 * time.Hour)
			log.Printf("[auto] 定时任务已关闭（面板仍可手动触发）")
		}
		writeState(store, AutoState{
			NextRun:     next.Format(time.RFC3339),
			AutoEnabled: rt.cfg.AutoEnabled,
			Channel:     rt.cfg.Channel,
		})

		timer := time.NewTimer(time.Until(next))
		ticker := time.NewTicker(2 * time.Second)
		restart := false
		for !restart {
			select {
			case <-ctx.Done():
				timer.Stop()
				ticker.Stop()
				return
			case <-timer.C:
				if ctx.Err() != nil {
					return
				}
				if rt.cfg.AutoEnabled {
					seq++
					runOnce(ctx, rt.cfg, rt.rep, seq)
				}
				restart = true
			case <-ticker.C:
				if configChanged(store, &confMod) {
					nextRT, err := newRuntimeState()
					if err != nil {
						log.Printf("[auto] 新配置无效，保持原配置: %v", err)
					} else {
						// Only reschedule when the schedule itself changed; saving
						// credentials must not push the next run 6h into the future.
						scheduleChanged := nextRT.cfg.AutoEnabled != rt.cfg.AutoEnabled ||
							nextRT.cfg.AutoInterval != rt.cfg.AutoInterval ||
							nextRT.cfg.AutoAt != rt.cfg.AutoAt
						rt = nextRT
						log.Printf("[auto] 配置已热加载: auto=%v interval=%s at=%q channel=%s 调度重算=%v",
							rt.cfg.AutoEnabled, rt.cfg.AutoInterval, rt.cfg.AutoAt, rt.cfg.Channel, scheduleChanged)
						if scheduleChanged {
							restart = true
						} else {
							writeState(store, AutoState{
								NextRun:     next.Format(time.RFC3339),
								AutoEnabled: rt.cfg.AutoEnabled,
								Channel:     rt.cfg.Channel,
							})
						}
					}
				}
				if takeTrigger(store) {
					seq++
					log.Printf("[auto] 收到手动触发（第 %d 次任务）", seq)
					runOnce(ctx, rt.cfg, rt.rep, seq)
					restart = true
				}
			}
		}
		timer.Stop()
		ticker.Stop()
	}
}

func startWeb(cfg Config) *exec.Cmd {
	args := []string{"-port=" + cfg.WebPort, "-host=0.0.0.0", "-skipgeo=true"}
	if cfg.WebUser != "" && cfg.WebPass != "" {
		args = append(args, "-user="+cfg.WebUser, "-password="+cfg.WebPass)
	}
	cmd := exec.Command(cfg.BinaryPath, args...)
	cmd.Dir = cfg.DataDir
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		log.Printf("[web] 启动失败: %v", err)
		return nil
	}
	log.Printf("[web] 已启动，端口 %s", cfg.WebPort)
	go func() {
		if err := cmd.Wait(); err != nil {
			log.Printf("[web] 进程退出: %v", err)
		}
	}()
	return cmd
}

func nextRun(cfg Config, now time.Time) time.Time {
	if cfg.AutoAt != "" {
		h, m, _ := parseHHMM(cfg.AutoAt)
		local := now.In(time.Local)
		n := time.Date(local.Year(), local.Month(), local.Day(), h, m, 0, 0, time.Local)
		if !n.After(now) {
			n = n.AddDate(0, 0, 1)
		}
		return n
	}
	return now.Add(cfg.AutoInterval)
}

func runOnce(ctx context.Context, cfg Config, rep *Reporter, seq int) {
	store := autoconf.Store{Dir: cfg.DataDir}
	setRunning(store, true)
	defer setRunning(store, false)

	start := time.Now()
	st := RunStatus{Time: start.UTC().Format(time.RFC3339), Sequence: seq}
	log.Printf("[auto] === 第 %d 次任务开始 ===", seq)

	scanCtx, cancelScan := context.WithTimeout(ctx, cfg.ScanTimeout)
	ips, err := runScan(scanCtx, cfg)
	cancelScan()
	if err != nil {
		st.Error = err.Error()
		st.Duration = time.Since(start).String()
		writeStatus(cfg.DataDir, st)
		log.Printf("[auto] 第 %d 次任务失败（扫描阶段）: %v", seq, err)
		return
	}
	st.ScanIPs = len(ips)
	log.Printf("[auto] 扫描得到 %d 个优选 IP", len(ips))

	reportCtx, cancelReport := context.WithTimeout(ctx, cfg.HTTPTimeout*4)
	summary, err := rep.Report(reportCtx, ips)
	cancelReport()
	if err != nil {
		st.Error = err.Error()
		st.Duration = time.Since(start).String()
		writeStatus(cfg.DataDir, st)
		log.Printf("[auto] 第 %d 次任务失败（上报阶段）: %v", seq, err)
		return
	}
	st.Report = summary
	st.Duration = time.Since(start).String()
	writeStatus(cfg.DataDir, st)
	log.Printf("[auto] 第 %d 次任务完成: %s (%s)", seq, summary, st.Duration)
}

func writeStatus(dir string, st RunStatus) {
	b, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return
	}
	path := filepath.Join(dir, "last-run.json")
	if err := os.WriteFile(path, append(b, '\n'), 0600); err != nil {
		log.Printf("[auto] 状态写入失败: %v", err)
	}
}

func writeState(s autoconf.Store, st AutoState) {
	st.UpdatedAt = time.Now().Format(time.RFC3339)
	b, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return
	}
	tmp := s.StatePath() + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0600); err != nil {
		return
	}
	_ = os.Rename(tmp, s.StatePath())
}

func setRunning(s autoconf.Store, running bool) {
	var st AutoState
	if b, err := os.ReadFile(s.StatePath()); err == nil {
		_ = json.Unmarshal(b, &st)
	}
	st.Running = running
	writeState(s, st)
}

func currentModTime(s autoconf.Store) time.Time {
	if fi, err := os.Stat(s.ConfigPath()); err == nil {
		return fi.ModTime()
	}
	return time.Time{}
}

func configChanged(s autoconf.Store, last *time.Time) bool {
	fi, err := os.Stat(s.ConfigPath())
	if err != nil {
		if !last.IsZero() {
			*last = time.Time{} // config removed -> fall back to env
			return true
		}
		return false
	}
	if !fi.ModTime().Equal(*last) {
		*last = fi.ModTime()
		return true
	}
	return false
}

func takeTrigger(s autoconf.Store) bool {
	if _, err := os.Stat(s.TriggerPath()); err != nil {
		return false
	}
	_ = os.Remove(s.TriggerPath())
	return true
}
