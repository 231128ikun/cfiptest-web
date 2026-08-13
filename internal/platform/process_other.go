//go:build !windows

package platform

import "errors"

// PIDOnPort 当前平台不通过系统命令查找监听进程。
func PIDOnPort(port int) int { return 0 }

// KillPID 当前平台不提供强制结束进程的能力。
func KillPID(pid int) error { return errors.New("process kill is not supported on this platform") }
