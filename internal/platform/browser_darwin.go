//go:build darwin

package platform

import (
	"fmt"
	"os"
	"os/exec"
	"time"
)

// OpenBrowser 使用系统默认浏览器打开目标地址。
func OpenBrowser(url string) {
	time.Sleep(300 * time.Millisecond) // 等服务就绪
	if err := exec.Command("open", url).Start(); err != nil {
		fmt.Fprintf(os.Stderr, "无法自动打开浏览器，请手动访问 %s: %v\n", url, err)
	}
}
