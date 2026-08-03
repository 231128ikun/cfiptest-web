package server

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	LogDir      = "logs"
	LogFileName = "app.log"
	LogMaxBytes = 1 << 20
	LogKeep     = 3
)

type Logger struct {
	dir     string
	path    string
	enabled bool
	mu      sync.Mutex
	lastErr string
}

func NewLogger(dataDir string, enabled bool) *Logger {
	return &Logger{dir: filepath.Join(dataDir, LogDir), path: filepath.Join(dataDir, LogDir, LogFileName), enabled: enabled}
}

func (l *Logger) SetEnabled(on bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.enabled = on
}

func (l *Logger) Enabled() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.enabled
}

// Log writes one serialized, timestamped line when debug logging is enabled.
func (l *Logger) Log(level, format string, args ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.enabled {
		return
	}
	if err := os.MkdirAll(l.dir, 0755); err != nil {
		l.lastErr = err.Error()
		return
	}
	l.rotateIfNeededLocked()
	line := fmt.Sprintf("%s [%s] %s\n", time.Now().Format(time.RFC3339Nano), strings.ToLower(level), fmt.Sprintf(format, args...))
	f, err := os.OpenFile(l.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		l.lastErr = err.Error()
		return
	}
	_, writeErr := io.WriteString(f, line)
	closeErr := f.Close()
	if writeErr != nil {
		l.lastErr = writeErr.Error()
	} else if closeErr != nil {
		l.lastErr = closeErr.Error()
	} else {
		l.lastErr = ""
	}
}

func (l *Logger) LastError() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.lastErr
}

func (l *Logger) rotateIfNeededLocked() {
	st, err := os.Stat(l.path)
	if err != nil || st.Size() < LogMaxBytes {
		return
	}
	for i := LogKeep - 1; i >= 1; i-- {
		src := fmt.Sprintf("%s.%d", l.path, i)
		dst := fmt.Sprintf("%s.%d", l.path, i+1)
		_ = os.Remove(dst)
		if _, err := os.Stat(src); err == nil {
			_ = os.Rename(src, dst)
		}
	}
	_ = os.Remove(l.path + ".1")
	_ = os.Rename(l.path, l.path+".1")
}

// Tail includes rotated files so an incident spanning a rotation is inspectable.
func (l *Logger) Tail(n int) []string {
	if n <= 0 {
		n = 200
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	var body strings.Builder
	for i := LogKeep; i >= 1; i-- {
		if part, err := os.ReadFile(fmt.Sprintf("%s.%d", l.path, i)); err == nil {
			body.Write(part)
		}
	}
	if part, err := os.ReadFile(l.path); err == nil {
		body.Write(part)
	}
	if body.Len() == 0 {
		return []string{"(log is empty or debug logging is disabled)"}
	}
	lines := strings.Split(strings.TrimRight(body.String(), "\r\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return lines
}

func (l *Logger) Clear() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	var firstErr error
	for i := 0; i <= LogKeep; i++ {
		path := l.path
		if i > 0 {
			path = fmt.Sprintf("%s.%d", l.path, i)
		}
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}
