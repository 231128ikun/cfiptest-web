package server

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseImportTextKeepsUnspecifiedPort(t *testing.T) {
	recorder := httptest.NewRecorder()
	targets, ok := parseImportText(recorder, "1.2.3.4\n5.6.7.8:2053", "one", 1)
	if !ok || len(targets) != 2 {
		t.Fatalf("解析失败: ok=%v targets=%+v body=%s", ok, targets, recorder.Body.String())
	}
	if targets[0].Port != 0 || targets[1].Port != 2053 {
		t.Fatalf("未指定与显式端口语义走样: %+v", targets)
	}
}

func TestImportResponseIncludesRawRemoteText(t *testing.T) {
	data, err := json.Marshal(importResponse{Text: "1.2.3.4:443#日本"})
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded["text"] != "1.2.3.4:443#日本" {
		t.Fatalf("远程响应没有保留原文供备注筛选: %s", data)
	}
}

func TestDetectRemoteFormat(t *testing.T) {
	tests := []struct {
		url, contentType, body, want string
	}{
		{"https://example.com/list.csv", "text/plain", "ip,port,country\n1.1.1.1,443,日本", "csv"},
		{"https://example.com/list", "text/csv; charset=utf-8", "x", "csv"},
		{"https://example.com/list", "text/plain", "IP地址,端口号,国家,城市\n1.1.1.1,443,日本,东京", "csv"},
		{"https://example.com/list.txt", "text/plain", "1.1.1.1:443\n", "text"},
	}
	for _, test := range tests {
		if got := detectRemoteFormat(test.url, test.contentType, test.body); got != test.want {
			t.Errorf("detectRemoteFormat(%q)=%q want %q", test.url, got, test.want)
		}
	}
}

func TestAutoInputUploadPersistsAtomicallyAndSanitizesName(t *testing.T) {
	s := autoServer(t)
	rec := doJSON(t, s.handleAutoInputUpload, "POST", "/api/auto/input/upload", map[string]any{
		"name": `..\\bad:name.csv`,
		"text": "1.1.1.1,2053,US\n",
	})
	if rec.Code != 200 {
		t.Fatalf("上传失败: %d %s", rec.Code, rec.Body.String())
	}
	var got autoInputUploadResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Targets != 1 || got.Bytes == 0 || got.Name != "bad_name.csv" {
		t.Fatalf("上传响应不符合预期: %+v", got)
	}
	body, err := os.ReadFile(filepath.Join(s.dataDir, filepath.FromSlash(got.Path)))
	if err != nil {
		t.Fatalf("上传文件不存在: %v", err)
	}
	if string(body) != "1.1.1.1,2053,US\n" {
		t.Fatalf("上传文件内容不完整: %q", body)
	}
	entries, err := os.ReadDir(filepath.Join(s.dataDir, "inputs"))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".auto-input-") {
			t.Fatalf("不应残留上传临时文件: %s", entry.Name())
		}
	}
	// 同名连续上传必须生成不同路径，不能覆盖定时任务正在读取的旧文件。
	rec2 := doJSON(t, s.handleAutoInputUpload, "POST", "/api/auto/input/upload", map[string]any{
		"name": "bad:name.csv",
		"text": "2.2.2.2,443,US\n",
	})
	if rec2.Code != 200 {
		t.Fatalf("第二次上传失败: %d %s", rec2.Code, rec2.Body.String())
	}
	var got2 autoInputUploadResponse
	if err := json.Unmarshal(rec2.Body.Bytes(), &got2); err != nil {
		t.Fatal(err)
	}
	if got.Path == got2.Path {
		t.Fatalf("同名上传路径被覆盖: %q", got.Path)
	}
}

func TestAutoInputUploadRejectsEmptyOrInvalid(t *testing.T) {
	s := autoServer(t)
	for _, body := range []map[string]any{
		{"name": "empty.txt", "text": ""},
		{"name": "header.csv", "text": "ip,port,country\n"},
	} {
		rec := doJSON(t, s.handleAutoInputUpload, "POST", "/api/auto/input/upload", body)
		if rec.Code < 400 {
			t.Fatalf("无效上传应失败: %d %s", rec.Code, rec.Body.String())
		}
	}
}
