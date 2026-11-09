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
)

type RunStatus struct {
	Time     string `json:"time"`
	Sequence int    `json:"sequence"`
	ScanIPs  int    `json:"scan_ips"`
	Report   string `json:"report,omitempty"`
	Error    string `json:"error,omitempty"`
	Duration string `json:"duration"`
}

func main() {
	log.SetFlags(log.LstdFlags | log.LUTC)
	_ = os.MkdirAll("/tmp", 0777)
	if tz := os.Getenv("TZ"); tz != "" {
		if loc, err := time.LoadLocation(tz); err == nil {
			time.Local = loc
		}
	}

	cfg, err := loadConfig()
	if err != nil {
		log.Fatalf("[init] 配置错误: %v", err)
	}
	if err := os.MkdirAll(cfg.DataDir, 0755); err != nil {
		log.Fatalf("[init] 数据目录创建失败: %v", err)
	}
	if err := ensureCLIConfig(cfg.DataDir); err != nil {
		log.Fatalf("[init] CLI 配置初始化失败: %v", err)
	}
	rep, err := NewReporter(cfg)
	if err != nil {
		log.Fatalf("[init] 上报器初始化失败: %v", err)
	}

	log.Printf("[init] CFScan-Docker: web=%v port=%s auto=%v interval=%s at=%q scan=%s channel=%s dryRun=%v",
		cfg.WebEnabled, cfg.WebPort, cfg.AutoEnabled, cfg.AutoInterval, cfg.AutoAt, cfg.ScanMode, cfg.Channel, cfg.ScanDryRun)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if cfg.RunOnce {
		log.Printf("[init] 单次任务模式（不启动 Web / 不进入调度循环）")
		runOnce(ctx, cfg, rep, 1)
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

	seq := 0
	if cfg.AutoEnabled && cfg.RunOnStart {
		seq++
		runOnce(ctx, cfg, rep, seq)
	}
	if !cfg.AutoEnabled {
		log.Printf("[auto] 定时任务已关闭，仅运行 Web 服务")
		<-ctx.Done()
		return
	}

	for {
		next := nextRun(cfg, time.Now())
		log.Printf("[auto] 下次任务: %s", next.Format(time.RFC3339))
		timer := time.NewTimer(time.Until(next))
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
			if ctx.Err() != nil {
				return
			}
			seq++
			runOnce(ctx, cfg, rep, seq)
		}
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
