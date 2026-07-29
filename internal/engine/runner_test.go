package engine

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// newFakeCFServer 起一个假的 Cloudflare 边缘节点：
// 对 /cdn-cgi/trace 返回合法 trace 文本（含 UA 回显与 colo/loc），
// 使 testSingleIP 判定其为真实 CF 节点。
//
// 这样 MaxResults 的行为就能在本地闭环验证，不依赖公网可达性——
// 「凑够 N 个就停」这类语义正是最不该靠手工点界面来确认的。
func newFakeCFServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "/cdn-cgi/trace") {
			http.NotFound(w, r)
			return
		}
		ua := r.Header.Get("User-Agent")
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("fl=1a2b3c\nh=fake\nip=203.0.113.7\nts=1720000000.1\n" +
			"colo=NRT\nloc=JP\nuag=" + ua + "\nwarp=off\ntls=TLSv1.3\nhttp=http/1.1\n"))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// fakeCFRunner 返回一个把 trace 请求指向 fake 服务器的 Runner，
// 以及指向该服务器的 N 个目标（同一 host:port 重复 N 次即可，
// 测试关心的是「产出多少个有效结果」而非 IP 各不相同）。
func fakeCFRunner(t *testing.T, targetCount int) (*Runner, []Target) {
	t.Helper()
	srv := newFakeCFServer(t)

	u := strings.TrimPrefix(srv.URL, "http://")
	host, portStr, err := net.SplitHostPort(u)
	if err != nil {
		t.Fatalf("无法解析测试服务器地址 %q: %v", srv.URL, err)
	}
	port, _ := strconv.Atoi(portStr)

	r := &Runner{
		locations: map[string]Location{
			"NRT": {Iata: "NRT", Country: "日本", CityZh: "东京", Emoji: "🇯🇵"},
		},
		traceURL: u + "/cdn-cgi/trace",
	}

	targets := make([]Target, targetCount)
	for i := range targets {
		targets[i] = Target{IP: host, Port: port}
	}
	return r, targets
}

func latencyTestOpts() LatencyOptions {
	opts := DefaultLatencyOptions()
	opts.EnableTLS = false // fake 服务器是明文 HTTP
	opts.TimeoutMs = 3000
	opts.MaxConcurrency = 4
	return opts
}

// TestLatencyMaxResultsZeroTestsEverything 是本次改动最关键的回归用例。
//
// MaxResults 语义：0 = 不限制、全部测完。若实现把 0 误当成「上限为 0，
// 立刻停止」，测试会一个结果都不返回——这是该语义最容易写错的方向。
func TestLatencyMaxResultsZeroTestsEverything(t *testing.T) {
	const n = 12
	r, targets := fakeCFRunner(t, n)

	opts := latencyTestOpts()
	opts.MaxResults = 0

	var reason DoneReason
	results, err := r.RunLatencyTest(context.Background(), targets, opts, func(ev Event) {
		if ev.Type == EventDone {
			reason = ev.Reason
		}
	})
	if err != nil {
		t.Fatalf("MaxResults=0 不应返回错误，实际: %v", err)
	}
	if len(results) != n {
		t.Errorf("MaxResults=0 得到 %d 个结果，期望全部 %d 个", len(results), n)
	}
	if reason != DoneCompleted {
		t.Errorf("结束原因 = %q，期望 %q", reason, DoneCompleted)
	}
}

// TestLatencyMaxResultsNegativeTestsEverything 负数同样视为不限制。
func TestLatencyMaxResultsNegativeTestsEverything(t *testing.T) {
	const n = 6
	r, targets := fakeCFRunner(t, n)

	opts := latencyTestOpts()
	opts.MaxResults = -1

	results, err := r.RunLatencyTest(context.Background(), targets, opts, func(Event) {})
	if err != nil {
		t.Fatalf("MaxResults=-1 不应返回错误，实际: %v", err)
	}
	if len(results) != n {
		t.Errorf("MaxResults=-1 得到 %d 个结果，期望 %d 个", len(results), n)
	}
}

// TestLatencyMaxResultsStopsEarly 验证凑够即停，且不会超量返回。
func TestLatencyMaxResultsStopsEarly(t *testing.T) {
	const total, limit = 200, 5
	r, targets := fakeCFRunner(t, total)

	opts := latencyTestOpts()
	opts.MaxResults = limit

	var mu sync.Mutex
	var resultEvents int
	var reason DoneReason
	results, err := r.RunLatencyTest(context.Background(), targets, opts, func(ev Event) {
		mu.Lock()
		defer mu.Unlock()
		switch ev.Type {
		case EventResult:
			resultEvents++
		case EventDone:
			reason = ev.Reason
		}
	})

	if !errors.Is(err, ErrResultLimitReached) {
		t.Errorf("期望返回 ErrResultLimitReached，实际: %v", err)
	}
	if len(results) != limit {
		t.Errorf("得到 %d 个结果，期望恰好 %d 个（并发下不应超量）", len(results), limit)
	}
	if resultEvents != limit {
		t.Errorf("推送了 %d 个 result 事件，期望 %d 个", resultEvents, limit)
	}
	if reason != DoneLimit {
		t.Errorf("结束原因 = %q，期望 %q", reason, DoneLimit)
	}
}

// TestLatencyMaxResultsAboveTotal 上限大于目标总数时应正常测完，
// 结束原因是「完成」而非「达到上限」。
func TestLatencyMaxResultsAboveTotal(t *testing.T) {
	const n = 5
	r, targets := fakeCFRunner(t, n)

	opts := latencyTestOpts()
	opts.MaxResults = 100

	var reason DoneReason
	results, err := r.RunLatencyTest(context.Background(), targets, opts, func(ev Event) {
		if ev.Type == EventDone {
			reason = ev.Reason
		}
	})
	if err != nil {
		t.Fatalf("不应返回错误，实际: %v", err)
	}
	if len(results) != n {
		t.Errorf("得到 %d 个结果，期望 %d 个", len(results), n)
	}
	if reason != DoneCompleted {
		t.Errorf("结束原因 = %q，期望 %q", reason, DoneCompleted)
	}
}

// TestLatencyUserStopIsDistinguishable 验证「用户停止」与「达到上限」
// 能被区分——两者内部都是 context 取消，若不加标志就会混为一谈。
func TestLatencyUserStopIsDistinguishable(t *testing.T) {
	r, targets := fakeCFRunner(t, 50)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // 开跑前就取消，模拟用户点「停止」

	opts := latencyTestOpts()
	opts.MaxResults = 5

	var reason DoneReason
	_, err := r.RunLatencyTest(ctx, targets, opts, func(ev Event) {
		if ev.Type == EventDone {
			reason = ev.Reason
		}
	})

	if !errors.Is(err, context.Canceled) {
		t.Errorf("用户停止应返回 context.Canceled，实际: %v", err)
	}
	if errors.Is(err, ErrResultLimitReached) {
		t.Error("用户停止被误报为「达到最大结果数」")
	}
	if reason != DoneStopped {
		t.Errorf("结束原因 = %q，期望 %q", reason, DoneStopped)
	}
}

// TestSpeedMaxResultsZeroTestsEverything 是测速侧的 0=不限制回归用例。
func TestSpeedMaxResultsZeroTestsEverything(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(make([]byte, 64*1024))
	}))
	defer srv.Close()

	u := strings.TrimPrefix(srv.URL, "http://")
	host, portStr, _ := net.SplitHostPort(u)
	port, _ := strconv.Atoi(portStr)

	const n = 6
	targets := make([]Target, n)
	for i := range targets {
		targets[i] = Target{IP: host, Port: port}
	}

	opts := DefaultSpeedOptions()
	opts.EnableTLS = false
	opts.DownloadURL = u + "/down"
	opts.MaxConcurrency = 3
	opts.DurationSec = 3
	opts.MaxResults = 0

	var mu sync.Mutex
	var speedEvents int
	var reason DoneReason
	r := &Runner{}
	err := r.RunSpeedTest(context.Background(), targets, opts, func(ev Event) {
		mu.Lock()
		defer mu.Unlock()
		switch ev.Type {
		case EventSpeed:
			speedEvents++
		case EventDone:
			reason = ev.Reason
		}
	})
	if err != nil {
		t.Fatalf("MaxResults=0 不应返回错误，实际: %v", err)
	}
	if speedEvents != n {
		t.Errorf("MaxResults=0 推送 %d 个速度事件，期望全部 %d 个", speedEvents, n)
	}
	if reason != DoneCompleted {
		t.Errorf("结束原因 = %q，期望 %q", reason, DoneCompleted)
	}
}

// TestSpeedMaxResultsStopsEarly 验证测速阶段也能凑够即停。
func TestSpeedMaxResultsStopsEarly(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(make([]byte, 64*1024))
	}))
	defer srv.Close()

	u := strings.TrimPrefix(srv.URL, "http://")
	host, portStr, _ := net.SplitHostPort(u)
	port, _ := strconv.Atoi(portStr)

	const total, limit = 60, 3
	targets := make([]Target, total)
	for i := range targets {
		targets[i] = Target{IP: host, Port: port}
	}

	opts := DefaultSpeedOptions()
	opts.EnableTLS = false
	opts.DownloadURL = u + "/down"
	opts.MaxConcurrency = 2
	opts.DurationSec = 3
	opts.MaxResults = limit

	var mu sync.Mutex
	var speedEvents int
	var reason DoneReason
	r := &Runner{}
	err := r.RunSpeedTest(context.Background(), targets, opts, func(ev Event) {
		mu.Lock()
		defer mu.Unlock()
		switch ev.Type {
		case EventSpeed:
			speedEvents++
		case EventDone:
			reason = ev.Reason
		}
	})

	if !errors.Is(err, ErrResultLimitReached) {
		t.Errorf("期望返回 ErrResultLimitReached，实际: %v", err)
	}
	if speedEvents != limit {
		t.Errorf("推送了 %d 个速度事件，期望恰好 %d 个", speedEvents, limit)
	}
	if reason != DoneLimit {
		t.Errorf("结束原因 = %q，期望 %q", reason, DoneLimit)
	}
}
