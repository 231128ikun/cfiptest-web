package server

import (
	"net/http"
	"time"
)

// handleShutdown 停止服务并退出进程。先写回响应，再延迟触发关闭，避免当前请求被中断。
func (s *Server) handleShutdown(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	fn := s.shutdownHandler
	if fn == nil {
		return
	}
	go func() {
		time.Sleep(200 * time.Millisecond)
		fn()
	}()
}
