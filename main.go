// iptest-web：Cloudflare IP 测速本地 Web 应用。
//
// 共享服务启动逻辑位于 internal/app，平台相关启动行为位于
// internal/platform；本文件只负责解析命令行参数并转发给共享入口。
package main

import (
	"flag"
	"os"

	"iptest-web/internal/app"

	"embed"
)

//go:embed web
var webFS embed.FS

// version 是当前版本号（形如 2026.07.31-22.06），随每次代码修改更新为当前时间。
// build.bat / build.sh 直接读取此值命名产物，不再另行生成。
var version = "2026.08.13-14.07"

func main() {
	port := flag.Int("port", 18080, "HTTP 服务监听端口")
	noBrowser := flag.Bool("no-browser", false, "启动后不自动打开浏览器")
	dataDirFlag := flag.String("data-dir", "", "数据目录（留空时使用程序同级 data）")
	strictPort := flag.Bool("strict-port", false, "仅监听指定端口，不自动顺延")
	flag.Parse()

	if err := app.Run(app.Options{
		Port:       *port,
		StrictPort: *strictPort,
		NoBrowser:  *noBrowser,
		DataDir:    *dataDirFlag,
		Version:    version,
		WebFS:      webFS,
	}); err != nil {
		os.Exit(1)
	}
}
