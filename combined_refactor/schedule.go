package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
)

const (
	scheduleServiceName = "cfdata-schedule.service"
	scheduleTimerName   = "cfdata-schedule.timer"
)

func scheduleUnitPaths() (servicePath string, timerPath string) {
	dir := scheduleBaseDir()
	return filepath.Join(dir, scheduleServiceName), filepath.Join(dir, scheduleTimerName)
}

func scheduleBaseDir() string {
	exe, err := os.Executable()
	if err != nil || exe == "" {
		return "."
	}
	return filepath.Dir(exe)
}

func scheduleStatus() (string, bool) {
	if runtime.GOOS != "linux" {
		return "暂不支持", false
	}
	if _, err := exec.LookPath("systemctl"); err != nil {
		return "暂不支持", false
	}
	out, err := exec.Command("systemctl", "is-enabled", scheduleTimerName).CombinedOutput()
	if err == nil && strings.TrimSpace(string(out)) == "enabled" {
		activeOut, activeErr := exec.Command("systemctl", "is-active", scheduleTimerName).CombinedOutput()
		if activeErr == nil && strings.TrimSpace(string(activeOut)) == "active" {
			return "已启用", true
		}
		return "已启用（未运行）", true
	}
	return "已关闭", false
}

func scheduleDescFromFile() string {
	_, timerPath := scheduleUnitPaths()
	data, err := os.ReadFile(timerPath)
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "# schedule:") {
			return strings.TrimSpace(strings.TrimPrefix(line, "# schedule:"))
		}
	}
	return ""
}

func runSystemctl(args ...string) (string, error) {
	out, err := exec.Command("systemctl", args...).CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

func checkSchedulePrerequisites() bool {
	if runtime.GOOS != "linux" {
		fmt.Println("定时任务暂不支持当前系统（仅支持 Linux systemd）")
		return false
	}
	if _, err := exec.LookPath("systemctl"); err != nil {
		fmt.Println("定时任务暂不支持：当前系统未找到 systemctl")
		return false
	}
	if os.Geteuid() != 0 {
		fmt.Println("需要 root 权限：请使用 sudo 运行程序后再设置定时任务")
		return false
	}
	return true
}

func runScheduleMenu() {
	for {
		status, enabled := scheduleStatus()
		sectionHeader("定时任务")
		if enabled {
			if desc := scheduleDescFromFile(); desc != "" {
				fmt.Printf("  当前状态: 已启用（%s）\n", desc)
			} else {
				fmt.Println("  当前状态: 已启用")
			}
		} else {
			fmt.Printf("  当前状态: %s\n", status)
		}
		fmt.Println("  1. 开启定时任务")
		fmt.Println("  2. 关闭定时任务")
		fmt.Println("  0. 返回菜单")
		fmt.Print("请选择> ")
		line, outcome := readMenuLine()
		if outcome != promptOK {
			return
		}
		switch strings.TrimSpace(line) {
		case "1":
			if enabled {
				fmt.Println("    定时任务已开启，如需修改请先关闭再重新开启")
				continue
			}
			configureSchedule()
		case "2":
			if !enabled {
				fmt.Println("    定时任务当前未开启")
				continue
			}
			disableSchedule()
		case "0":
			return
		default:
			fmt.Println("请输入 0-2 之间的数字")
		}
	}
}

func configureSchedule() {
	if !checkSchedulePrerequisites() {
		return
	}
	freqIdx, o := askChoice("执行频率", "定时任务的执行周期",
		[]string{"按天（每天同一时间）", "按周（每周指定星期与时间）", "按月（每月指定日期与时间）"}, 0)
	if o != promptOK {
		return
	}
	hour, minute, o := askTimeValue("执行时间")
	if o != promptOK {
		return
	}
	onCalendar := ""
	desc := ""
	switch freqIdx {
	case 0:
		onCalendar = fmt.Sprintf("*-*-* %02d:%02d:00", hour, minute)
		desc = fmt.Sprintf("每天 %02d:%02d", hour, minute)
	case 1:
		weekday, o := askInt("星期几", "1", "1=周一、2=周二 … 7=周日", 1, 1, 7)
		if o != promptOK {
			return
		}
		weekdayNames := []string{"Mon", "Tue", "Wed", "Thu", "Fri", "Sat", "Sun"}
		weekdayCN := []string{"一", "二", "三", "四", "五", "六", "日"}
		onCalendar = fmt.Sprintf("%s *-*-* %02d:%02d:00", weekdayNames[weekday-1], hour, minute)
		desc = fmt.Sprintf("每周%s %02d:%02d", weekdayCN[weekday-1], hour, minute)
	case 2:
		day, o := askInt("几号", "1", "1-31（当月没有该日期时跳过本次执行）", 1, 1, 31)
		if o != promptOK {
			return
		}
		onCalendar = fmt.Sprintf("*-*-%02d %02d:%02d:00", day, hour, minute)
		desc = fmt.Sprintf("每月%d日 %02d:%02d", day, hour, minute)
	}
	skipIdx, o := askChoice("跳过代理检测", "定时任务无交互终端，若不跳过，执行时遇到代理/VPN 警告会直接中止本次任务并记入日志",
		[]string{"开启（跳过，直接开始测试）", "关闭（执行代理检测）"}, 1)
	if o != promptOK {
		return
	}
	skipGeo := skipIdx == 0

	exe, err := os.Executable()
	if err != nil || exe == "" {
		fmt.Println("无法获取程序路径，设置失败")
		return
	}
	dir := filepath.Dir(exe)
	execStart := exe
	if strings.ContainsAny(exe, " \t") {
		execStart = strconv.Quote(exe)
	}
	execStart += " -cli qs"
	if skipGeo {
		execStart += " -skipgeo"
	} else {
		execStart += " -skipgeo=false"
	}
	logPath := filepath.Join(dir, "cfdata-schedule.log")
	servicePath := filepath.Join(dir, scheduleServiceName)
	timerPath := filepath.Join(dir, scheduleTimerName)

	serviceContent := fmt.Sprintf(`[Unit]
Description=CFData-WEB 定时优选任务
After=network-online.target
Wants=network-online.target

[Service]
Type=oneshot
WorkingDirectory=%s
ExecStart=%s
StandardOutput=append:%s
StandardError=append:%s
`, dir, execStart, logPath, logPath)
	timerContent := fmt.Sprintf(`[Unit]
Description=CFData-WEB 定时优选任务

[Timer]
# schedule: %s
OnCalendar=%s
Persistent=true
Unit=%s

[Install]
WantedBy=timers.target
`, desc, onCalendar, scheduleServiceName)

	if err := os.WriteFile(servicePath, []byte(serviceContent), 0644); err != nil {
		fmt.Printf("写入服务文件失败: %v\n", err)
		return
	}
	if err := os.WriteFile(timerPath, []byte(timerContent), 0644); err != nil {
		fmt.Printf("写入定时器文件失败: %v\n", err)
		return
	}

	steps := [][]string{
		{"daemon-reload"},
		{"link", servicePath},
		{"daemon-reload"},
		{"enable", "--now", timerPath},
	}
	for _, step := range steps {
		if out, err := runSystemctl(step...); err != nil {
			fmt.Printf("systemctl %s 失败: %v\n", strings.Join(step, " "), err)
			if out != "" {
				fmt.Printf("%s\n", out)
			}
			rollbackScheduleFiles(servicePath, timerPath)
			return
		}
	}
	fmt.Printf("定时任务已开启: %s\n", desc)
	fmt.Printf("跳过代理检测: %s\n", map[bool]string{true: "开启", false: "关闭"}[skipGeo])
	fmt.Printf("执行日志: %s\n", logPath)
}

func rollbackScheduleFiles(servicePath, timerPath string) {
	runSystemctl("disable", "--now", scheduleTimerName)
	removeScheduleSymlink(servicePath)
	removeScheduleSymlink(timerPath)
	os.Remove(servicePath)
	os.Remove(timerPath)
	runSystemctl("daemon-reload")
	fmt.Println("设置失败，已回滚清理残留文件")
}

func removeScheduleSymlink(unitPath string) {
	link := filepath.Join("/etc/systemd/system", filepath.Base(unitPath))
	fi, err := os.Lstat(link)
	if err != nil || fi.Mode()&os.ModeSymlink == 0 {
		return
	}
	target, err := os.Readlink(link)
	if err != nil || target != unitPath {
		return
	}
	os.Remove(link)
}

func disableSchedule() {
	if !checkSchedulePrerequisites() {
		return
	}
	servicePath, timerPath := scheduleUnitPaths()
	runSystemctl("disable", "--now", scheduleTimerName)
	removeScheduleSymlink(servicePath)
	removeScheduleSymlink(timerPath)
	if err := os.Remove(servicePath); err != nil && !os.IsNotExist(err) {
		fmt.Printf("删除服务文件失败: %v\n", err)
	}
	if err := os.Remove(timerPath); err != nil && !os.IsNotExist(err) {
		fmt.Printf("删除定时器文件失败: %v\n", err)
	}
	runSystemctl("daemon-reload")
	fmt.Println("定时任务已关闭")
}
