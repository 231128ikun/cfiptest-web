package server

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"iptest-web/internal/engine"
)

// importRemoteRequest 对应 POST /api/import/remote。
type importRemoteRequest struct {
	URL string `json:"url"`

	// 远程文本里的 CIDR 网段如何抽样，语义同 latencyRequest。
	SampleMode string `json:"sampleMode"`
	SampleN    int    `json:"sampleN"`
}

// importResponse 返回解析后的目标，而非原始文本：
// 解析规则（多格式识别、CIDR 展开、去重）后端已有一份，
// 让前端再实现一遍只会两边走样。
type importResponse struct {
	Targets []engine.Target `json:"targets"`
	Bytes   int             `json:"bytes"`
	Source  string          `json:"source"`
	Format  string          `json:"format,omitempty"` // text | csv
	Text    string          `json:"text,omitempty"`   // 远程导入保留原文，供前端按备注/端口筛选
}

const (
	// 导入的 TXT 一般几十 KB；给到 8 MB 足够，同时防止把整个进程内存拖垮。
	maxImportBytes = 8 << 20
	importTimeout  = 20 * time.Second

	// maxExpandedTargets 限制一次解析能返回给前端的目标数。
	//
	// 官方段「全取」是 152 万个地址，序列化成 JSON 再堆进浏览器内存有上百 MB，
	// 页面会直接卡死。与其假装支持，不如明确拒绝并告诉用户换抽样粒度。
	maxExpandedTargets = 200000
)

// importTextRequest 对应 POST /api/import/text。
type importTextRequest struct {
	Text       string `json:"text"`
	SampleMode string `json:"sampleMode"`
	SampleN    int    `json:"sampleN"`
}

// parseImportText 解析 IP 文本并展开其中的网段，附带规模上限检查。
// 两个导入端点（粘贴文本 / 远程 TXT）共用，避免上限与措辞两处漂移。
func parseImportText(w http.ResponseWriter, text, sampleMode string, sampleN int) ([]engine.Target, bool) {
	mode := engine.ParseSampleMode(sampleMode)

	// 先估算规模再展开，避免为了报错而先吃掉几百 MB 内存
	if cidrs := engine.CollectCIDRs(text); len(cidrs) > 0 {
		if total, _ := engine.CountCIDRs(cidrs, mode, sampleN); total > maxExpandedTargets {
			writeError(w, http.StatusRequestEntityTooLarge, fmt.Sprintf(
				"按当前抽样粒度需展开 %d 个 IP，超过 %d 的上限；请改用更粗的抽样粒度（如每 /24 取 1 个）",
				total, maxExpandedTargets))
			return nil, false
		}
	}

	targets := engine.ParseTargetsWithCIDR(text, mode, sampleN)
	if len(targets) == 0 {
		writeError(w, http.StatusUnprocessableEntity,
			fmt.Sprintf("已读到 %d 字节，但其中没有可识别的 IP", len(text)))
		return nil, false
	}
	return targets, true
}

// handleImportText 解析任意 IP 文本（含 CIDR 网段）并返回展开后的目标。
//
// 前端的 lineToTarget 解析单个 IP 目标，不展开网段；把 CIDR 展开与抽样放在后端，
// 是为了让「粘贴里混写网段」与「官方优选」共用 engine 里那一份已测过的算法，
// 而不是在 JS 里再实现一遍抽样。
func (s *Server) handleImportText(w http.ResponseWriter, r *http.Request) {
	var req importTextRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if strings.TrimSpace(req.Text) == "" {
		writeError(w, http.StatusBadRequest, "内容为空")
		return
	}

	targets, ok := parseImportText(w, req.Text, req.SampleMode, req.SampleN)
	if !ok {
		return
	}

	writeJSON(w, http.StatusOK, importResponse{
		Targets: targets,
		Bytes:   len(req.Text),
		Source:  "text",
	})
}

// normalizeRemoteImportURL 统一远程导入地址行为：缺少协议时补 https，且只允许 HTTP(S)。
func normalizeRemoteImportURL(raw string) (string, error) {
	target := strings.TrimSpace(raw)
	if target == "" {
		return "", fmt.Errorf("请填写要导入的地址")
	}
	if !strings.Contains(target, "://") {
		target = "https://" + target
	}
	parsed, err := url.Parse(target)
	if err != nil {
		return "", fmt.Errorf("地址格式不正确: %w", err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", fmt.Errorf("只支持 http/https 地址")
	}
	if parsed.Host == "" {
		return "", fmt.Errorf("地址缺少主机名")
	}
	return parsed.String(), nil
}

// handleImportRemote 代前端抓取远程 IP 列表。
//
// 存在的唯一理由是绕开浏览器 CORS：绝大多数订阅地址不会给
// Access-Control-Allow-Origin，前端 fetch 直接被拦。由 Go 来取则无此限制。
func (s *Server) handleImportRemote(w http.ResponseWriter, r *http.Request) {
	var req importRemoteRequest
	if !decodeJSON(w, r, &req) {
		return
	}

	target, err := normalizeRemoteImportURL(req.URL)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	body, contentType, err := fetchTextFile(target)
	if err != nil {
		// 内网拦截是「请求不该这么填」，不是「上游挂了」，用 400 而非 502；
		// 且 ErrBlockedAddr 会被 net.OpError / url.Error 层层包裹，
		// 直接 err.Error() 会把一堆 dial 细节丢到前端 toast 里。
		if errors.Is(err, engine.ErrBlockedAddr) {
			writeError(w, http.StatusBadRequest,
				"该地址解析到内网或保留地址，已拒绝访问；导入源应是公网上的 IP 列表")
			return
		}
		writeError(w, http.StatusBadGateway, "导入失败: "+err.Error())
		return
	}

	format := detectRemoteFormat(target, contentType, body)
	if format == "csv" {
		writeJSON(w, http.StatusOK, importResponse{
			Bytes:  len(body),
			Source: target,
			Format: format,
			Text:   body,
		})
		return
	}

	targets, ok := parseImportText(w, body, req.SampleMode, req.SampleN)
	if !ok {
		return
	}

	writeJSON(w, http.StatusOK, importResponse{
		Targets: targets,
		Bytes:   len(body),
		Source:  target,
		Format:  format,
		Text:    body,
	})
}

// autoInputUploadRequest 将浏览器选择的本地文件持久化到 data/inputs，供定时任务重复读取。
type autoInputUploadRequest struct {
	Name string `json:"name"`
	Text string `json:"text"`
}

type autoInputUploadResponse struct {
	Path    string `json:"path"`
	Name    string `json:"name"`
	Bytes   int    `json:"bytes"`
	Targets int    `json:"targets"`
}

func sanitizeInputFileName(name string) string {
	name = filepath.Base(strings.TrimSpace(name))
	name = strings.Map(func(r rune) rune {
		if r < 32 || strings.ContainsRune(`<>:"/\|?*`, r) {
			return '_'
		}
		return r
	}, name)
	name = strings.Trim(name, " .")
	if name == "" {
		name = "targets.txt"
	}
	if ext := strings.ToLower(filepath.Ext(name)); ext != ".txt" && ext != ".csv" {
		name += ".txt"
	}
	return name
}

func (s *Server) handleAutoInputUpload(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxImportBytes+(1<<20))
	var req autoInputUploadRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if len(req.Text) == 0 {
		writeError(w, http.StatusBadRequest, "导入文件为空")
		return
	}
	if len(req.Text) > maxImportBytes {
		writeError(w, http.StatusRequestEntityTooLarge, fmt.Sprintf("文件超过 %d MB 上限", maxImportBytes>>20))
		return
	}
	targets := engine.ParseTargetsWithCIDR(req.Text, engine.SampleOnePerSubnet, 1)
	if len(targets) == 0 {
		writeError(w, http.StatusUnprocessableEntity, "文件中没有可识别的 IP 或 CIDR")
		return
	}
	dir := filepath.Join(s.dataDir, "inputs")
	if err := os.MkdirAll(dir, 0755); err != nil {
		writeError(w, http.StatusInternalServerError, "创建输入目录失败: "+err.Error())
		return
	}
	cleanName := sanitizeInputFileName(req.Name)
	tmp, err := os.CreateTemp(dir, ".auto-input-*.tmp")
	if err != nil {
		writeError(w, http.StatusInternalServerError, "创建输入临时文件失败: "+err.Error())
		return
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err := tmp.Chmod(0644); err != nil {
		_ = tmp.Close()
		writeError(w, http.StatusInternalServerError, "设置输入文件权限失败: "+err.Error())
		return
	}
	if _, err := tmp.WriteString(req.Text); err != nil {
		_ = tmp.Close()
		writeError(w, http.StatusInternalServerError, "写入输入文件失败: "+err.Error())
		return
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		writeError(w, http.StatusInternalServerError, "刷新输入文件失败: "+err.Error())
		return
	}
	if err := tmp.Close(); err != nil {
		writeError(w, http.StatusInternalServerError, "关闭输入文件失败: "+err.Error())
		return
	}
	// 临时文件写完整后再原子改名，避免定时任务读到半截内容；随机后缀也避免同毫秒上传覆盖。
	randomPart := strings.TrimSuffix(strings.TrimPrefix(filepath.Base(tmpPath), ".auto-input-"), ".tmp")
	name := time.Now().Format("20060102-150405.000") + "-" + randomPart + "-" + cleanName
	abs := filepath.Join(dir, name)
	if err := os.Rename(tmpPath, abs); err != nil {
		writeError(w, http.StatusInternalServerError, "保存输入文件失败: "+err.Error())
		return
	}
	rel := filepath.ToSlash(filepath.Join("inputs", name))
	writeJSON(w, http.StatusOK, autoInputUploadResponse{Path: rel, Name: cleanName, Bytes: len(req.Text), Targets: len(targets)})
}

// fetchTextFile 取回远程文本，限制大小与耗时，并拒绝解析到内网的目标。
func fetchTextFile(rawURL string) (string, string, error) {
	client := &http.Client{
		Timeout: importTimeout,
		// 自定义 DialContext 与 engine 数据下载共用同一套内网拦截；换掉 Transport 时别丢了它。
		Transport: &http.Transport{
			DialContext:         engine.SafeDialContext,
			TLSHandshakeTimeout: 10 * time.Second,
		},
		// 重定向同样受 safeDialContext 约束，这里只额外限制跳数，
		// 避免 302 链把 20 秒超时耗在无意义的跳转上。
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return fmt.Errorf("重定向次数过多")
			}
			if req.URL.Scheme != "http" && req.URL.Scheme != "https" {
				return fmt.Errorf("重定向到不支持的协议 %s", req.URL.Scheme)
			}
			return nil
		},
	}

	req, err := http.NewRequest(http.MethodGet, rawURL, nil)
	if err != nil {
		return "", "", err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0")

	resp, err := client.Do(req)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", "", fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	// 多读 1 字节用于判断是否被截断，避免把半行 IP 当成完整内容
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxImportBytes+1))
	if err != nil {
		return "", "", err
	}
	if len(data) > maxImportBytes {
		return "", "", fmt.Errorf("文件超过 %d MB 上限", maxImportBytes>>20)
	}
	return string(data), resp.Header.Get("Content-Type"), nil
}

func detectRemoteFormat(rawURL, contentType, body string) string {
	lowerType := strings.ToLower(contentType)
	if strings.Contains(lowerType, "text/csv") || strings.Contains(lowerType, "application/csv") {
		return "csv"
	}
	if parsed, err := url.Parse(rawURL); err == nil && strings.HasSuffix(strings.ToLower(parsed.Path), ".csv") {
		return "csv"
	}
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(strings.TrimPrefix(line, "\ufeff"))
		if line == "" {
			continue
		}
		lower := strings.ToLower(line)
		hasIP := strings.Contains(lower, "ip") || strings.Contains(line, "IP地址")
		hasPort := strings.Contains(lower, "port") || strings.Contains(line, "端口")
		if strings.Contains(line, ",") && hasIP && hasPort {
			return "csv"
		}
		break
	}
	return "text"
}
