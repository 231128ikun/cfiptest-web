package server

import (
	"encoding/json"
	"fmt"
	"net/http"

	"iptest-web/internal/engine"
)

// sseHeaders 设置 SSE 响应头并确认客户端支持 Flusher。
func sseHeaders(w http.ResponseWriter) (http.Flusher, error) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		return nil, fmt.Errorf("响应不支持流式推送")
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	return flusher, nil
}

// writeSSE 将一条事件按 SSE 协议写入并立即刷新。
func writeSSE(w http.ResponseWriter, flusher http.Flusher, ev engine.Event) error {
	data, err := json.Marshal(ev)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", ev.Type, data)
	if err != nil {
		return err
	}
	flusher.Flush()
	return nil
}
