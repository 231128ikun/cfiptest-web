package server

import (
	"encoding/json"
	"net/http/httptest"
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
