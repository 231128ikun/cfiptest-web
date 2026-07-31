package server

import (
	"encoding/json"
	"net"
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

func TestBlockedAddressCoverage(t *testing.T) {
	blocked := []string{"127.0.0.1", "10.0.0.1", "169.254.169.254", "100.64.0.1", "240.0.0.1", "::1", "fd00::1"}
	for _, raw := range blocked {
		if !isBlockedIP(net.ParseIP(raw)) {
			t.Errorf("应拦截 %s", raw)
		}
	}
	if isBlockedIP(net.ParseIP("1.1.1.1")) {
		t.Fatal("公网地址 1.1.1.1 不应被拦截")
	}
}
