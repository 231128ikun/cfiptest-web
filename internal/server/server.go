// Package server 提供 iptest-web 的 HTTP 接口：静态页面、任务管理与 SSE 事件推送。
package server

import (
	"context"
	"encoding/json"
	"io/fs"
	"net/http"
	"sync"

	"iptest-web/internal/engine"
)

// Server 是应用层 HTTP 服务器。
// 单机单用户模型：任意时刻只允许一个活跃任务；SSE 订阅者共享同一条事件总线。
type Server struct {
	runner  *engine.Runner
	assets  fs.FS
	mux     *http.ServeMux
	version string

	// 任务生命周期
	mu         sync.Mutex
	taskID     string
	taskCancel context.CancelFunc

	// SSE 订阅者
	sseMu     sync.Mutex
	sseClients map[chan engine.Event]struct{}
}

// New 创建 Server 并注册全部路由。assets 为嵌入的前端静态资源。
func New(runner *engine.Runner, assets fs.FS, version string) *Server {
	s := &Server{
		runner:     runner,
		assets:     assets,
		mux:        http.NewServeMux(),
		version:    version,
		sseClients: make(map[chan engine.Event]struct{}),
	}
	s.registerRoutes()
	return s
}

// Handler 返回根 http.Handler。
func (s *Server) Handler() http.Handler { return s.mux }

func (s *Server) registerRoutes() {
	s.mux.Handle("/", http.FileServer(http.FS(s.assets)))
	s.mux.HandleFunc("GET /api/config", s.handleConfig)
	s.mux.HandleFunc("POST /api/task/latency", s.handleStartLatency)
	s.mux.HandleFunc("POST /api/task/speed", s.handleStartSpeed)
	s.mux.HandleFunc("POST /api/task/stop", s.handleStop)
	s.mux.HandleFunc("GET /api/task/events", s.handleEvents)
}

// broadcast 将事件推送给所有 SSE 订阅者（engine 回调入口）。
func (s *Server) broadcast(ev engine.Event) {
	s.sseMu.Lock()
	defer s.sseMu.Unlock()
	for ch := range s.sseClients {
		select {
		case ch <- ev:
		default: // 订阅者消费不及时则丢弃，避免阻塞测试协程
		}
	}
}

func (s *Server) subscribe() chan engine.Event {
	ch := make(chan engine.Event, 256)
	s.sseMu.Lock()
	s.sseClients[ch] = struct{}{}
	s.sseMu.Unlock()
	return ch
}

func (s *Server) unsubscribe(ch chan engine.Event) {
	s.sseMu.Lock()
	delete(s.sseClients, ch)
	s.sseMu.Unlock()
}

// tryStartTask 尝试占用任务槽；成功返回新的可取消 ctx，失败返回 false（409 场景）。
func (s *Server) tryStartTask(id string) (context.Context, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.taskCancel != nil {
		return nil, false
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.taskID = id
	s.taskCancel = cancel
	return ctx, true
}

// finishTask 释放任务槽。
func (s *Server) finishTask(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.taskID == id {
		s.taskID = ""
		s.taskCancel = nil
	}
}

// stopTask 取消指定任务；返回是否命中了活跃任务。
func (s *Server) stopTask(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.taskCancel == nil || (id != "" && s.taskID != id) {
		return false
	}
	s.taskCancel()
	return true
}

// CancelActive 在进程退出前取消进行中的任务。
func (s *Server) CancelActive() { s.stopTask("") }

// writeJSON 以 JSON 响应；失败时写 500。
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
