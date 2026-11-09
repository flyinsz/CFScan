package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
)

func shouldWarnProxyCountry(country string) bool {
	country = strings.ToUpper(strings.TrimSpace(country))
	return country == "" || (country != "CN" && country != "XX" && country != "T1")
}

func detectCloudflareTraceCountry(ctx context.Context) (string, bool) {
	var lastErr string
	for attempt := 1; attempt <= 3; attempt++ {
		country, ok, errText := detectCloudflareTraceCountryOnce(ctx)
		if ok {
			return country, true
		}
		lastErr = errText
	}
	if lastErr != "" {
		recordDebugError("proxy_country_check", "三次检测失败: "+lastErr)
	}
	return "", false
}

func detectCloudflareTraceCountryOnce(ctx context.Context) (string, bool, string) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://www.cloudflare.com/cdn-cgi/trace", nil)
	if err != nil {
		return "", false, err.Error()
	}
	req.Header.Set("User-Agent", "CFData-WEB/"+appVersion)
	resp, err := upstreamHTTPClient.Do(req)
	if err != nil {
		recordDebugError("proxy_country_check", err.Error())
		return "", false, err.Error()
	}
	defer resp.Body.Close()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		errText := fmt.Sprintf("Cloudflare trace status %d", resp.StatusCode)
		recordDebugError("proxy_country_check", errText)
		return "", false, errText
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 16*1024))
	if err != nil {
		recordDebugError("proxy_country_check", err.Error())
		return "", false, err.Error()
	}
	for _, line := range strings.Split(string(data), "\n") {
		key, value, ok := strings.Cut(strings.TrimSpace(line), "=")
		if ok && key == "loc" {
			country := strings.ToUpper(strings.TrimSpace(value))
			return country, country != "", ""
		}
	}
	return "", false, "Cloudflare trace missing loc"
}

func confirmCLIProxyCountry(country string, ok bool) bool {
	if ok && !shouldWarnProxyCountry(country) {
		return true
	}
	if target, err := os.Readlink("/proc/self/fd/0"); err == nil && target == "/dev/null" {
		fmt.Println("[proxy-check] 非交互环境无法确认代理警告，已中止本次任务（如为定时任务请查看日志）")
		return false
	}
	displayCountry := strings.TrimSpace(country)
	if displayCountry == "" {
		displayCountry = "未知"
	}
	fmt.Println("🚨 代理检测警告")
	fmt.Printf("读取到的网络标签：%s\n", displayCountry)
	if ok {
		fmt.Println("检测到您当前很可能处于代理/VPN环境中！")
	} else {
		fmt.Println("地区验证失败，无法确认当前是否处于代理/VPN环境中！")
	}
	fmt.Println()
	fmt.Println("在代理状态下进行的IP优选测试结果将不准确，可能导致：")
	fmt.Println("- 延迟数据失真，无法反映真实网络状况")
	fmt.Println("- 优选出的IP在直连环境下表现不佳")
	fmt.Println("- 测试结果对实际使用场景参考价值有限")
	fmt.Println()
	fmt.Println("建议操作：请关闭所有代理软件（VPN、科学上网工具等），确保处于直连网络环境后重新开始。")
	fmt.Print("是否强制继续？输入 y 继续，n 取消退出（默认 n）：")
	line, _ := menuInputReader.ReadString('\n')
	return strings.EqualFold(strings.TrimSpace(line), "y")
}
