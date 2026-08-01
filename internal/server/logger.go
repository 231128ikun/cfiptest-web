package server

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// 调试日志配置：data/logs/app.log，按大小轮转。
const (
	LogDir      = "logs"
	LogFileName = "app.log"
	LogMaxBytes = 1 << 20 // 单文件 1MB 触发轮转
	LogKeep     = 3       // 保留 app.log + .1 + .2
)

// Logger 是极简调试日志：默认关闭（用户开启后才落盘），用于排查问题。
type Logger struct {
	dir     string
	path    string
	enabled bool
	mu      sync.Mutex
}

// NewLogger 创建日志器。
func NewLogger(dataDir string, enabled bool) *Logger {
	return &Logger{
		dir:     filepath.Join(dataDir, LogDir),
		path:    filepath.Join(dataDir, LogDir, LogFileName),
		enabled: enabled,
	}
}

// SetEnabled 开关调试日志（默认关闭）。
func (l *Logger) SetEnabled(on bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.enabled = on
}

// Enabled 返回当前是否开启。
func (l *Logger) Enabled() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.enabled
}

// Log 写一条日志；开关关闭时不落盘（符合「默认关闭，出问题再打开」）。
func (l *Logger) Log(level, format string, args ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.enabled {
		return
	}
	_ = os.MkdirAll(l.dir, 0755)
	l.rotateIfNeededLocked()
	line := fmt.Sprintf("%s [%s] %s\n", time.Now().Format("2006-01-02 15:04:05.000"), level, fmt.Sprintf(format, args...))
	f, err := os.OpenFile(l.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return
	}
	_, _ = f.WriteString(line)
	_ = f.Close()
}

// rotateIfNeededLocked 按大小轮转：app.log → app.log.1 → app.log.2，保留 LogKeep 份。
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
	_ = os.Rename(l.path, l.path+".1")
}

// Tail 返回主日志文件末尾最多 n 行。
func (l *Logger) Tail(n int) []string {
	if n <= 0 {
		n = 200
	}
	body, err := os.ReadFile(l.path)
	if err != nil {
		return []string{"（日志为空，或调试日志未开启）"}
	}
	lines := strings.Split(strings.TrimRight(string(body), "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return lines
}

// Clear 清空日志文件。
func (l *Logger) Clear() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return os.Remove(l.path)
}
