package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"iptest-web/internal/config"
	"iptest-web/internal/engine"
	"iptest-web/internal/library"
	"iptest-web/internal/subscription"
)

func autoServer(t *testing.T) *Server {
	t.Helper()
	dir := t.TempDir()
	return &Server{dataDir: dir, log: NewLogger(dir, false)}
}

func doJSON(t *testing.T, handler http.HandlerFunc, method, target string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var reader *bytes.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		reader = bytes.NewReader(data)
	} else {
		reader = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, target, reader)
	rec := httptest.NewRecorder()
	handler(rec, req)
	return rec
}

func TestTasksSaveGetRoundTrip(t *testing.T) {
	s := autoServer(t)
	task := subscription.Task{
		ID: "t-1", Name: "综合维护", Enabled: true,
		Schedule: subscription.TaskSchedule{Enabled: true, Cron: "0 3 * * *"},
		Rules:    []subscription.TaskRule{{Name: "美国", Limit: 10, Conditions: []subscription.Condition{{Field: "country", Values: []string{"US"}}}}},
		Output:   subscription.TaskOutput{Path: "out/sub.txt", Template: "{ip}:{port}#{country}"},
	}
	rec := doJSON(t, s.handleTasksSave, http.MethodPut, "/api/auto/tasks", map[string]any{"tasks": []subscription.Task{task}})
	if rec.Code != http.StatusOK {
		t.Fatalf("保存失败: %d %s", rec.Code, rec.Body.String())
	}
	rec = doJSON(t, s.handleTasksGet, http.MethodGet, "/api/auto/tasks", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("读取失败: %d", rec.Code)
	}
	var got struct {
		Tasks []subscription.Task `json:"tasks"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Tasks) != 1 || got.Tasks[0].Name != "综合维护" || got.Tasks[0].LibraryID != library.DefaultID {
		t.Fatalf("往返不一致: %+v", got.Tasks)
	}
	if !got.Tasks[0].Schedule.Enabled || got.Tasks[0].Schedule.Cron != "0 3 * * *" {
		t.Fatalf("定时配置未往返保留: %+v", got.Tasks[0].Schedule)
	}
	// 非法 Cron 的任务应被拒绝
	badSchedule := subscription.Task{Name: "x", Schedule: subscription.TaskSchedule{Enabled: true, Cron: "not cron"}, Rules: []subscription.TaskRule{{Name: "r", Limit: 1}}}
	if rec := doJSON(t, s.handleTasksSave, http.MethodPut, "/api/auto/tasks", map[string]any{"tasks": []subscription.Task{badSchedule}}); rec.Code != http.StatusBadRequest {
		t.Fatalf("非法定时应 400: %d %s", rec.Code, rec.Body.String())
	}
	if rec := doJSON(t, s.handleTasksSave, http.MethodPut, "/api/auto/tasks", map[string]any{"tasks": []subscription.Task{{Name: ""}}}); rec.Code != http.StatusBadRequest {
		t.Fatalf("非法任务应 400: %d %s", rec.Code, rec.Body.String())
	}
}

func TestTaskValidate(t *testing.T) {
	s := autoServer(t)
	bad := subscription.Task{Name: "x", Rules: []subscription.TaskRule{{Name: "r", Conditions: []subscription.Condition{{Field: "bad", Values: []string{"v"}}}}}}
	if rec := doJSON(t, s.handleTaskValidate, http.MethodPost, "/api/auto/tasks/validate", bad); rec.Code != http.StatusBadRequest {
		t.Fatalf("非法任务应 400: %d %s", rec.Code, rec.Body.String())
	}
	good := subscription.Task{Name: "x", Rules: []subscription.TaskRule{{Name: "r", Limit: 1}}}
	if rec := doJSON(t, s.handleTaskValidate, http.MethodPost, "/api/auto/tasks/validate", good); rec.Code != http.StatusOK {
		t.Fatalf("合法任务应 200: %d %s", rec.Code, rec.Body.String())
	}
}

func TestLibrariesCRUD(t *testing.T) {
	s := autoServer(t)
	// 首次访问自动建默认库（无旧数据则空库）
	rec := doJSON(t, s.handleLibrariesGet, http.MethodGet, "/api/auto/libraries", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("库列表失败: %d %s", rec.Code, rec.Body.String())
	}
	var got struct {
		Libraries []library.Info           `json:"libraries"`
		Stats     map[string]library.Stats `json:"stats"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if len(got.Libraries) < 1 || got.Libraries[0].ID != library.DefaultID || got.Libraries[0].Name != "默认库" {
		t.Fatalf("应有默认库: %+v", got.Libraries)
	}
	// 新建
	rec = doJSON(t, s.handleLibrariesCreate, http.MethodPost, "/api/auto/libraries", map[string]string{"name": "备用库"})
	var created struct {
		Library library.Info `json:"library"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &created)
	if created.Library.ID == "" || created.Library.Name != "备用库" {
		t.Fatalf("新建失败: %+v", created)
	}
	// 改名
	rec = doJSON(t, s.handleLibrariesRename, http.MethodPost, "/api/auto/libraries/rename", map[string]string{"id": created.Library.ID, "name": "日本库"})
	if rec.Code != http.StatusOK {
		t.Fatalf("改名失败: %s", rec.Body.String())
	}
	// 删除
	rec = doJSON(t, s.handleLibrariesDelete, http.MethodPost, "/api/auto/libraries/delete", map[string]string{"id": created.Library.ID})
	if rec.Code != http.StatusOK {
		t.Fatalf("删除失败: %s", rec.Body.String())
	}
	// 默认库不可删
	rec = doJSON(t, s.handleLibrariesDelete, http.MethodPost, "/api/auto/libraries/delete", map[string]string{"id": library.DefaultID})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("默认库删除应被拒: %d", rec.Code)
	}
}

func TestLibraryMigratesLegacy(t *testing.T) {
	s := autoServer(t)
	// 写入旧版单文件库
	legacy := filepath.Join(s.dataDir, library.FileName)
	if err := os.WriteFile(legacy, []byte("{\"ip\":\"1.1.1.1\",\"port\":443,\"status\":\"active\"}\n"), 0644); err != nil {
		t.Fatal(err)
	}
	rec := doJSON(t, s.handleLibrariesGet, http.MethodGet, "/api/auto/libraries", nil)
	var got struct {
		Stats map[string]library.Stats `json:"stats"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if got.Stats[library.DefaultID].Total != 1 {
		t.Fatalf("旧库应迁移为默认库且含 1 条: %+v", got.Stats)
	}
	if _, err := os.Stat(legacy); !os.IsNotExist(err) {
		t.Fatal("旧文件应已移除")
	}
}

func TestLibraryImportListAndResults(t *testing.T) {
	s := autoServer(t)
	rec := doJSON(t, s.handleLibraryImport, http.MethodPost, "/api/auto/library/import", autoImportRequest{
		Targets: []engine.Target{{IP: "1.1.1.1", Port: 443}, {IP: "2.2.2.2", Port: 2053}},
		Source:  library.SourceManual,
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("导入失败: %d %s", rec.Code, rec.Body.String())
	}
	var imp autoImportResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &imp)
	if imp.Added != 2 {
		t.Fatalf("导入统计错误: %+v", imp)
	}
	// 检测结果导入（更新已有 + 新增）
	rec = doJSON(t, s.handleLibraryImport, http.MethodPost, "/api/auto/library/import", autoImportRequest{
		Results: []engine.Result{{IP: "1.1.1.1", Port: 443, CountryCode: "US", Country: "美国", TCPLatencyMs: 120, DownloadSpeedKBs: 4500}},
	})
	_ = json.Unmarshal(rec.Body.Bytes(), &imp)
	if imp.Added != 0 || imp.Updated != 1 {
		t.Fatalf("结果导入统计错误: %+v", imp)
	}
	rec = doJSON(t, s.handleLibraryGet, http.MethodGet, "/api/auto/library?status=active", nil)
	var lib autoLibraryResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &lib)
	if lib.Total != 1 || lib.Stats.Active != 1 || lib.Stats.New != 1 {
		t.Fatalf("列表过滤错误: %+v", lib)
	}
}

func TestDebugLogToggleAndTail(t *testing.T) {
	s := autoServer(t)
	if s.log == nil {
		t.Fatal("logger 未初始化")
	}
	if s.log.Enabled() {
		t.Fatal("调试日志应默认关闭")
	}
	// 关闭时不写
	s.log.Log("info", "should-not-appear")
	// 开启后写
	s.log.SetEnabled(true)
	s.log.Log("info", "hello %s", "world")
	s.log.Log("error", "boom")
	lines := s.log.Tail(10)
	found := false
	for _, l := range lines {
		if len(l) > 0 && len(l) < 200 { // 简单校验有内容
			found = true
		}
	}
	if !found {
		t.Fatalf("开启后应能读到日志: %v", lines)
	}
	// HTTP 查看 + 清空
	rec := doJSON(t, s.handleLogGet, http.MethodGet, "/api/log", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("日志读取失败: %d", rec.Code)
	}
	var got struct {
		Enabled bool     `json:"enabled"`
		Lines   []string `json:"lines"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if !got.Enabled || len(got.Lines) == 0 {
		t.Fatalf("日志接口返回错误: %+v", got)
	}
	if rec := doJSON(t, s.handleLogClear, http.MethodPost, "/api/log/clear", nil); rec.Code != http.StatusOK {
		t.Fatalf("清空失败: %d", rec.Code)
	}
	if s.log.Enabled() && len(s.log.Tail(5)) == 0 {
		t.Fatal("清空后应为空")
	}
}

func TestAutoRunNotFoundAndConflict(t *testing.T) {
	s := autoServer(t)
	// 任务不存在 → 404
	rec := doJSON(t, s.handleAutoRun, http.MethodPost, "/api/auto/run", autoRunRequest{TaskID: "nope"})
	if rec.Code != http.StatusNotFound {
		t.Fatalf("应 404: %d %s", rec.Code, rec.Body.String())
	}
	subscription.SaveTasks(s.dataDir, []subscription.Task{{
		ID: "t-1", Name: "中文任务", Rules: []subscription.TaskRule{{Name: "r", Limit: 1}},
	}})
	ctx, ok := s.tryStartTask("occupied")
	if !ok || ctx == nil {
		t.Fatal("占用任务槽失败")
	}
	defer s.finishTask("occupied")
	// 中文任务 ID 应被找到（409 而非 404）
	rec = doJSON(t, s.handleAutoRun, http.MethodPost, "/api/auto/run", autoRunRequest{TaskID: "t-1"})
	if rec.Code != http.StatusConflict {
		t.Fatalf("中文任务应被找到（409），实际 %d: %s", rec.Code, rec.Body.String())
	}
}

func TestAutoOutputSecurity(t *testing.T) {
	s := autoServer(t)
	rec := doJSON(t, s.handleAutoOutput, http.MethodGet, "/api/auto/output?path=..%2F..%2Fsecret.txt", nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("路径穿越应 400: %d", rec.Code)
	}
	out := subscription.Output{Path: "out/t.txt", Template: "{ip}:{port}"}
	if _, err := subscription.WriteOutput(s.dataDir, out, []library.Entry{{IP: "9.9.9.9", Port: 443}}); err != nil {
		t.Fatal(err)
	}
	rec = doJSON(t, s.handleAutoOutput, http.MethodGet, "/api/auto/output?path=out%2Ft.txt", nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "9.9.9.9:443") {
		t.Fatalf("输出下载失败: %d %s", rec.Code, rec.Body.String())
	}
}

func TestResolveTaskInputNone(t *testing.T) {
	s := autoServer(t)
	task := subscription.Task{Name: "none-task", Input: subscription.TaskInput{Mode: "none", Protocol: "https", Port: 443}}
	targets, source, err := s.resolveTaskInput(task)
	if err != nil {
		t.Fatalf("none source should not error, got %v", err)
	}
	if targets != nil || len(targets) != 0 {
		t.Fatalf("none source should yield no targets, got %v", targets)
	}
	if source != "" {
		t.Fatalf("none source should yield empty source label, got %q", source)
	}
}

func TestAutoRunOptionsPriority(t *testing.T) {
	s := autoServer(t)
	if err := config.SaveSettings(s.dataDir, map[string]any{"autoLatencyConcurrency": 30, "autoLatencyTimeoutMs": 1400, "autoLatencyProbes": 4, "autoLatencyHTTPProbes": 3, "autoSpeedConcurrency": 6, "autoSpeedDurationSec": 9}); err != nil {
		t.Fatal(err)
	}
	task := &subscription.Task{Name: "x", LatencyConcurrency: 20, SpeedConcurrency: 4}

	// 仅全局默认
	opts := s.autoRunOptions(nil, nil)
	if opts.LatencyConcurrency != 30 || opts.SpeedConcurrency != 6 || opts.LatencyTimeoutMs != 1400 || opts.LatencyProbes != 4 || opts.LatencyHTTPProbes != 3 || opts.SpeedDurationSec != 9 {
		t.Fatalf("全局默认未完整读取: %+v", opts)
	}
	// 任务级覆盖全局
	opts = s.autoRunOptions(task, nil)
	if opts.LatencyConcurrency != 20 || opts.SpeedConcurrency != 4 || opts.LatencyTimeoutMs != 1400 || opts.LatencyProbes != 4 || opts.LatencyHTTPProbes != 3 || opts.SpeedDurationSec != 9 {
		t.Fatalf("任务级未保留其它全局参数: %+v", opts)
	}
	// 请求覆盖优先
	opts = s.autoRunOptions(task, &subscription.RunOptions{LatencyConcurrency: 10, SpeedConcurrency: 2, LatencyTimeoutMs: 700, LatencyProbes: 2, LatencyHTTPProbes: 2, SpeedDurationSec: 3})
	if opts.LatencyConcurrency != 10 || opts.SpeedConcurrency != 2 || opts.LatencyTimeoutMs != 700 || opts.LatencyProbes != 2 || opts.LatencyHTTPProbes != 2 || opts.SpeedDurationSec != 3 {
		t.Fatalf("请求覆盖未完整生效: %+v", opts)
	}
	// 请求覆盖为 0 时回退任务级
	opts = s.autoRunOptions(task, &subscription.RunOptions{})
	if opts.LatencyConcurrency != 20 || opts.SpeedConcurrency != 4 {
		t.Fatalf("请求为空应回退任务级 20/4，实际 %d/%d", opts.LatencyConcurrency, opts.SpeedConcurrency)
	}
	// 无任何配置时返回 0/0，表示回退引擎内置默认（50/5 兜底在 runCore 中生效，
	// 由 subscription.TestRunTaskConcurrencyOptions 覆盖验证）
	s2 := autoServer(t)
	opts = s2.autoRunOptions(nil, nil)
	if opts.LatencyConcurrency != 0 || opts.SpeedConcurrency != 0 {
		t.Fatalf("无配置应返回 0/0，实际 %d/%d", opts.LatencyConcurrency, opts.SpeedConcurrency)
	}
}
