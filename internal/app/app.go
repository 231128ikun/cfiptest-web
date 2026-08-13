// Package app 提供平台无关的本地服务启动、端口管理和生命周期控制。
package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"iptest-web/internal/config"
	"iptest-web/internal/engine"
	"iptest-web/internal/platform"
	"iptest-web/internal/server"
)

// Options 描述启动本地 HTTP 服务所需的配置；平台差异由 internal/platform 处理。
type Options struct {
	Port       int
	StrictPort bool
	NoBrowser  bool
	DataDir    string
	Version    string
	WebFS      fs.FS
}

// Run 启动本地服务并阻塞到收到退出信号或网页关闭请求。
func Run(opts Options) error {
	platform.InitDNS()

	// 若已有 iptest-web 实例占用端口：先自动停止旧实例（先请求网页停止接口优雅退出，
	// 超时后 Windows 上按端口找到进程并强制结束），再由本实例接管端口。
	existing := ""
	if opts.StrictPort {
		if isIptestWeb(opts.Port) {
			existing = fmt.Sprintf("http://127.0.0.1:%d/", opts.Port)
		}
	} else {
		existing = findExistingInstance(opts.Port)
	}
	if existing != "" {
		fmt.Printf("检测到旧实例：%s，正在停止…\n", existing)
		if stopExistingInstance(existing) {
			fmt.Println("旧实例已停止，本实例继续启动。")
		} else {
			fmt.Println("未能停止旧实例，将打开其页面，请在其中点击「停止服务」后再试。")
			if !opts.NoBrowser {
				platform.OpenBrowser(existing)
			}
			return nil
		}
	}

	executableDir := exeDir()
	var dataDir string
	var err error
	if strings.TrimSpace(opts.DataDir) == "" {
		dataDir, err = config.PrepareDataDir(executableDir)
	} else {
		dataDir = filepath.Clean(opts.DataDir)
		err = config.PrepareDataDirAt(dataDir)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "创建数据目录失败: %v\n", err)
		return err
	}
	sub, err := fs.Sub(opts.WebFS, "web")
	if err != nil {
		fmt.Fprintf(os.Stderr, "加载内嵌页面失败: %v\n", err)
		return err
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
		return err
	}
	defer runner.Close()
	fmt.Printf("资源状态：地理位置 %d 条，ASN 数据库%s\n",
		runner.LocationCount(), map[bool]string{true: "已加载", false: "后台加载中"}[runner.ASNLoaded()])

	shutdownCh := make(chan struct{}, 1)
	srv := server.New(runner, sub, opts.Version, cfg, dataDir, runtime.GOOS)
	srv.SetShutdownHandler(func() {
		select {
		case shutdownCh <- struct{}{}:
		default:
		}
	})

	listen := listenOnPort
	if opts.StrictPort {
		listen = listenOnExactPort
	}
	addr, ln, existingURL, err := listen(opts.Port)
	if existingURL != "" {
		if !stopExistingInstance(existingURL) {
			handleExistingInstance(existingURL, opts.NoBrowser)
			return nil
		}
		addr, ln, existingURL, err = listen(opts.Port)
	}
	if err != nil {
		if opts.StrictPort {
			fmt.Fprintf(os.Stderr, "端口 %d 无法使用: %v\n", opts.Port, err)
		} else {
			fmt.Fprintf(os.Stderr, "端口 %d-%d 均无法使用: %v\n", opts.Port, opts.Port+20, err)
		}
		fmt.Fprintln(os.Stderr, "请关闭已有 iptest-web 实例，或使用 -port 指定其他端口。")
		return err
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
	if !opts.NoBrowser {
		go platform.OpenBrowser(url)
	}

	go func() {
		if err := httpServer.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			fmt.Fprintf(os.Stderr, "HTTP 服务异常: %v\n", err)
			os.Exit(1)
		}
	}()

	// Ctrl+C / 系统终止统一走优雅退出。库文件采用「临时文件 + 重命名」原子写入，
	// 因此进程即使被强制结束也不会损坏数据（最多丢失最后一次未保存的更新）。
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
	return nil
}

// exeDir 返回可执行文件所在目录；获取失败时退回当前工作目录。
func exeDir() string {
	exe, err := os.Executable()
	if err != nil {
		return "."
	}
	return filepath.Dir(exe)
}

// listenOnPort 优先使用用户指定端口；若端口被其他程序占用则顺延尝试一小段范围。
// 若占用端口的是 iptest-web 自身（旧实例），返回其首页地址供调用方停止并重试。
func listenOnPort(port int) (string, net.Listener, string, error) {
	var lastErr error
	for candidate := port; candidate <= port+20; candidate++ {
		addr := fmt.Sprintf("127.0.0.1:%d", candidate)
		ln, err := net.Listen("tcp", addr)
		if err == nil {
			return addr, ln, "", nil
		}
		lastErr = err
		if isIptestWeb(candidate) {
			return "", nil, fmt.Sprintf("http://127.0.0.1:%d/", candidate), nil
		}
	}
	return "", nil, "", lastErr
}

// listenOnExactPort 仅监听指定端口，供固定访问地址的 Android WebView 使用。
func listenOnExactPort(port int) (string, net.Listener, string, error) {
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	ln, err := net.Listen("tcp", addr)
	if err == nil {
		return addr, ln, "", nil
	}
	if isIptestWeb(port) {
		return "", nil, fmt.Sprintf("http://127.0.0.1:%d/", port), nil
	}
	return "", nil, "", err
}

// findExistingInstance 探测端口范围内是否已有 iptest-web 实例，返回其首页地址（未找到返回空串）。
func findExistingInstance(port int) string {
	for candidate := port; candidate <= port+20; candidate++ {
		if isIptestWeb(candidate) {
			return fmt.Sprintf("http://127.0.0.1:%d/", candidate)
		}
	}
	return ""
}

// handleExistingInstance 处理监听完成后仍发现旧实例的竞态：再尝试停止一次，
// 仍失败则打开其页面并提示；返回 true 表示应退出本次启动。
func handleExistingInstance(url string, noBrowser bool) bool {
	if url == "" {
		return false
	}
	fmt.Printf("检测到 iptest-web 正在运行：%s\n", url)
	if stopExistingInstance(url) {
		fmt.Println("旧实例已停止。")
		return false
	}
	fmt.Println("未能停止旧实例，请在其页面点击「停止服务」后重试。")
	if !noBrowser {
		platform.OpenBrowser(url)
	}
	return true
}

// stopExistingInstance 尝试停止旧实例并等待其端口释放：
// 先请求 /api/shutdown 优雅退出（等待数秒），仍占用时在 Windows 上按端口结束进程。
// 返回端口是否已释放。
func stopExistingInstance(homeURL string) bool {
	u, err := url.Parse(homeURL)
	if err != nil {
		return false
	}
	_, portStr, err := net.SplitHostPort(u.Host)
	if err != nil {
		return false
	}
	port, err := strconv.Atoi(portStr)
	if err != nil || port <= 0 {
		return false
	}

	shutdownURL := "http://" + u.Host + "/api/shutdown"
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Post(shutdownURL, "application/json", nil)
	if err == nil {
		resp.Body.Close()
		fmt.Printf("已请求旧实例优雅退出：%s\n", shutdownURL)
	} else {
		fmt.Printf("旧实例不支持网页停止接口，稍后将尝试按端口结束进程（%v）\n", err)
	}

	// 等待优雅退出释放端口。
	waitFor := func(d time.Duration) bool {
		deadline := time.Now().Add(d)
		for time.Now().Before(deadline) {
			if portFree(port) {
				return true
			}
			time.Sleep(200 * time.Millisecond)
		}
		return false
	}
	if waitFor(4 * time.Second) {
		return true
	}

	// Windows 兜底：找到占用端口的进程并强制结束；其余平台 PIDOnPort 返回 0。
	if pid := platform.PIDOnPort(port); pid > 0 {
		fmt.Printf("旧实例未在时限内退出，正在结束进程 PID %d…\n", pid)
		if err := platform.KillPID(pid); err != nil {
			fmt.Printf("结束进程失败：%v\n", err)
		}
		return waitFor(2 * time.Second)
	}
	return false
}

// portFree 判断 127.0.0.1 上的端口当前是否可被监听（可监听即视为已释放）。
func portFree(port int) bool {
	if port <= 0 {
		return false
	}
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return false
	}
	ln.Close()
	return true
}

// isIptestWeb 通过 /api/config 返回的版本号格式判断指定端口是否已在运行 iptest-web。
func isIptestWeb(port int) bool {
	url := fmt.Sprintf("http://127.0.0.1:%d/api/config", port)
	client := &http.Client{Timeout: 800 * time.Millisecond}
	resp, err := client.Get(url)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return false
	}
	var info struct {
		Version string `json:"version"`
	}
	if json.NewDecoder(resp.Body).Decode(&info) != nil {
		return false
	}
	return versionRe.MatchString(info.Version)
}

// versionRe 匹配 iptest-web 版本号格式（yyyy.MM.dd-HH.mm）。
var versionRe = regexp.MustCompile(`^\d{4}\.\d{2}\.\d{2}-\d{2}\.\d{2}$`)
