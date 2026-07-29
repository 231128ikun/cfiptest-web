// iptest-web：Cloudflare IP 测速本地 Web 应用。
//
// 双击启动后在浏览器中使用；数据文件（locations.json、GeoLite2-ASN.mmdb）
// 存放在 exe 同目录，首次启动时自动下载。
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"syscall"
	"time"

	"iptest-web/internal/config"
	"iptest-web/internal/engine"
	"iptest-web/internal/server"

	"embed"
)

//go:embed web
var webFS embed.FS

// version 是当前版本号，取构建时刻（形如 2026.07.29-01.21）。
// 由 build.bat / build.sh 通过 -ldflags "-X main.version=..." 注入；
// 直接 go build 时保留下面的占位值。
var version = "dev"

func main() {
	port := flag.Int("port", 18080, "HTTP 服务监听端口")
	noBrowser := flag.Bool("no-browser", false, "启动后不自动打开浏览器")
	flag.Parse()

	dataDir := exeDir()
	cfg := config.Load(dataDir)

	fmt.Println("正在加载地理位置与 ASN 数据库（首次运行需下载）...")
	runner, err := engine.NewRunner(engine.RunnerConfig{
		DataDir:         dataDir,
		LocationSources: cfg.Sources.Locations,
		ASNSources:      cfg.Sources.ASNDatabase,
		TraceURL:        cfg.TraceURL,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "初始化失败: %v\n", err)
		os.Exit(1)
	}
	defer runner.Close()
	fmt.Printf("资源就绪：地理位置 %d 条，ASN 数据库%s\n",
		runner.LocationCount(), map[bool]string{true: "已加载", false: "未加载"}[runner.ASNLoaded()])

	sub, err := fs.Sub(webFS, "web")
	if err != nil {
		fmt.Fprintf(os.Stderr, "加载内嵌页面失败: %v\n", err)
		os.Exit(1)
	}
	srv := server.New(runner, sub, version, cfg)

	addr := fmt.Sprintf("127.0.0.1:%d", *port)
	httpServer := &http.Server{Addr: addr, Handler: srv.Handler()}

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "端口 %d 被占用: %v\n（可用 -port 指定其他端口）\n", *port, err)
		os.Exit(1)
	}

	url := fmt.Sprintf("http://%s", addr)
	fmt.Printf("服务已启动: %s （按 Ctrl+C 退出）\n", url)
	if !*noBrowser {
		go openBrowser(url)
	}

	go func() {
		if err := httpServer.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			fmt.Fprintf(os.Stderr, "HTTP 服务异常: %v\n", err)
			os.Exit(1)
		}
	}()

	// 优雅退出
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh

	fmt.Println("\n正在退出...")
	srv.CancelActive()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = httpServer.Shutdown(ctx)
}

// exeDir 返回可执行文件所在目录；获取失败时退回当前工作目录。
func exeDir() string {
	exe, err := os.Executable()
	if err != nil {
		return "."
	}
	return filepath.Dir(exe)
}

// openBrowser 按平台调起默认浏览器。
func openBrowser(url string) {
	time.Sleep(300 * time.Millisecond) // 等服务就绪
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	case "darwin":
		cmd = exec.Command("open", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	_ = cmd.Start()
}
