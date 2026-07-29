package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"iptest-web/internal/engine"
)

// latencyOptionsDTO 延迟测试参数（指针字段区分"未提供"与"显式零值"）。
// 未提供的字段回落到 engine.DefaultLatencyOptions()。
type latencyOptionsDTO struct {
	MaxConcurrency *int  `json:"maxConcurrency"`
	TimeoutMs      *int  `json:"timeoutMs"`
	MaxLatencyMs   *int  `json:"maxLatencyMs"`
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

func (s *Server) handleStartLatency(w http.ResponseWriter, r *http.Request) {
	var req latencyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "请求体不是合法 JSON: "+err.Error())
		return
	}

	targets := req.Targets
	if len(targets) == 0 && req.RawText != "" {
		targets = engine.ParseTargets(req.RawText)
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
	go func() {
		defer s.finishTask(taskID)
		_, err := s.runner.RunLatencyTest(ctx, targets, opts, s.broadcast)
		if err != nil && !errors.Is(err, context.Canceled) {
			s.broadcast(engine.Event{Type: engine.EventError, Message: err.Error()})
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
	go func() {
		defer s.finishTask(taskID)
		if err := s.runner.RunSpeedTest(ctx, req.Targets, opts, s.broadcast); err != nil && !errors.Is(err, context.Canceled) {
			s.broadcast(engine.Event{Type: engine.EventError, Message: err.Error()})
		}
	}()

	writeJSON(w, http.StatusOK, taskResponse{TaskID: taskID, Status: "running", TotalTargets: len(req.Targets)})
}

func (s *Server) handleStop(w http.ResponseWriter, r *http.Request) {
	var req stopRequest
	_ = json.NewDecoder(r.Body).Decode(&req) // 允许空 body = 停止当前任务
	writeJSON(w, http.StatusOK, map[string]bool{"stopped": s.stopTask(req.TaskID)})
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
