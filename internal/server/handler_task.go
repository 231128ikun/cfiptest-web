package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"sync"
	"time"

	"iptest-web/internal/engine"
)

// latencyOptionsDTO 延迟测试参数（指针字段区分"未提供"与"显式零值"）。
// 未提供的字段回落到 engine.DefaultLatencyOptions()。
type latencyOptionsDTO struct {
	MaxConcurrency *int  `json:"maxConcurrency"`
	TimeoutMs      *int  `json:"timeoutMs"`
	MaxLatencyMs   *int  `json:"maxLatencyMs"`
	MaxResults     *int  `json:"maxResults"`
	EnableTLS      *bool `json:"enableTLS"`
	EnableIPAPI    *bool `json:"enableIPAPI"`
}

// apply 把 DTO 叠加到 def 上；def 由 Server 持有（已含 config.json 的覆盖）。
func (d *latencyOptionsDTO) apply(def engine.LatencyOptions) engine.LatencyOptions {
	opts := def
	if d == nil {
		return opts
	}
	if d.MaxConcurrency != nil {
		opts.MaxConcurrency = *d.MaxConcurrency
	}
	if d.TimeoutMs != nil {
		opts.TimeoutMs = *d.TimeoutMs
	}
	if d.MaxLatencyMs != nil {
		opts.MaxLatencyMs = *d.MaxLatencyMs
	}
	// 显式传 0 表示「不限制」，故这里不做 > 0 判断
	if d.MaxResults != nil {
		opts.MaxResults = *d.MaxResults
	}
	if d.EnableTLS != nil {
		opts.EnableTLS = *d.EnableTLS
	}
	if d.EnableIPAPI != nil {
		opts.EnableIPAPI = *d.EnableIPAPI
	}
	return opts
}

// speedOptionsDTO 测速参数，语义同 latencyOptionsDTO。
type speedOptionsDTO struct {
	MaxConcurrency *int     `json:"maxConcurrency"`
	DurationSec    *int     `json:"durationSec"`
	MinSpeedKBs    *float64 `json:"minSpeedKBs"`
	MaxResults     *int     `json:"maxResults"`
	DownloadURL    *string  `json:"downloadURL"`
	EnableTLS      *bool    `json:"enableTLS"`
}

func (d *speedOptionsDTO) apply(def engine.SpeedOptions) engine.SpeedOptions {
	opts := def
	if d == nil {
		return opts
	}
	if d.MaxConcurrency != nil {
		opts.MaxConcurrency = *d.MaxConcurrency
	}
	if d.DurationSec != nil {
		opts.DurationSec = *d.DurationSec
	}
	if d.MinSpeedKBs != nil {
		opts.MinSpeedKBs = *d.MinSpeedKBs
	}
	if d.MaxResults != nil {
		opts.MaxResults = *d.MaxResults
	}
	if d.DownloadURL != nil && *d.DownloadURL != "" {
		opts.DownloadURL = *d.DownloadURL
	}
	if d.EnableTLS != nil {
		opts.EnableTLS = *d.EnableTLS
	}
	return opts
}

// latencyRequest 对应 POST /api/task/latency。
type latencyRequest struct {
	Targets []engine.Target    `json:"targets"`
	RawText string             `json:"rawText"` // 可选：直接传原始文本，后端代为解析
	Options *latencyOptionsDTO `json:"options"`

	// rawText 中的 CIDR 网段如何抽样。缺省为「每 /24 取 1 个」。
	SampleMode string `json:"sampleMode"` // "one"(默认) | "n" | "all"
	SampleN    int    `json:"sampleN"`    // sampleMode="n" 时每个 /24 取几个

	// 启用速度规则时，在同一个任务内完成「延迟 → 测速」流水线。
	EnableSpeed  bool             `json:"enableSpeed"`
	SpeedOptions *speedOptionsDTO `json:"speedOptions"`
}

// speedRequest 对应 POST /api/task/speed。targets 是前端从延迟结果中挑选的子集。
type speedRequest struct {
	Targets []engine.Target  `json:"targets"`
	Options *speedOptionsDTO `json:"options"`
}

type stopRequest struct {
	TaskID string `json:"taskId"`
}

type taskResponse struct {
	TaskID       string `json:"taskId"`
	Status       string `json:"status"`
	TotalTargets int    `json:"totalTargets"`
}

// isTaskFailure 判断一次任务的返回错误是否需要作为 error 事件推给前端。
//
// 两种「正常结束」不算失败：用户点停止（context.Canceled）与
// 达到最大结果数（ErrResultLimitReached，内部同样以取消实现）。
// 这两种情况 engine 已经发过带 reason 的 done 事件，再报错只会让界面
// 弹出一个莫名的失败提示。
func isTaskFailure(err error) bool {
	if err == nil {
		return false
	}
	return !errors.Is(err, context.Canceled) && !errors.Is(err, engine.ErrResultLimitReached)
}

func targetResultKey(ip string, port int) string {
	return fmt.Sprintf("%s|%d", ip, port)
}

// mergePipelineSpeedEvent 把流水线测速阶段的轻量 speed 事件补回完整的延迟结果。
//
// 启用速度规则时，第三步只能出现「同时满足延迟与速度规则」的最终结果：
// 延迟阶段的 result 事件不直接下发；测速达标后，再把完整元数据与速度合并为
// 一个 result 事件。补充测速任务仍沿用原来的 speed 事件，不受这里影响。
func mergePipelineSpeedEvent(ev engine.Event, latencyResults map[string]engine.Result) (engine.Event, bool) {
	if ev.Progress != nil {
		ev.Progress.Phase = "speed"
	}
	if ev.Type != engine.EventSpeed {
		return ev, true
	}
	if ev.Result == nil {
		return engine.Event{}, false
	}
	result, ok := latencyResults[targetResultKey(ev.Result.IP, ev.Result.Port)]
	if !ok {
		return engine.Event{}, false
	}
	result.DownloadSpeedKBs = ev.Result.DownloadSpeedKBs
	return engine.Event{Type: engine.EventResult, Result: &result}, true
}

// broadcastPartialResults 在用户手动停止流水线时保留已完成的延迟结果。
// 已经通过速度条件并下发过的结果不会重复发送；其余结果保留完整延迟元数据，
// DownloadSpeedKBs 维持 0，前端显示为“未测速”。
func (s *Server) broadcastPartialResults(taskID string, results []engine.Result, emitted map[string]struct{}) int {
	count := 0
	for i := range results {
		result := results[i]
		key := targetResultKey(result.IP, result.Port)
		if _, exists := emitted[key]; exists {
			continue
		}
		s.broadcastTaskEvent(taskID, engine.Event{Type: engine.EventResult, Result: &result})
		count++
	}
	return count
}

func (s *Server) broadcastTaskEvent(taskID string, ev engine.Event) {
	switch ev.Type {
	case engine.EventDone:
		log.Printf("任务 %s 结束: reason=%s message=%s", taskID, ev.Reason, ev.Message)
	case engine.EventError:
		log.Printf("任务 %s 错误: %s", taskID, ev.Message)
	}
	s.broadcast(ev)
}

func (s *Server) handleStartLatency(w http.ResponseWriter, r *http.Request) {
	var req latencyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "请求体不是合法 JSON: "+err.Error())
		return
	}

	targets := req.Targets
	if len(targets) == 0 && req.RawText != "" {
		targets = engine.ParseTargetsWithCIDR(req.RawText,
			engine.ParseSampleMode(req.SampleMode), req.SampleN)
	}
	if len(targets) == 0 {
		writeError(w, http.StatusBadRequest, "没有可测试的目标（IP 列表为空或格式无法识别）")
		return
	}

	taskID := fmt.Sprintf("lat-%d", time.Now().UnixNano())
	ctx, ok := s.tryStartTask(taskID)
	if !ok {
		writeError(w, http.StatusConflict, "已有任务正在运行，请先停止")
		return
	}

	opts := req.Options.apply(s.latencyDefaults)
	targets = engine.ResolveDefaultPorts(targets, opts.EnableTLS)
	log.Printf("任务 %s 开始: targets=%d tls=%v continueSpeed=%v", taskID, len(targets), opts.EnableTLS, req.EnableSpeed)
	go func() {
		defer s.finishTask(taskID)
		if !req.EnableSpeed {
			_, err := s.runner.RunLatencyTest(ctx, targets, opts, func(ev engine.Event) {
				s.broadcastTaskEvent(taskID, ev)
			})
			if isTaskFailure(err) {
				s.broadcastTaskEvent(taskID, engine.Event{Type: engine.EventError, Message: err.Error()})
			}
			return
		}

		// 速度规则启用时，统一的 maxResults 应作用于「同时满足延迟和速度」的结果，
		// 因此延迟阶段不能先按数量截断。
		opts.MaxResults = 0
		latencyCB := func(ev engine.Event) {
			if ev.Progress != nil {
				ev.Progress.Phase = "latency"
			}
			if ev.Type == engine.EventResult {
				// 这里只是中间结果；速度规则通过后再作为最终 result 下发。
				return
			}
			if ev.Type == engine.EventDone {
				// 阶段结束由调用方统一处理，确保停止时先补发部分结果再发 done。
				return
			}
			s.broadcastTaskEvent(taskID, ev)
		}
		results, err := s.runner.RunLatencyTest(ctx, targets, opts, latencyCB)
		if err != nil {
			if errors.Is(err, context.Canceled) {
				partial := s.broadcastPartialResults(taskID, results, nil)
				s.broadcastTaskEvent(taskID, engine.Event{Type: engine.EventDone, Reason: engine.DoneStopped,
					Message: fmt.Sprintf("已停止，保留 %d 个已完成延迟检测的结果", partial)})
				return
			}
			if isTaskFailure(err) {
				s.broadcastTaskEvent(taskID, engine.Event{Type: engine.EventError, Message: err.Error()})
			}
			return
		}
		if len(results) == 0 {
			s.broadcastTaskEvent(taskID, engine.Event{Type: engine.EventDone, Reason: engine.DoneCompleted, Message: "检测完成，没有符合延迟规则的 IP"})
			return
		}

		speedOpts := req.SpeedOptions.apply(s.speedDefaults)
		speedOpts.EnableTLS = opts.EnableTLS
		speedTargets := make([]engine.Target, 0, len(results))
		latencyByTarget := make(map[string]engine.Result, len(results))
		emitted := make(map[string]struct{}, len(results))
		var emittedMu sync.Mutex
		for _, result := range results {
			speedTargets = append(speedTargets, engine.Target{IP: result.IP, Port: result.Port})
			latencyByTarget[targetResultKey(result.IP, result.Port)] = result
		}
		speedCB := func(ev engine.Event) {
			if ev.Type == engine.EventDone && ev.Reason == engine.DoneStopped {
				// 先补发未完成测速的延迟结果，再统一发送最终 done。
				return
			}
			merged, ok := mergePipelineSpeedEvent(ev, latencyByTarget)
			if ok {
				if merged.Type == engine.EventResult && merged.Result != nil {
					emittedMu.Lock()
					emitted[targetResultKey(merged.Result.IP, merged.Result.Port)] = struct{}{}
					emittedMu.Unlock()
				}
				s.broadcastTaskEvent(taskID, merged)
			}
		}
		if err := s.runner.RunSpeedTest(ctx, speedTargets, speedOpts, speedCB); err != nil {
			if errors.Is(err, context.Canceled) {
				emittedMu.Lock()
				emittedCount := len(emitted)
				partial := s.broadcastPartialResults(taskID, results, emitted)
				emittedMu.Unlock()
				s.broadcastTaskEvent(taskID, engine.Event{Type: engine.EventDone, Reason: engine.DoneStopped,
					Message: fmt.Sprintf("已停止，保留 %d 个结果（%d 个已完成速度条件，%d 个仅完成延迟检测）", len(results), emittedCount, partial)})
				return
			}
			if isTaskFailure(err) {
				s.broadcastTaskEvent(taskID, engine.Event{Type: engine.EventError, Message: err.Error()})
			}
		}
	}()

	writeJSON(w, http.StatusOK, taskResponse{TaskID: taskID, Status: "running", TotalTargets: len(targets)})
}

func (s *Server) handleStartSpeed(w http.ResponseWriter, r *http.Request) {
	var req speedRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "请求体不是合法 JSON: "+err.Error())
		return
	}
	if len(req.Targets) == 0 {
		writeError(w, http.StatusBadRequest, "没有可测速的目标（请先在结果表格中选择）")
		return
	}

	taskID := fmt.Sprintf("spd-%d", time.Now().UnixNano())
	ctx, ok := s.tryStartTask(taskID)
	if !ok {
		writeError(w, http.StatusConflict, "已有任务正在运行，请先停止")
		return
	}

	opts := req.Options.apply(s.speedDefaults)
	req.Targets = engine.ResolveDefaultPorts(req.Targets, opts.EnableTLS)
	log.Printf("任务 %s 开始: targets=%d supplementalSpeed=true tls=%v", taskID, len(req.Targets), opts.EnableTLS)
	go func() {
		defer s.finishTask(taskID)
		if err := s.runner.RunSpeedTest(ctx, req.Targets, opts, func(ev engine.Event) {
			s.broadcastTaskEvent(taskID, ev)
		}); isTaskFailure(err) {
			s.broadcastTaskEvent(taskID, engine.Event{Type: engine.EventError, Message: err.Error()})
		}
	}()

	writeJSON(w, http.StatusOK, taskResponse{TaskID: taskID, Status: "running", TotalTargets: len(req.Targets)})
}

func (s *Server) handleStop(w http.ResponseWriter, r *http.Request) {
	var req stopRequest
	_ = json.NewDecoder(r.Body).Decode(&req) // 允许空 body = 停止当前任务
	stopped := s.stopTask(req.TaskID)
	if stopped {
		log.Printf("任务 %s 收到停止指令", req.TaskID)
	}
	writeJSON(w, http.StatusOK, map[string]bool{"stopped": stopped})
}

// handleEvents 是 SSE 事件流端点；前端用 EventSource 长连接订阅。
func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	flusher, err := sseHeaders(w)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	ch := s.subscribe()
	defer s.unsubscribe(ch)

	// 立即发送一个注释行，让浏览器触发 onopen
	fmt.Fprintf(w, ": connected\n\n")
	flusher.Flush()

	for {
		select {
		case <-r.Context().Done():
			return
		case ev := <-ch:
			if err := writeSSE(w, flusher, ev); err != nil {
				return
			}
		}
	}
}
