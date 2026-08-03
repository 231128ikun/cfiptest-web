package server

import (
	"bufio"
	"fmt"
	"net"
	"net/http"
	"runtime/debug"
	"strings"
	"time"
)

type responseRecorder struct {
	http.ResponseWriter
	status int
	bytes  int
}

func (w *responseRecorder) WriteHeader(status int) {
	if w.status != 0 {
		return
	}
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}

func (w *responseRecorder) Write(body []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	n, err := w.ResponseWriter.Write(body)
	w.bytes += n
	return n, err
}

func (w *responseRecorder) Flush() {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	if flusher, ok := w.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

func (w *responseRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	hijacker, ok := w.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, fmt.Errorf("response writer does not support hijacking")
	}
	return hijacker.Hijack()
}

func (w *responseRecorder) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (s *Server) observeHTTP(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rw := &responseRecorder{ResponseWriter: w}
		defer func() {
			if recovered := recover(); recovered != nil {
				s.log.Log("error", "http panic method=%s path=%q remote=%q panic=%v stack=%s", r.Method, r.URL.RequestURI(), r.RemoteAddr, recovered, strings.TrimSpace(string(debug.Stack())))
				if rw.status == 0 {
					writeError(rw, http.StatusInternalServerError, "internal server error")
				}
			}
			status := rw.status
			if status == 0 {
				status = http.StatusOK
			}
			s.log.Log("debug", "http request method=%s path=%q status=%d bytes=%d duration=%s remote=%q userAgent=%q", r.Method, r.URL.RequestURI(), status, rw.bytes, time.Since(start).Round(time.Millisecond), r.RemoteAddr, r.UserAgent())
		}()
		next.ServeHTTP(rw, r)
	})
}
