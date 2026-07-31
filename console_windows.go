//go:build windows

package main

import (
	"sync"
	"syscall"
	"time"
)

const (
	ctrlCloseEvent    = 2
	ctrlLogoffEvent   = 5
	ctrlShutdownEvent = 6
)

// installConsoleCloseHandler 捕获 Windows 控制台关闭、注销与关机事件。
// 回调会等待主流程完成收尾，但最长只等待 4 秒，避免阻塞系统关闭。
func installConsoleCloseHandler(shutdown chan<- struct{}, done <-chan struct{}) func() {
	kernel32 := syscall.NewLazyDLL("kernel32.dll")
	setConsoleCtrlHandler := kernel32.NewProc("SetConsoleCtrlHandler")

	callback := syscall.NewCallback(func(event uint32) uintptr {
		if event != ctrlCloseEvent && event != ctrlLogoffEvent && event != ctrlShutdownEvent {
			return 0
		}
		select {
		case shutdown <- struct{}{}:
		default:
		}
		select {
		case <-done:
		case <-time.After(4 * time.Second):
		}
		return 1
	})

	registered, _, _ := setConsoleCtrlHandler.Call(callback, 1)
	var once sync.Once
	return func() {
		once.Do(func() {
			if registered != 0 {
				setConsoleCtrlHandler.Call(callback, 0)
			}
		})
	}
}
