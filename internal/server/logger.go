package server

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
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
	dir      string
	path     string
	enabled  bool
	minLevel int
	mu       sync.Mutex
	lastErr  string
}

// levels 为日志级别从低到高的顺序，索引即级别阈值。
var levels = []string{"debug", "info", "warn", "error"}

func levelRank(name string) (int, bool) {
	name = strings.ToLower(strings.TrimSpace(name))
	for i, level := range levels {
		if level == name {
			return i, true
		}
	}
	return 0, false
}

func NewLogger(dataDir string, enabled bool) *Logger {
	l := &Logger{dir: filepath.Join(dataDir, LogDir), path: filepath.Join(dataDir, LogDir, LogFileName)}
	l.SetEnabled(enabled)
	return l
}

// SetEnabled 是日志总开关：关闭时停止写入并立即清空已有日志文件。
func (l *Logger) SetEnabled(on bool) {
	l.mu.Lock()
	l.enabled = on
	l.mu.Unlock()
	if !on {
		_ = l.Clear()
	}
}

func (l *Logger) Enabled() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.enabled
}

// SetMinLevel 设置写入阈值；无效或空值回落到 debug（全量）。
func (l *Logger) SetMinLevel(level string) {
	rank, ok := levelRank(level)
	if !ok {
		rank = 0 // debug
	}
	l.mu.Lock()
	l.minLevel = rank
	l.mu.Unlock()
}

// Level 返回当前级别过滤阈值（如 "info"）。
func (l *Logger) Level() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return levels[l.minLevel]
}

// Log writes one serialized, timestamped line when the master switch is on
// and the level passes the configured threshold.
func (l *Logger) Log(level, format string, args ...any) {
	rank, ok := levelRank(level)
	if !ok {
		rank = 1 // info
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.enabled || rank < l.minLevel {
		return
	}
	if err := os.MkdirAll(l.dir, 0755); err != nil {
		l.lastErr = err.Error()
		return
	}
	l.rotateIfNeededLocked()
	caller := ""
	if _, file, line, ok := runtime.Caller(1); ok {
		caller = fmt.Sprintf("%s:%d", filepath.Base(file), line)
	}
	line := fmt.Sprintf("%s [%s] %s %s\n", time.Now().Format(time.RFC3339Nano), levels[rank], caller, fmt.Sprintf(format, args...))
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
