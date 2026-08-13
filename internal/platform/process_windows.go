//go:build windows

package platform

import (
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

// PIDOnPort 返回监听 127.0.0.1:port 的进程 PID，未找到返回 0。
func PIDOnPort(port int) int {
	if port <= 0 {
		return 0
	}
	out, err := exec.Command("netstat", "-ano", "-p", "tcp").Output()
	if err != nil {
		return 0
	}
	prefix := fmt.Sprintf("127.0.0.1:%d", port)
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 5 && strings.EqualFold(fields[0], "TCP") &&
			fields[1] == prefix && strings.EqualFold(fields[3], "LISTENING") {
			pid, err := strconv.Atoi(fields[4])
			if err == nil {
				return pid
			}
		}
	}
	return 0
}

// KillPID 强制结束指定进程。
func KillPID(pid int) error {
	_, err := exec.Command("taskkill", "/F", "/PID", strconv.Itoa(pid)).CombinedOutput()
	return err
}
