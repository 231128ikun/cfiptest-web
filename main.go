// iptest-web：Cloudflare IP 测速本地 Web 应用。
//
// 双击启动后在浏览器中使用；配置、设置和运行数据统一存放在 exe 同级 data 目录。
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

// version 是当前版本号（形如 2026.07.31-22.06），随每次代码修改更新为当前时间。
// build.bat / build.sh 直接读取此值命名产物，不再另行生成。
var version = "2026.08.05-16.26"

func main() {
	port := flag.Int("port", 18080, "HTTP 服务监听端口")
	noBrowser := flag.Bool("no-browser", false, "启动后不自动打开浏览器")
	flag.Parse()

	executableDir := exeDir()
	dataDir, err := config.PrepareDataDir(executableDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "创建数据目录失败: %v\n", err)
		os.Exit(1)
	}
	sub, err := fs.Sub(webFS, "web")
	if err != nil {
		fmt.Fprintf(os.Stderr, "加载内嵌页面失败: %v\n", err)
		os.Exit(1)
	}
	cfg := config.Load(dataDir)

	fmt.Println("启动中：地理位置与 ASN 数据库若有缺失将在后台下载，不影响使用...")
	runner, err := engine.NewRunner(engine.RunnerConfig{
		DataDir:         dataDir,
		LocationSources: cfg.Sources.Locations,
		ASNSources:      cfg.Sources.ASNDatabase,
		TraceURL:        cfg.TraceURL,
		IPSTypeURL:      cfg.IPSTypeURL,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "初始化失败: %v\n", err)
		os.Exit(1)
	}
	defer runner.Close()
	fmt.Printf("资源状态：地理位置 %d 条，ASN 数据库%s\n",
		runner.LocationCount(), map[bool]string{true: "已加载", false: "后台加载中"}[runner.ASNLoaded()])

	srv := server.New(runner, sub, version, cfg, dataDir)

	addr, ln, err := listenOnPort(*port)
	if err != nil {
		fmt.Fprintf(os.Stderr, "端口 %d-%d 均无法使用: %v\n", *port, *port+20, err)
		fmt.Fprintln(os.Stderr, "请关闭已有 iptest-web 实例，或使用 -port 指定其他端口。")
		os.Exit(1)
	}
	httpServer := &http.Server{
		Addr:              addr,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		IdleTimeout:       60 * time.Second,
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

	// Ctrl+C、系统终止和 Windows 控制台关闭按钮统一走优雅退出。
	shutdownCh := make(chan struct{}, 1)
	shutdownDone := make(chan struct{})
	uninstallConsoleHandler := installConsoleCloseHandler(shutdownCh, shutdownDone)
	defer uninstallConsoleHandler()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(sigCh)
	go func() {
		<-sigCh
		select {
		case shutdownCh <- struct{}{}:
		default:
		}
	}()
	<-shutdownCh

	fmt.Println("\n正在退出...")
	srv.CancelActive()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = httpServer.Shutdown(ctx)
	close(shutdownDone)
}

// exeDir 返回可执行文件所在目录；获取失败时退回当前工作目录。
func exeDir() string {
	exe, err := os.Executable()
	if err != nil {
		return "."
	}
	return filepath.Dir(exe)
}

// listenOnPort 优先使用用户指定端口；默认端口被旧实例占用时顺延尝试一小段范围，
// 避免双击启动因旧窗口未关闭而立即退出。调用方会使用实际监听地址打开浏览器。
func listenOnPort(port int) (string, net.Listener, error) {
	var lastErr error
	for candidate := port; candidate <= port+20; candidate++ {
		addr := fmt.Sprintf("127.0.0.1:%d", candidate)
		ln, err := net.Listen("tcp", addr)
		if err == nil {
			return addr, ln, nil
		}
		lastErr = err
	}
	return "", nil, lastErr
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
	if err := cmd.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "无法自动打开浏览器，请手动访问 %s: %v\n", url, err)
	}
}
