// Package server 提供 iptest-web 的 HTTP 接口：静态页面、任务管理与 SSE 事件推送。
package server

import (
	"context"
	"encoding/json"
	"io/fs"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"iptest-web/internal/config"
	"iptest-web/internal/engine"
	"iptest-web/internal/library"
	"iptest-web/internal/subscription"
)

// Server 是应用层 HTTP 服务器。
// 单机单用户模型：任意时刻只允许一个活跃任务；SSE 订阅者共享同一条事件总线。
type Server struct {
	runner      *engine.Runner
	assets      fs.FS
	mux         *http.ServeMux
	version     string
	cfg         config.Config
	dataDir     string
	configMu    sync.RWMutex
	rangesMu    sync.Mutex
	rangesCache *officialRangesResponse

	// 参数默认值：内置默认值叠加本地 data/config.json 的覆盖结果。
	// 前端 /api/config 读它做表单初值，请求里缺省的字段也回落到它。
	latencyDefaults engine.LatencyOptions
	speedDefaults   engine.SpeedOptions

	// IP 库管理器（惰性初始化）
	libMgr   *library.Manager
	libMgrMu sync.Mutex

	// 调试日志（默认关闭，设置页开关）
	log *Logger

	// 任务生命周期
	mu         sync.Mutex
	taskID     string
	taskCancel context.CancelFunc

	// 定时维护：按分钟检查标准 5 段 Cron，记录本进程已触发的分钟，避免重复执行。
	schedulerStop chan struct{}
	scheduleMu    sync.Mutex
	scheduleRuns  map[string]string

	// SSE 订阅者
	sseMu      sync.Mutex
	sseClients map[chan engine.Event]struct{}
}

// New 创建 Server 并注册全部路由。assets 为嵌入的前端静态资源。
func New(runner *engine.Runner, assets fs.FS, version string, cfg config.Config, dataDir string) *Server {
	speedDefaults := engine.DefaultSpeedOptions()
	if cfg.SpeedTestURL != "" {
		speedDefaults.DownloadURL = cfg.SpeedTestURL
	}

	logEnabled := false
	if v, ok := config.LoadSettings(dataDir)["debugLog"].(bool); ok {
		logEnabled = v
	}
	s := &Server{
		runner:          runner,
		assets:          assets,
		mux:             http.NewServeMux(),
		version:         version,
		cfg:             cfg,
		dataDir:         dataDir,
		latencyDefaults: engine.DefaultLatencyOptions(),
		speedDefaults:   speedDefaults,
		sseClients:      make(map[chan engine.Event]struct{}),
		schedulerStop:   make(chan struct{}),
		scheduleRuns:    make(map[string]string),
		log:             NewLogger(dataDir, logEnabled),
	}
	s.registerRoutes()
	go s.runScheduler()
	return s
}

// Handler 返回根 http.Handler。
func (s *Server) Handler() http.Handler {
	return s.observeHTTP(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !isLocalHost(r.Host) {
			s.log.Log("warn", "reject non-local Host: method=%s host=%q remote=%q", r.Method, r.Host, r.RemoteAddr)
			writeError(w, http.StatusForbidden, "仅允许通过本机地址访问")
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		s.mux.ServeHTTP(w, r)
	}))
}
func isLocalHost(hostport string) bool {
	host := hostport
	if parsedHost, _, err := net.SplitHostPort(hostport); err == nil {
		host = parsedHost
	}
	host = strings.Trim(strings.ToLower(host), "[]")
	return host == "localhost" || host == "127.0.0.1" || host == "::1"
}

func (s *Server) registerRoutes() {
	s.mux.Handle("/", http.FileServer(http.FS(s.assets)))
	s.mux.HandleFunc("GET /api/config", s.handleConfig)
	s.mux.HandleFunc("PUT /api/config", s.handleSaveConfig)
	s.mux.HandleFunc("PUT /api/settings", s.handleSaveSettings)
	s.mux.HandleFunc("GET /api/official-ranges", s.handleOfficialRanges)
	s.mux.HandleFunc("POST /api/import/remote", s.handleImportRemote)
	s.mux.HandleFunc("POST /api/import/text", s.handleImportText)
	s.mux.HandleFunc("POST /api/auto/input/upload", s.handleAutoInputUpload)
	s.mux.HandleFunc("POST /api/task/latency", s.handleStartLatency)
	s.mux.HandleFunc("POST /api/task/speed", s.handleStartSpeed)
	s.mux.HandleFunc("POST /api/task/stop", s.handleStop)
	s.mux.HandleFunc("GET /api/task/status", s.handleTaskStatus)
	s.mux.HandleFunc("GET /api/task/events", s.handleEvents)

	// 自动化：维护任务 / IP 库 / 运行 / 历史
	s.mux.HandleFunc("GET /api/auto/tasks", s.handleTasksGet)
	s.mux.HandleFunc("PUT /api/auto/tasks", s.handleTasksSave)
	s.mux.HandleFunc("POST /api/auto/tasks/validate", s.handleTaskValidate)
	s.mux.HandleFunc("GET /api/auto/libraries", s.handleLibrariesGet)
	s.mux.HandleFunc("POST /api/auto/libraries", s.handleLibrariesCreate)
	s.mux.HandleFunc("POST /api/auto/libraries/rename", s.handleLibrariesRename)
	s.mux.HandleFunc("POST /api/auto/libraries/delete", s.handleLibrariesDelete)
	s.mux.HandleFunc("POST /api/auto/libraries/clear", s.handleLibrariesClear)
	s.mux.HandleFunc("GET /api/auto/library", s.handleLibraryGet)
	s.mux.HandleFunc("POST /api/auto/library/import", s.handleLibraryImport)
	s.mux.HandleFunc("POST /api/auto/library/remove", s.handleLibraryRemove)
	s.mux.HandleFunc("POST /api/auto/run", s.handleAutoRun)
	s.mux.HandleFunc("GET /api/auto/output", s.handleAutoOutput)
	s.mux.HandleFunc("GET /api/log", s.handleLogGet)
	s.mux.HandleFunc("POST /api/log/clear", s.handleLogClear)
	// 旧订阅器路径别名（兼容旧页面）
	s.mux.HandleFunc("GET /api/auto/subs", s.handleTasksGet)
	s.mux.HandleFunc("PUT /api/auto/subs", s.handleTasksSave)
	s.mux.HandleFunc("POST /api/auto/subs/validate", s.handleTaskValidate)
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

// CancelActive 在进程退出前停止定时器并取消进行中的任务。
func (s *Server) CancelActive() {
	select {
	case <-s.schedulerStop:
	default:
		close(s.schedulerStop)
	}
	s.stopTask("")
}

func (s *Server) runScheduler() {
	// 启动后立即检查一次，之后对齐到下一分钟，避免固定从启动秒数漂移。
	timer := time.NewTimer(200 * time.Millisecond)
	defer timer.Stop()
	for {
		select {
		case <-s.schedulerStop:
			return
		case now := <-timer.C:
			s.checkScheduledTasks(now)
			delay := time.Until(time.Now().Truncate(time.Minute).Add(time.Minute))
			if delay < time.Second {
				delay = time.Minute
			}
			timer.Reset(delay)
		}
	}
}

func (s *Server) checkScheduledTasks(now time.Time) {
	tasks, err := subscription.LoadTasks(s.dataDir)
	if err != nil {
		s.log.Log("error", "读取定时维护任务失败: %v", err)
		return
	}
	minuteKey := now.Format("2006-01-02 15:04")
	for _, task := range tasks {
		if !task.Enabled || !task.Schedule.Enabled || !subscription.CronMatches(task.Schedule.Cron, now) {
			continue
		}
		key := task.ID
		if key == "" {
			key = task.Name
		}
		s.scheduleMu.Lock()
		already := s.scheduleRuns[key] == minuteKey
		if !already {
			s.scheduleRuns[key] = minuteKey
		}
		s.scheduleMu.Unlock()
		if already {
			continue
		}
		taskID := "auto:" + task.Name
		ctx, ok := s.tryStartTask(taskID)
		if !ok {
			s.log.Log("warn", "定时维护 %s 已到期，但当前有其他任务运行，本次跳过", task.Name)
			continue
		}
		opts := s.autoRunOptions(&task, nil)
		s.log.Log("info", "Cron 定时触发维护任务: %s (%s)", task.Name, task.Schedule.Cron)
		go s.runAutoTask(ctx, taskID, task, opts)
		break
	}
}

// writeJSON 以 JSON 响应；失败时写 500。
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
