package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"iptest-web/internal/engine"
	"iptest-web/internal/library"
	"iptest-web/internal/subscription"
)

// ---- 订阅器定义 ----

// autoSubsResponse 对应 GET /api/auto/subs。
type autoSubsResponse struct {
	Subscriptions []subscription.Subscription `json:"subscriptions"`
}

func (s *Server) handleAutoSubsGet(w http.ResponseWriter, _ *http.Request) {
	subs, err := subscription.LoadSubscriptions(s.dataDir)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, autoSubsResponse{Subscriptions: subs})
}

// handleAutoSubsSave 全量保存订阅器定义。
func (s *Server) handleAutoSubsSave(w http.ResponseWriter, r *http.Request) {
	var req autoSubsResponse
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "请求体不是合法 JSON: "+err.Error())
		return
	}
	if err := subscription.SaveSubscriptions(s.dataDir, req.Subscriptions); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"saved": true})
}

// handleAutoSubsValidate 校验单个订阅器定义（不落盘）。
func (s *Server) handleAutoSubsValidate(w http.ResponseWriter, r *http.Request) {
	var sub subscription.Subscription
	if err := json.NewDecoder(r.Body).Decode(&sub); err != nil {
		writeError(w, http.StatusBadRequest, "请求体不是合法 JSON: "+err.Error())
		return
	}
	if err := sub.Validate(); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"valid": true})
}

// ---- IP 库 ----

// autoLibraryResponse 对应 GET /api/auto/library。
type autoLibraryResponse struct {
	Stats   library.Stats   `json:"stats"`
	Entries []library.Entry `json:"entries"`
	Offset  int             `json:"offset"`
	Limit   int             `json:"limit"`
	Total   int             `json:"total"`
}

func (s *Server) handleAutoLibraryGet(w http.ResponseWriter, r *http.Request) {
	lib, err := library.Open(s.dataDir)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	status := strings.TrimSpace(r.URL.Query().Get("status"))
	country := strings.ToUpper(strings.TrimSpace(r.URL.Query().Get("country")))
	q := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("q")))
	offset, limit := 0, 500
	if v := r.URL.Query().Get("offset"); v != "" {
		fmt.Sscanf(v, "%d", &offset)
	}
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := fmt.Sscanf(v, "%d", &limit); err == nil && n == 1 {
			if limit < 1 {
				limit = 1
			}
			if limit > 2000 {
				limit = 2000
			}
		}
	}

	all := lib.All()
	filtered := all[:0]
	for _, e := range all {
		if status != "" && e.Status != status {
			continue
		}
		if country != "" && e.CountryCode != country {
			continue
		}
		if q != "" {
			line := strings.ToLower(e.IP + "|" + e.Country + "|" + e.CityZh + "|" + e.DataCenter + "|" + e.ASNOrg)
			if !strings.Contains(line, q) {
				continue
			}
		}
		filtered = append(filtered, e)
	}
	total := len(filtered)
	if offset > total {
		offset = total
	}
	end := offset + limit
	if end > total {
		end = total
	}
	writeJSON(w, http.StatusOK, autoLibraryResponse{
		Stats:   lib.Stats(),
		Entries: filtered[offset:end],
		Offset:  offset,
		Limit:   limit,
		Total:   total,
	})
}

// autoImportRequest 对应 POST /api/auto/library/import。
type autoImportRequest struct {
	Targets    []engine.Target  `json:"targets"`    // 已解析目标（前端可直接用现有导入结果）
	Results    []engine.Result  `json:"results"`    // 检测结果（带元数据，导入后状态 active）
	Text       string           `json:"text"`       // 或原始文本（后端解析）
	SampleMode string           `json:"sampleMode"` // CIDR 抽样
	SampleN    int              `json:"sampleN"`
	Source     string           `json:"source"` // manual | import | official
}

// autoImportResponse 返回入库统计。
type autoImportResponse struct {
	Added   int `json:"added"`
	Updated int `json:"updated"`
	Total   int `json:"total"`
}

func (s *Server) handleAutoLibraryImport(w http.ResponseWriter, r *http.Request) {
	var req autoImportRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "请求体不是合法 JSON: "+err.Error())
		return
	}
	// 检测结果导入：直接带元数据入库（状态 active），覆盖式更新
	if len(req.Results) > 0 {
		lib, err := library.Open(s.dataDir)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		now := time.Now()
		added, updated := 0, 0
		for _, res := range req.Results {
			if res.IP == "" {
				continue
			}
			e := library.EntryFromResult(res, now)
			if existing, ok := lib.Get(res.IP, res.Port); ok {
				// 已存在：保留首见时间与来源，累计检测次数
				e.FirstSeenAt = existing.FirstSeenAt
				e.Source = existing.Source
				e.Checks = existing.Checks + 1
				updated++
			} else {
				added++
			}
			lib.Upsert(e)
		}
		if err := lib.Save(); err != nil {
			writeError(w, http.StatusInternalServerError, "保存 IP 库失败: "+err.Error())
			return
		}
		writeJSON(w, http.StatusOK, autoImportResponse{Added: added, Updated: updated, Total: lib.Len()})
		return
	}

	var targets []engine.Target
	if len(req.Targets) > 0 {
		targets = req.Targets
	} else if strings.TrimSpace(req.Text) != "" {
		var ok bool
		targets, ok = parseImportText(w, req.Text, req.SampleMode, req.SampleN)
		if !ok {
			return
		}
	} else {
		writeError(w, http.StatusBadRequest, "请提供 targets 或 text")
		return
	}
	source := req.Source
	if source != library.SourceManual && source != library.SourceOfficial {
		source = library.SourceImport
	}

	lib, err := library.Open(s.dataDir)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	now := time.Now()
	added, updated := 0, 0
	for _, t := range targets {
		e := library.Entry{
			IP: t.IP, Port: t.Port,
			Source: source, Status: library.StatusNew,
			FirstSeenAt: now,
		}
		if lib.Upsert(e) {
			added++
		} else {
			updated++
		}
	}
	if err := lib.Save(); err != nil {
		writeError(w, http.StatusInternalServerError, "保存 IP 库失败: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, autoImportResponse{Added: added, Updated: updated, Total: lib.Len()})
}

// autoLibraryRemoveRequest 对应 POST /api/auto/library/remove。
type autoLibraryRemoveRequest struct {
	Keys []string `json:"keys"`
}

func (s *Server) handleAutoLibraryRemove(w http.ResponseWriter, r *http.Request) {
	var req autoLibraryRemoveRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "请求体不是合法 JSON: "+err.Error())
		return
	}
	lib, err := library.Open(s.dataDir)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	removed := 0
	for _, key := range req.Keys {
		if key == "" {
			continue
		}
		if lib.RemoveKey(key) {
			removed++
		}
	}
	if removed > 0 {
		if err := lib.Save(); err != nil {
			writeError(w, http.StatusInternalServerError, "保存 IP 库失败: "+err.Error())
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]int{"removed": removed})
}

// handleAutoLibraryClear 清空 IP 库（需要显式 confirm）。
func (s *Server) handleAutoLibraryClear(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Confirm bool `json:"confirm"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "请求体不是合法 JSON: "+err.Error())
		return
	}
	if !req.Confirm {
		writeError(w, http.StatusBadRequest, "请确认清空（confirm=true）")
		return
	}
	lib, err := library.Open(s.dataDir)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	for _, e := range lib.All() {
		lib.RemoveKey(e.Key())
	}
	if err := lib.Save(); err != nil {
		writeError(w, http.StatusInternalServerError, "保存 IP 库失败: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"cleared": true})
}

// ---- 自动化运行 ----

// autoRunRequest 对应 POST /api/auto/run。
type autoRunRequest struct {
	SubscriptionName string                    `json:"subscriptionName"`
	Options          *subscription.RunOptions  `json:"options,omitempty"`
}

func (s *Server) handleAutoRun(w http.ResponseWriter, r *http.Request) {
	var req autoRunRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "请求体不是合法 JSON: "+err.Error())
		return
	}
	subs, err := subscription.LoadSubscriptions(s.dataDir)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	var sub *subscription.Subscription
	for i := range subs {
		if subs[i].Name == req.SubscriptionName {
			sub = &subs[i]
			break
		}
	}
	if sub == nil {
		writeError(w, http.StatusNotFound, "找不到订阅器: "+req.SubscriptionName)
		return
	}
	taskID := "auto:" + sub.Name
	ctx, ok := s.tryStartTask(taskID)
	if !ok {
		writeError(w, http.StatusConflict, "已有任务在运行")
		return
	}

	// 运行参数默认沿用本地配置
	s.configMu.RLock()
	latencyDefaults := s.latencyDefaults
	speedDefaults := s.speedDefaults
	s.configMu.RUnlock()
	opts := subscription.RunOptions{
		LatencyTimeoutMs: latencyDefaults.TimeoutMs,
		SpeedDurationSec: speedDefaults.DurationSec,
		SpeedConcurrency: speedDefaults.MaxConcurrency,
		DownloadURL:      speedDefaults.DownloadURL,
	}
	if req.Options != nil {
		merged := *req.Options
		if merged.LatencyTimeoutMs == 0 {
			merged.LatencyTimeoutMs = opts.LatencyTimeoutMs
		}
		if merged.SpeedDurationSec == 0 {
			merged.SpeedDurationSec = opts.SpeedDurationSec
		}
		if merged.SpeedConcurrency == 0 {
			merged.SpeedConcurrency = opts.SpeedConcurrency
		}
		if merged.DownloadURL == "" {
			merged.DownloadURL = opts.DownloadURL
		}
		opts = merged
	}

	go s.runAutoTask(ctx, taskID, *sub, opts)

	writeJSON(w, http.StatusOK, taskResponse{TaskID: taskID, Status: "running", TotalTargets: 0})
}

// runAutoTask 在后台执行一次订阅维护，并通过 SSE 广播进度。
func (s *Server) runAutoTask(ctx context.Context, taskID string, sub subscription.Subscription, opts subscription.RunOptions) {
	defer s.finishTask(taskID)
	emit := func(p subscription.Progress) error {
		body, _ := json.Marshal(p)
		s.broadcast(engine.Event{Type: engine.EventAuto, Message: string(body)})
		return ctx.Err()
	}

	lib, err := library.Open(s.dataDir)
	if err != nil {
		s.broadcast(engine.Event{Type: engine.EventError, Message: "打开 IP 库失败: " + err.Error()})
		return
	}
	report, err := subscription.Run(ctx, s.runner, lib, sub, opts, emit)
	if err != nil {
		if ctx.Err() != nil {
			s.broadcast(engine.Event{Type: engine.EventDone, Reason: engine.DoneStopped, Message: "自动化已停止"})
			return
		}
		s.broadcast(engine.Event{Type: engine.EventDone, Reason: engine.DoneCompleted,
			Message: "自动化运行出错: " + err.Error()})
		return
	}
	summary := fmt.Sprintf("自动化完成：%d 条输出 → %s", report.TotalLines, report.OutputPath)
	if len(report.Shortages) > 0 {
		summary += "；" + strings.Join(report.Shortages, "；")
	}
	reportJSON, _ := json.Marshal(report)
	s.broadcast(engine.Event{Type: engine.EventDone, Reason: engine.DoneCompleted, Message: summary})
	// 完整报告随 done 事件附带，前端可展示明细
	s.broadcast(engine.Event{Type: engine.EventAuto, Message: `{"stage":"report","report":` + string(reportJSON) + `}`})
}

// handleAutoOutput 下载订阅输出文件；path 必须解析到 dataDir 内。
func (s *Server) handleAutoOutput(w http.ResponseWriter, r *http.Request) {
	rel := strings.TrimSpace(r.URL.Query().Get("path"))
	if rel == "" {
		writeError(w, http.StatusBadRequest, "缺少 path 参数")
		return
	}
	abs := filepath.Join(s.dataDir, filepath.FromSlash(rel))
	base := filepath.Clean(s.dataDir)
	target := filepath.Clean(abs)
	relTo, err := filepath.Rel(base, target)
	if err != nil || strings.HasPrefix(relTo, "..") || filepath.IsAbs(relTo) {
		writeError(w, http.StatusBadRequest, "path 必须位于 data 目录内")
		return
	}
	body, err := os.ReadFile(target)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			writeError(w, http.StatusNotFound, "输出文件不存在（可能尚未运行）")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	name := filepath.Base(target)
	ct := "text/plain; charset=utf-8"
	if strings.HasSuffix(strings.ToLower(name), ".csv") {
		ct = "text/csv; charset=utf-8"
	}
	w.Header().Set("Content-Type", ct)
	w.Header().Set("Content-Disposition", "attachment; filename="+name)
	w.Write(body)
}
