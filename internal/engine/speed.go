package engine

import (
	"context"
	"io"
	"net"
	"net/http"
	"strconv"
	"time"
)

// testSingleSpeed 对单个目标执行下载测速，返回速度（kB/s）。
// 测速失败或 ctx 取消时返回 0。
//
// 原理与延迟测试相同：直连目标 IP，劫持 HTTP 传输层，
// 请求测速文件并在限定时间窗口内统计下载字节数。
func testSingleSpeed(ctx context.Context, target Target, opts SpeedOptions) float64 {
	timeout := 1 * time.Second
	if opts.DurationSec > 0 {
		timeout = time.Duration(opts.DurationSec) * time.Second
	}

	scheme := "https://"
	if !opts.EnableTLS {
		scheme = "http://"
	}
	url := scheme + opts.DownloadURL

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return 0
	}
	req.Header.Set("User-Agent", "Mozilla/5.0")
	req.Header.Set("Referer", "https://speed.cloudflare.com/")
	req.Close = true

	// 直连目标 IP
	dialer := &net.Dialer{Timeout: 1 * time.Second}
	conn, err := dialer.DialContext(ctx, "tcp", net.JoinHostPort(target.IP, strconv.Itoa(target.Port)))
	if err != nil {
		return 0
	}
	defer conn.Close()

	// ctx 取消时强制断开连接以中断下载
	stopWatch := context.AfterFunc(ctx, func() { conn.Close() })
	defer stopWatch()

	client := &http.Client{
		Transport: &http.Transport{
			DialContext: func(context.Context, string, string) (net.Conn, error) {
				return conn, nil
			},
		},
		Timeout: timeout, // 单 IP 测速最长时间
	}

	startTime := time.Now()
	resp, err := client.Do(req)
	if err != nil {
		return 0
	}
	defer resp.Body.Close()

	written, _ := io.Copy(io.Discard, resp.Body)
	duration := time.Since(startTime)
	if duration <= 0 || written <= 0 {
		return 0
	}
	return float64(written) / duration.Seconds() / 1024
}
