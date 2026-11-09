package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// ensureCLIConfig pre-creates a minimal config so CFScan CLI never exits on first-run template creation.
func ensureCLIConfig(dir string) error {
	path := filepath.Join(dir, "cfdata-config.json")
	if _, err := os.Stat(path); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := os.WriteFile(path, []byte("{}\n"), 0600); err != nil {
		return err
	}
	log.Printf("[scan] 已创建 CLI 配置: %s", path)
	return nil
}

func runScan(ctx context.Context, cfg Config) ([]PreferredIP, error) {
	if cfg.ScanDryRun {
		log.Printf("[scan] DRY RUN: 跳过真实扫描，使用 SCAN_TEST_IPS")
		ips, err := parseScanResults([]byte(cfg.TestIPs))
		if err != nil {
			return nil, err
		}
		if len(ips) == 0 {
			return nil, fmt.Errorf("SCAN_DRY_RUN=true 但 SCAN_TEST_IPS 中没有可用 IP")
		}
		return ips, nil
	}
	if err := ensureCLIConfig(cfg.DataDir); err != nil {
		return nil, err
	}
	if _, err := os.Stat(cfg.BinaryPath); err != nil {
		return nil, fmt.Errorf("找不到 cfscan 二进制 %s: %w", cfg.BinaryPath, err)
	}

	args := []string{
		"-cli",
		"-mode=" + cfg.ScanMode,
		"-config=" + filepath.Join(cfg.DataDir, "cfdata-config.json"),
		"-out=scan-results",
		"-outformat=txt",
		"-outfields=ipport",
		"-skipgeo=true",
		"-nocolor=true",
		"-progress=false",
		"-outendrow=" + cfg.ScanRows,
		"-offiptype=" + cfg.ScanIPType,
		"-offport=" + cfg.ScanPort,
		"-offthreads=" + cfg.ScanThreads,
		"-offdelay=" + cfg.ScanDelay,
		"-offspeedlimit=" + cfg.ScanSpeedLimit,
		"-offspeedmin=" + cfg.ScanSpeedMin,
	}
	cmd := exec.CommandContext(ctx, cfg.BinaryPath, args...)
	cmd.Dir = cfg.DataDir
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = os.Environ()
	log.Printf("[scan] 执行: %s %s", cfg.BinaryPath, strings.Join(args, " "))
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("CFScan CLI 执行失败: %w", err)
	}

	resultPath := filepath.Join(cfg.DataDir, "scan-results."+ "txt")
	data, err := os.ReadFile(resultPath)
	if err != nil {
		return nil, fmt.Errorf("读取扫描结果 %s 失败: %w", resultPath, err)
	}
	ips, err := parseScanResults(data)
	if err != nil {
		return nil, err
	}
	if len(ips) == 0 {
		return nil, fmt.Errorf("扫描结果为空（%s），本次不上报", resultPath)
	}
	return ips, nil
}
