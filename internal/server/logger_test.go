package server

import (
	"os"
	"testing"
)

func TestLoggerLevelFilter(t *testing.T) {
	dir := t.TempDir()
	l := NewLogger(dir, true)
	l.SetMinLevel("error")
	l.Log("info", "noise")
	l.Log("warn", "careful")
	l.Log("error", "boom")
	if got := len(l.Tail(10)); got != 1 {
		t.Fatalf("error 阈值下应只写 1 行，实际 %d: %v", got, l.Tail(10))
	}
	l.SetMinLevel("debug")
	l.Log("info", "hello")
	if got := len(l.Tail(10)); got != 2 {
		t.Fatalf("debug 全量下应写 2 行，实际 %d", got)
	}
}

func TestLoggerDisableClears(t *testing.T) {
	dir := t.TempDir()
	l := NewLogger(dir, true)
	l.Log("info", "keep me")
	if _, err := os.Stat(l.path); err != nil {
		t.Fatalf("开启时应有日志文件: %v", err)
	}
	l.SetEnabled(false)
	if _, err := os.Stat(l.path); !os.IsNotExist(err) {
		t.Fatalf("关闭总开关后日志文件应被删除: %v", err)
	}
	if l.Enabled() {
		t.Fatal("总开关应已关闭")
	}
}

func TestLoggerNewDisabledClearsLeftovers(t *testing.T) {
	dir := t.TempDir()
	first := NewLogger(dir, true)
	first.Log("info", "leftover")
	if _, err := os.Stat(first.path); err != nil {
		t.Fatalf("先写入旧日志: %v", err)
	}
	second := NewLogger(dir, false)
	if _, err := os.Stat(second.path); !os.IsNotExist(err) {
		t.Fatalf("关闭状态启动应清理残留日志: %v", err)
	}
}
