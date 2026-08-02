package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"iptest-web/internal/engine"
	"iptest-web/internal/library"
	"iptest-web/internal/subscription"
)

// libraryManager 惰性打开库管理器（多库 data/ipdb/）。
func (s *Server) libraryManager() (*library.Manager, error) {
	s.libMgrMu.Lock()
	defer s.libMgrMu.Unlock()
	if s.libMgr == nil {
		m, err := library.OpenManager(s.dataDir)
		if err != nil {
			return nil, err
		}
		s.libMgr = m
	}
	return s.libMgr, nil
}

// ---- 维护任务 ----

func (s *Server) handleTasksGet(w http.ResponseWriter, _ *http.Request) {
	tasks, err := subscription.LoadTasks(s.dataDir)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"tasks": tasks})
}

func (s *Server) handleTasksSave(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Tasks []subscription.Task `json:"tasks"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "请求体不是合法 JSON: "+err.Error())
		return
	}
	if err := subscription.SaveTasks(s.dataDir, req.Tasks); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"saved": true})
}

func (s *Server) handleTaskValidate(w http.ResponseWriter, r *http.Request) {
	var task subscription.Task
	if err := json.NewDecoder(r.Body).Decode(&task); err != nil {
		writeError(w, http.StatusBadRequest, "请求体不是合法 JSON: "+err.Error())
		return
	}
	if err := task.Validate(); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"valid": true})
}

// ---- IP 库管理 ----

func (s *Server) handleLibrariesGet(w http.ResponseWriter, _ *http.Request) {
	mgr, err := s.libraryManager()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	list, stats, err := mgr.ListWithStats()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"libraries": list, "stats": stats})
}

func (s *Server) handleLibrariesCreate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "请求体不是合法 JSON: "+err.Error())
		return
	}
	mgr, err := s.libraryManager()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	info, err := mgr.Create(req.Name)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"library": info})
}

func (s *Server) handleLibrariesRename(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "请求体不是合法 JSON: "+err.Error())
		return
	}
	mgr, err := s.libraryManager()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := mgr.Rename(req.ID, req.Name); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"renamed": true})
}

func (s *Server) handleLibrariesDelete(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "请求体不是合法 JSON: "+err.Error())
		return
	}
	mgr, err := s.libraryManager()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := mgr.Delete(req.ID); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"deleted": true})
}

func (s *Server) handleLibrariesClear(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID      string `json:"id"`
		Confirm bool   `json:"confirm"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "请求体不是合法 JSON: "+err.Error())
		return
	}
	if !req.Confirm {
		writeError(w, http.StatusBadRequest, "请确认清空（confirm=true）")
		return
	}
	mgr, err := s.libraryManager()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := mgr.Clear(req.ID); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"cleared": true})
}

// ---- 库内容 ----

func (s *Server) libraryFor(w http.ResponseWriter, r *http.Request) (*library.Store, *library.Manager, bool) {
	mgr, err := s.libraryManager()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return nil, nil, false
	}
	id := strings.TrimSpace(r.URL.Query().Get("lib"))
	if id == "" {
		id = library.DefaultID
	}
	lib, err := mgr.Open(id)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return nil, nil, false
	}
	return lib, mgr, true
}

type autoLibraryResponse struct {
	Lib     string         `json:"lib"`
	Stats   library.Stats  `json:"stats"`
	Entries []library.Entry `json:"entries"`
	Offset  int            `json:"offset"`
	Limit   int            `json:"limit"`
	Total   int            `json:"total"`
}

func (s *Server) handleLibraryGet(w http.ResponseWriter, r *http.Request) {
	lib, _, ok := s.libraryFor(w, r)
	if !ok {
		return
	}
	status := strings.TrimSpace(r.URL.Query().Get("status"))
	country := strings.ToUpper(strings.TrimSpace(r.URL.Query().Get("country")))
	city := strings.TrimSpace(r.URL.Query().Get("city"))
	dc := strings.ToUpper(strings.TrimSpace(r.URL.Query().Get("dc")))
	asn := strings.TrimSpace(r.URL.Query().Get("asn"))
	port := strings.TrimSpace(r.URL.Query().Get("port"))
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
		if city != "" && e.CityZh != city {
			continue
		}
		if dc != "" && !strings.EqualFold(e.DataCenter, dc) {
			continue
		}
		if asn != "" && strconv.FormatUint(uint64(e.ASN), 10) != asn {
			continue
		}
		if port != "" && strconv.Itoa(e.Port) != port {
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
		Lib:     libID(r),
		Stats:   lib.Stats(),
		Entries: filtered[offset:end],
		Offset:  offset,
		Limit:   limit,
		Total:   total,
	})
}

func libID(r *http.Request) string {
	id := strings.TrimSpace(r.URL.Query().Get("lib"))
	if id == "" {
		return library.DefaultID
	}
	return id
}

type autoImportRequest struct {
	Lib        string          `json:"lib,omitempty"`
	Targets    []engine.Target `json:"targets"`
	Results    []engine.Result `json:"results"`
	Text       string          `json:"text"`
	SampleMode string          `json:"sampleMode"`
	SampleN    int             `json:"sampleN"`
	Source     string          `json:"source"`
}

type autoImportResponse struct {
	Added   int `json:"added"`
	Updated int `json:"updated"`
	Total   int `json:"total"`
}

func (s *Server) handleLibraryImport(w http.ResponseWriter, r *http.Request) {
	var req autoImportRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "请求体不是合法 JSON: "+err.Error())
		return
	}
	mgr, err := s.libraryManager()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	id := req.Lib
	if id == "" {
		id = library.DefaultID
	}
	lib, err := mgr.Open(id)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}

	// 检测结果导入：带元数据，状态 active
	if len(req.Results) > 0 {
		now := time.Now()
		added, updated := 0, 0
		for _, res := range req.Results {
			if res.IP == "" {
				continue
			}
			e := library.EntryFromResult(res, now)
			if existing, ok := lib.Get(res.IP, res.Port); ok {
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
		writeError(w, http.StatusBadRequest, "请提供 results、targets 或 text")
		return
	}
	source := req.Source
	if source != library.SourceManual && source != library.SourceOfficial {
		source = library.SourceImport
	}
	now := time.Now()
	added, updated := 0, 0
	for _, t := range targets {
		e := library.Entry{IP: t.IP, Port: t.Port, Source: source, Status: library.StatusNew, FirstSeenAt: now}
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

func (s *Server) handleLibraryRemove(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Lib  string   `json:"lib"`
		Keys []string `json:"keys"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "请求体不是合法 JSON: "+err.Error())
		return
	}
	mgr, err := s.libraryManager()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	id := req.Lib
	if id == "" {
		id = library.DefaultID
	}
	lib, err := mgr.Open(id)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
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

// ---- 运行 ----

type autoRunRequest struct {
	TaskID  string                   `json:"taskId"`
	Options *subscription.RunOptions `json:"options,omitempty"`
}

func (s *Server) handleAutoRun(w http.ResponseWriter, r *http.Request) {
	var req autoRunRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "请求体不是合法 JSON: "+err.Error())
		return
	}
	tasks, err := subscription.LoadTasks(s.dataDir)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	var task *subscription.Task
	for i := range tasks {
		if tasks[i].ID == req.TaskID || tasks[i].Name == req.TaskID {
			task = &tasks[i]
			break
		}
	}
	if task == nil {
		writeError(w, http.StatusNotFound, "找不到维护任务: "+req.TaskID)
		return
	}
	taskID := "auto:" + task.Name
	ctx, ok := s.tryStartTask(taskID)
	if !ok {
		writeError(w, http.StatusConflict, "已有任务在运行")
		return
	}
	opts := s.autoRunOptions(req.Options)
	go s.runAutoTask(ctx, taskID, *task, opts)
	writeJSON(w, http.StatusOK, taskResponse{TaskID: taskID, Status: "running", TotalTargets: 0})
}

func (s *Server) autoRunOptions(overrides *subscription.RunOptions) subscription.RunOptions {
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
	if overrides == nil {
		return opts
	}
	merged := *overrides
	if merged.LatencyTimeoutMs == 0 { merged.LatencyTimeoutMs = opts.LatencyTimeoutMs }
	if merged.SpeedDurationSec == 0 { merged.SpeedDurationSec = opts.SpeedDurationSec }
	if merged.SpeedConcurrency == 0 { merged.SpeedConcurrency = opts.SpeedConcurrency }
	if merged.DownloadURL == "" { merged.DownloadURL = opts.DownloadURL }
	return merged
}

func (s *Server) runAutoTask(ctx context.Context, taskID string, task subscription.Task, opts subscription.RunOptions) {
	defer s.finishTask(taskID)
	emit := func(p subscription.Progress) error {
		body, _ := json.Marshal(p)
		s.broadcast(engine.Event{Type: engine.EventAuto, Message: string(body)})
		return ctx.Err()
	}
	s.log.Log("info", "维护任务开始: %s (taskId=%s, 库=%s)", task.Name, task.ID, task.LibraryID)

	mgr, err := s.libraryManager()
	if err != nil {
		s.broadcast(engine.Event{Type: engine.EventError, Message: "打开 IP 库失败: " + err.Error()})
		s.log.Log("error", "维护任务 %s 打开 IP 库失败: %v", task.Name, err)
		return
	}
	lib, err := mgr.Open(task.LibraryID)
	if err != nil {
		s.broadcast(engine.Event{Type: engine.EventError, Message: "打开 IP 库失败: " + err.Error()})
		s.log.Log("error", "维护任务 %s 打开 IP 库失败: %v", task.Name, err)
		return
	}
	report, err := subscription.RunTask(ctx, s.runner, lib, task, opts, emit)
	if err != nil {
		if ctx.Err() != nil {
			s.broadcast(engine.Event{Type: engine.EventDone, Reason: engine.DoneStopped, Message: "自动化已停止"})
			s.log.Log("warn", "维护任务 %s 已停止", task.Name)
			return
		}
		s.broadcast(engine.Event{Type: engine.EventDone, Reason: engine.DoneCompleted, Message: "自动化运行出错: " + err.Error()})
		s.log.Log("error", "维护任务 %s 出错: %v", task.Name, err)
		return
	}
	summary := fmt.Sprintf("自动化完成：%d 条输出 → %s", report.TotalLines, report.OutputPath)
	if len(report.Shortages) > 0 {
		summary += "；" + strings.Join(report.Shortages, "；")
	}
	reportJSON, _ := json.Marshal(report)
	s.broadcast(engine.Event{Type: engine.EventDone, Reason: engine.DoneCompleted, Message: summary})
	s.broadcast(engine.Event{Type: engine.EventAuto, Message: `{"stage":"report","report":` + string(reportJSON) + `}`})
	s.log.Log("info", "维护任务 %s 完成: %s", task.Name, summary)
}


// ---- 调试日志 ----

func (s *Server) handleLogGet(w http.ResponseWriter, r *http.Request) {
	lines := 200
	if v := r.URL.Query().Get("lines"); v != "" {
		fmt.Sscanf(v, "%d", &lines)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"enabled": s.log.Enabled(),
		"path":    filepath.Join(LogDir, LogFileName),
		"lines":   s.log.Tail(lines),
	})
}

func (s *Server) handleLogClear(w http.ResponseWriter, _ *http.Request) {
	if err := s.log.Clear(); err != nil && !errors.Is(err, os.ErrNotExist) {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"cleared": true})
}

// ---- 输出下载 ----

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

var _ = sync.Mutex{}
