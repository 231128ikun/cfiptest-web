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
	"sync/atomic"
	"testing"
	"time"
)

func TestCombinedPipelineUsesMinimumConcurrencyAndFinalLimit(t *testing.T) {
	var active int64
	var maxActive int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		current := atomic.AddInt64(&active, 1)
		defer atomic.AddInt64(&active, -1)
		for {
			seen := atomic.LoadInt64(&maxActive)
			if current <= seen || atomic.CompareAndSwapInt64(&maxActive, seen, current) {
				break
			}
		}
		time.Sleep(15 * time.Millisecond)
		if strings.Contains(req.URL.Path, "cdn-cgi/trace") {
			_, _ = w.Write([]byte("ip=203.0.113.7\ncolo=NRT\nloc=JP\nuag=Mozilla/5.0\n"))
			return
		}
		_, _ = w.Write(make([]byte, 32*1024))
	}))
	defer srv.Close()

	u := strings.TrimPrefix(srv.URL, "http://")
	host, portText, _ := net.SplitHostPort(u)
	port, _ := strconv.Atoi(portText)
	runner := testRunner(map[string]Location{"NRT": {Country: "日本"}}, u+"/cdn-cgi/trace")
	targets := make([]Target, 20)
	for i := range targets {
		targets[i] = Target{IP: host, Port: port}
	}
	latency := DefaultLatencyOptions()
	latency.EnableTLS = false
	latency.MaxConcurrency = 10
	latency.TimeoutMs = 2000
	speed := DefaultSpeedOptions()
	speed.EnableTLS = false
	speed.DownloadURL = u + "/down"
	speed.MaxConcurrency = 2
	speed.DurationSec = 2
	speed.MaxResults = 3

	var reason DoneReason
	results, err := runner.RunCombinedTest(context.Background(), targets, latency, speed, func(ev Event) {
		if ev.Type == EventDone {
			reason = ev.Reason
		}
	})
	if !errors.Is(err, ErrResultLimitReached) {
		t.Fatalf("err=%v", err)
	}
	if len(results) != 3 {
		t.Fatalf("results=%d, want 3", len(results))
	}
	if reason != DoneLimit {
		t.Fatalf("reason=%s", reason)
	}
	if maxActive > 2 {
		t.Fatalf("并发=%d，超过 min(10,2)", maxActive)
	}
	for _, result := range results {
		if result.DownloadSpeedKBs <= 0 {
			t.Fatal("最终结果缺少速度")
		}
	}
}

// TestCombinedLimitDoesNotStreamResultsEarly 有数量上限（MaxResults>0）时，
// 延迟合格不能提前推送 EventResult——否则前端表格/导出会把所有延迟合格 IP
// 都累积起来，最终数量超过约束。只有「延迟+测速」双达标的结果才推送，
// result / speed 事件数都应恰好等于上限。
func TestCombinedLimitDoesNotStreamResultsEarly(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if strings.Contains(req.URL.Path, "cdn-cgi/trace") {
			_, _ = w.Write([]byte("ip=203.0.113.7\ncolo=NRT\nloc=JP\nuag=Mozilla/5.0\n"))
			return
		}
		_, _ = w.Write(make([]byte, 32*1024))
	}))
	defer srv.Close()

	u := strings.TrimPrefix(srv.URL, "http://")
	host, portText, _ := net.SplitHostPort(u)
	port, _ := strconv.Atoi(portText)
	r := testRunner(map[string]Location{"NRT": {Country: "日本"}}, u+"/cdn-cgi/trace")
	targets := make([]Target, 12)
	for i := range targets {
		targets[i] = Target{IP: host, Port: port}
	}

	latency := DefaultLatencyOptions()
	latency.EnableTLS = false
	latency.TimeoutMs = 2000
	latency.MaxConcurrency = 4
	speed := DefaultSpeedOptions()
	speed.EnableTLS = false
	speed.DownloadURL = u + "/down"
	speed.MaxConcurrency = 2
	speed.DurationSec = 1
	speed.MaxResults = 3

	var mu sync.Mutex
	var resultEvents, speedEvents int
	results, err := r.RunCombinedTest(context.Background(), targets, latency, speed, func(ev Event) {
		mu.Lock()
		defer mu.Unlock()
		switch ev.Type {
		case EventResult:
			resultEvents++
		case EventSpeed:
			speedEvents++
		}
	})
	if !errors.Is(err, ErrResultLimitReached) {
		t.Fatalf("err=%v", err)
	}
	if len(results) != 3 {
		t.Fatalf("results=%d, want 3", len(results))
	}
	if resultEvents != 3 {
		t.Errorf("result 事件=%d，期望恰好 3（有上限时不应提前推送延迟合格结果）", resultEvents)
	}
	if speedEvents != 3 {
		t.Errorf("speed 事件=%d，期望 3", speedEvents)
	}
}

// TestCombinedNoLimitStreamsResultsProgressively 无数量上限（MaxResults==0）时
// 保持渐进式展示：每个延迟合格的 IP 都推送 result，测速完成再推送 speed 回填。
func TestCombinedNoLimitStreamsResultsProgressively(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if strings.Contains(req.URL.Path, "cdn-cgi/trace") {
			_, _ = w.Write([]byte("ip=203.0.113.7\ncolo=NRT\nloc=JP\nuag=Mozilla/5.0\n"))
			return
		}
		_, _ = w.Write(make([]byte, 32*1024))
	}))
	defer srv.Close()

	u := strings.TrimPrefix(srv.URL, "http://")
	host, portText, _ := net.SplitHostPort(u)
	port, _ := strconv.Atoi(portText)
	r := testRunner(map[string]Location{"NRT": {Country: "日本"}}, u+"/cdn-cgi/trace")
	const n = 4
	targets := make([]Target, n)
	for i := range targets {
		targets[i] = Target{IP: host, Port: port}
	}

	latency := DefaultLatencyOptions()
	latency.EnableTLS = false
	latency.TimeoutMs = 2000
	latency.MaxConcurrency = 2
	speed := DefaultSpeedOptions()
	speed.EnableTLS = false
	speed.DownloadURL = u + "/down"
	speed.MaxConcurrency = 2
	speed.DurationSec = 1
	speed.MaxResults = 0

	var mu sync.Mutex
	var resultEvents, speedEvents int
	results, err := r.RunCombinedTest(context.Background(), targets, latency, speed, func(ev Event) {
		mu.Lock()
		defer mu.Unlock()
		switch ev.Type {
		case EventResult:
			resultEvents++
		case EventSpeed:
			speedEvents++
		}
	})
	if err != nil {
		t.Fatalf("MaxResults=0 不应返回错误: %v", err)
	}
	if len(results) != n {
		t.Fatalf("results=%d, want %d", len(results), n)
	}
	if resultEvents != n {
		t.Errorf("result 事件=%d，期望 %d（无上限时每个延迟合格 IP 都应渐进推送）", resultEvents, n)
	}
	if speedEvents != n {
		t.Errorf("speed 事件=%d，期望 %d", speedEvents, n)
	}
}

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

	r := testRunner(map[string]Location{
		"NRT": {Iata: "NRT", Country: "日本", CityZh: "东京", Emoji: "🇯🇵"},
	}, u+"/cdn-cgi/trace")

	targets := make([]Target, targetCount)
	for i := range targets {
		targets[i] = Target{IP: host, Port: port}
	}
	return r, targets
}

// testRunner 构造一个直接使用给定位置表与 trace 地址的 Runner（绕过磁盘与网络）。
func testRunner(locations map[string]Location, traceURL string) *Runner {
	r := &Runner{traceURL: traceURL, locations: locations}
	// r.locations.Store(&locations) -- locations now a plain map
	return r
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

// TestLatencyInvalidTargetEmitsTargetDone 无效目标（连接/校验失败）也要发
// target_done：前端的候选漏斗按「是否完整检测过」移除，而不是按「是否有有效结果」。
func TestLatencyInvalidTargetEmitsTargetDone(t *testing.T) {
	// 找一个必定连接失败的端口：先监听拿一个空闲端口，再立即关闭。
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("无法占端口: %v", err)
	}
	addr := l.Addr().String()
	_ = l.Close()
	host, portText, _ := net.SplitHostPort(addr)
	port, _ := strconv.Atoi(portText)

	r := testRunner(map[string]Location{}, "http://"+addr+"/cdn-cgi/trace")
	target := Target{IP: host, Port: port}
	opts := latencyTestOpts()
	opts.MaxConcurrency = 1

	var doneTargets []Target
	_, err = r.RunLatencyTest(context.Background(), []Target{target}, opts, func(ev Event) {
		if ev.Type == EventTargetDone && ev.Target != nil {
			doneTargets = append(doneTargets, *ev.Target)
		}
	})
	if err != nil {
		t.Fatalf("无效目标不应让任务报错: %v", err)
	}
	if len(doneTargets) != 1 {
		t.Fatalf("target_done 次数 = %d，期望 1", len(doneTargets))
	}
	if doneTargets[0] != target {
		t.Errorf("target_done 回传 = %+v，期望 %+v", doneTargets[0], target)
	}
}

// TestLatencyTargetDoneCarriesKey 有效目标每个都发 target_done，
// 且事件携带前端原始候选键（端口 0 在解析阶段被补成 443，键仍为 ip|0）。
func TestLatencyTargetDoneCarriesKey(t *testing.T) {
	r, realTargets := fakeCFRunner(t, 1)
	// 端口 0 的候选在 server 层被解析成 443，但目标键必须是原始键 ip|0
	target := Target{IP: realTargets[0].IP, Port: realTargets[0].Port, Key: realTargets[0].IP + "|0"}
	opts := latencyTestOpts()
	opts.MaxConcurrency = 1

	var keys []string
	results, err := r.RunLatencyTest(context.Background(), []Target{target}, opts, func(ev Event) {
		if ev.Type == EventTargetDone && ev.Target != nil {
			keys = append(keys, ev.Target.Key)
		}
	})
	if err != nil {
		t.Fatalf("有效目标不应报错: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("有效结果 = %d，期望 1", len(results))
	}
	if len(keys) != 1 || keys[0] != target.Key {
		t.Fatalf("target_done keys = %v，期望 [%s]", keys, target.Key)
	}
}

// TestLatencyTraceMissingColoReportsFailure trace 响应缺少 colo 时，目标判定为
// 无效并下发带 Failure 的 target_done（缺 colo = 不是 CF 节点）。
func TestLatencyTraceMissingColoReportsFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "/cdn-cgi/trace") {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte("ip=203.0.113.7\nloc=JP\nuag=Mozilla/5.0\n"))
	}))
	defer srv.Close()
	u := strings.TrimPrefix(srv.URL, "http://")
	host, portText, _ := net.SplitHostPort(u)
	port, _ := strconv.Atoi(portText)

	r := testRunner(map[string]Location{}, u+"/cdn-cgi/trace")
	target := Target{IP: host, Port: port}
	opts := latencyTestOpts()
	opts.MaxConcurrency = 1

	var failures []*ProbeFailure
	results, err := r.RunLatencyTest(context.Background(), []Target{target}, opts, func(ev Event) {
		if ev.Type == EventTargetDone && ev.Target != nil {
			failures = append(failures, ev.Failure)
		}
	})
	if err != nil {
		t.Fatalf("无效目标不应让任务报错: %v", err)
	}
	if len(results) != 0 {
		t.Fatalf("有效结果 = %d，期望 0", len(results))
	}
	if len(failures) != 1 {
		t.Fatalf("target_done 次数 = %d，期望 1", len(failures))
	}
	if failures[0] == nil || failures[0].Stage != FailureTraceColo {
		t.Fatalf("失败原因 = %+v，期望 stage=%s", failures[0], FailureTraceColo)
	}
}

// TestLatencyTraceColoWithoutLocPasses trace 响应只有 colo、缺 loc/uag 时仍应
// 判定为有效 CF 节点（宽松判定，避免误杀可用节点）。
func TestLatencyTraceColoWithoutLocPasses(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "/cdn-cgi/trace") {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte("ip=203.0.113.7\ncolo=NRT\n"))
	}))
	defer srv.Close()
	u := strings.TrimPrefix(srv.URL, "http://")
	host, portText, _ := net.SplitHostPort(u)
	port, _ := strconv.Atoi(portText)

	r := testRunner(map[string]Location{}, u+"/cdn-cgi/trace")
	target := Target{IP: host, Port: port}
	opts := latencyTestOpts()
	opts.MaxConcurrency = 1

	var failures []*ProbeFailure
	results, err := r.RunLatencyTest(context.Background(), []Target{target}, opts, func(ev Event) {
		if ev.Type == EventTargetDone && ev.Target != nil {
			failures = append(failures, ev.Failure)
		}
	})
	if err != nil {
		t.Fatalf("有效目标不应报错: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("有效结果 = %d，期望 1", len(results))
	}
	if len(failures) != 1 {
		t.Fatalf("target_done 次数 = %d，期望 1", len(failures))
	}
	if failures[0] != nil {
		t.Fatalf("有效目标不应带失败原因: %+v", failures[0])
	}
}

// TestLatencyPreCancelledDoesNotEmitTargetDone 开跑前就取消：没有目标真正
// 完成，不能把任何候选从漏斗里移除。
func TestLatencyPreCancelledDoesNotEmitTargetDone(t *testing.T) {
	r, targets := fakeCFRunner(t, 20)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	var doneCount int
	_, _ = r.RunLatencyTest(ctx, targets, latencyTestOpts(), func(ev Event) {
		if ev.Type == EventTargetDone {
			doneCount++
		}
	})
	if doneCount != 0 {
		t.Fatalf("预取消仍发 %d 个 target_done，期望 0", doneCount)
	}
}

// TestCombinedStopDoesNotConsumeInflightTarget 组合任务测速阶段被打断时，
// 该目标虽然已发出延迟 EventResult，但未完整完成，不应发 target_done——
// 前端把它留在候选列表，下次续跑会用结果表里的延迟结果做 upsert。
func TestCombinedStopDoesNotConsumeInflightTarget(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if strings.Contains(req.URL.Path, "cdn-cgi/trace") {
			_, _ = w.Write([]byte("ip=203.0.113.7\ncolo=NRT\nloc=JP\nuag=Mozilla/5.0\n"))
			return
		}
		// 测速阶段慢速响应：取消时请求尚未结束
		time.Sleep(800 * time.Millisecond)
		_, _ = w.Write(make([]byte, 1024))
	}))
	defer srv.Close()

	u := strings.TrimPrefix(srv.URL, "http://")
	host, portStr, _ := net.SplitHostPort(u)
	port, _ := strconv.Atoi(portStr)

	targets := make([]Target, 5)
	for i := range targets {
		targets[i] = Target{IP: host, Port: port, Key: host + "|" + portStr}
	}

	latency := DefaultLatencyOptions()
	latency.EnableTLS = false
	latency.TimeoutMs = 2000
	latency.MaxConcurrency = 1
	speed := DefaultSpeedOptions()
	speed.EnableTLS = false
	speed.DownloadURL = u + "/down"
	speed.MaxConcurrency = 1
	speed.DurationSec = 3
	speed.MinSpeedKBs = 0

	ctx, cancel := context.WithCancel(context.Background())
	var resultEvents, doneEvents int
	go func() {
		time.Sleep(150 * time.Millisecond)
		cancel()
	}()
	r := testRunner(map[string]Location{"NRT": {Iata: "NRT", Country: "日本"}}, u+"/cdn-cgi/trace")
	_, _ = r.RunCombinedTest(ctx, targets, latency, speed, func(ev Event) {
		switch ev.Type {
		case EventResult:
			resultEvents++
		case EventTargetDone:
			doneEvents++
		}
	})
	if resultEvents == 0 {
		t.Fatal("应至少发出一个 latency result（测速被打断前延迟已完成）")
	}
	if doneEvents != 0 {
		t.Fatalf("被打断的目标不应从漏斗移除，target_done = %d，期望 0", doneEvents)
	}
}
