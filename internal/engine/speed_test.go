package engine

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

// TestSpeedSubMillisecondDownloadIsNotZero 回归用例：下载快到计时器测不出来时，
// 不能报告速度 0。
//
// 背景：Windows 时钟分辨率约 1ms，环回连接下小文件的 time.Since 可能返回 0。
// 旧实现 `if duration <= 0 || written <= 0 { return 0 }` 会把这种情况当成失败，
// 而 0 在调用方意味着「该目标不可用、过滤掉」——最快的目标反被丢弃。
// 这个用例在环回上跑，正是最容易触发该条件的环境。
func TestSpeedSubMillisecondDownloadIsNotZero(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(make([]byte, 64*1024))
	}))
	defer srv.Close()

	u := strings.TrimPrefix(srv.URL, "http://")
	host, portStr, _ := net.SplitHostPort(u)
	port, _ := strconv.Atoi(portStr)

	opts := DefaultSpeedOptions()
	opts.EnableTLS = false
	opts.DownloadURL = u + "/down"
	opts.DurationSec = 3

	// 单次可能碰巧超过 1ms，跑多次才能稳定覆盖「快到测不出」的分支
	for i := 0; i < 50; i++ {
		speed := testSingleSpeed(context.Background(), Target{IP: host, Port: port}, opts)
		if speed <= 0 {
			t.Fatalf("第 %d 次测速返回 %v；环回下载成功却报 0，会被上层当作目标不可用", i, speed)
		}
	}
}
