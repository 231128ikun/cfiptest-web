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
	"time"

	"iptest-web/internal/engine"
	"iptest-web/internal/library"
	"iptest-web/internal/subscription"
)

func autoServer(t *testing.T) *Server {
	t.Helper()
	return &Server{dataDir: t.TempDir()}
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
		Rules: []subscription.TaskRule{{Name: "美国", Limit: 10, Conditions: []subscription.Condition{{Field: "country", Values: []string{"US"}}}}},
		Output: subscription.TaskOutput{Path: "out/sub.txt", Template: "{ip}:{port}#{country}"},
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
		Libraries []library.Info        `json:"libraries"`
		Stats     map[string]library.Stats `json:"stats"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if len(got.Libraries) != 1 || got.Libraries[0].ID != library.DefaultID || got.Libraries[0].Name != "默认库" {
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
		Results: []engine.Result{{IP: "1.1.1.1", Port: 443, LocCode: "US", Country: "美国", TCPLatencyMs: 120, DownloadSpeedKBs: 4500}},
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

func TestRunsAppendAndList(t *testing.T) {
	s := autoServer(t)
	if err := appendRun(s.dataDir, RunRecord{TaskID: "t-1", Name: "甲", Status: "completed", StartedAt: time.Now(), FinishedAt: time.Now(), TotalLines: 3}); err != nil {
		t.Fatal(err)
	}
	if err := appendRun(s.dataDir, RunRecord{TaskID: "t-2", Name: "乙", Status: "error", StartedAt: time.Now(), FinishedAt: time.Now(), Error: "x"}); err != nil {
		t.Fatal(err)
	}
	rec := doJSON(t, s.handleRunsGet, http.MethodGet, "/api/auto/runs", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("历史读取失败: %d", rec.Code)
	}
	var got struct {
		Runs []RunRecord `json:"runs"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if len(got.Runs) != 2 || got.Runs[0].Name != "乙" {
		t.Fatalf("历史应按最新在前: %+v", got.Runs)
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
