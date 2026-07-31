package server

import (
	"encoding/json"
	"testing"

	"iptest-web/internal/engine"
)

func intPtr(v int) *int           { return &v }
func floatPtr(v float64) *float64 { return &v }
func boolPtr(v bool) *bool        { return &v }

func TestLatencyRequestPipelineDTO(t *testing.T) {
	var req latencyRequest
	err := json.Unmarshal([]byte(`{
		"targets":[{"ip":"1.2.3.4","port":0}],
		"options":{"enableTLS":false,"maxResults":0},
		"enableSpeed":true,
		"speedOptions":{"minSpeedKBs":512,"maxResults":8}
	}`), &req)
	if err != nil {
		t.Fatal(err)
	}
	if !req.EnableSpeed || req.SpeedOptions == nil {
		t.Fatal("速度规则没有从请求体进入流水线 DTO")
	}
	if req.Options == nil || req.Options.MaxResults == nil || *req.Options.MaxResults != 0 {
		t.Fatal("显式 maxResults=0 必须保留为不限制，不能与未提供混淆")
	}
	if req.SpeedOptions.MaxResults == nil || *req.SpeedOptions.MaxResults != 8 {
		t.Fatal("统一最大数量没有进入最终测速阶段")
	}
	if req.SpeedOptions.MinSpeedKBs == nil || *req.SpeedOptions.MinSpeedKBs != 512 {
		t.Fatal("最低速度规则丢失")
	}
}

func TestOptionDTOExplicitZeroAndFalse(t *testing.T) {
	latency := (&latencyOptionsDTO{
		MaxResults: intPtr(0), EnableTLS: boolPtr(false),
	}).apply(engine.DefaultLatencyOptions())
	if latency.MaxResults != 0 || latency.EnableTLS {
		t.Fatalf("延迟 DTO 零值/false 覆盖失败: %+v", latency)
	}

	speed := (&speedOptionsDTO{
		MinSpeedKBs: floatPtr(0), MaxResults: intPtr(0), EnableTLS: boolPtr(false),
	}).apply(engine.DefaultSpeedOptions())
	if speed.MinSpeedKBs != 0 || speed.MaxResults != 0 || speed.EnableTLS {
		t.Fatalf("测速 DTO 零值/false 覆盖失败: %+v", speed)
	}
}
