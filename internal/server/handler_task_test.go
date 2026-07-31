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

func TestMergePipelineSpeedEventProducesFinalResult(t *testing.T) {
	latency := engine.Result{
		IP: "1.2.3.4", Port: 443, TCPLatencyMs: 42,
		Country: "日本", DataCenter: "NRT",
	}
	index := map[string]engine.Result{targetResultKey(latency.IP, latency.Port): latency}
	event, ok := mergePipelineSpeedEvent(engine.Event{
		Type:   engine.EventSpeed,
		Result: &engine.Result{IP: latency.IP, Port: latency.Port, DownloadSpeedKBs: 2048},
	}, index)
	if !ok || event.Type != engine.EventResult || event.Result == nil {
		t.Fatalf("测速达标项没有转换为最终 result: %#v, ok=%v", event, ok)
	}
	if event.Result.Country != "日本" || event.Result.TCPLatencyMs != 42 || event.Result.DownloadSpeedKBs != 2048 {
		t.Fatalf("最终结果没有合并完整元数据: %+v", event.Result)
	}
}

func TestMergePipelineProgressPhase(t *testing.T) {
	event, ok := mergePipelineSpeedEvent(engine.Event{
		Type:     engine.EventProgress,
		Progress: &engine.Progress{Completed: 1, Total: 2},
	}, nil)
	if !ok || event.Progress.Phase != "speed" {
		t.Fatalf("测速阶段进度缺少 phase=speed: %+v", event.Progress)
	}
}

func TestMergePipelineRejectsUnknownSpeedResult(t *testing.T) {
	_, ok := mergePipelineSpeedEvent(engine.Event{
		Type:   engine.EventSpeed,
		Result: &engine.Result{IP: "9.9.9.9", Port: 443, DownloadSpeedKBs: 10},
	}, map[string]engine.Result{})
	if ok {
		t.Fatal("未知目标的轻量 speed 事件不应进入最终结果")
	}
}

func TestBroadcastPartialResultsSkipsAlreadyEmitted(t *testing.T) {
	s := &Server{sseClients: make(map[chan engine.Event]struct{})}
	ch := make(chan engine.Event, 4)
	s.sseClients[ch] = struct{}{}
	results := []engine.Result{
		{IP: "1.1.1.1", Port: 443, TCPLatencyMs: 10},
		{IP: "2.2.2.2", Port: 443, TCPLatencyMs: 20},
	}
	emitted := map[string]struct{}{targetResultKey("1.1.1.1", 443): {}}
	if count := s.broadcastPartialResults("test", results, emitted); count != 1 {
		t.Fatalf("补发数量=%d，期望 1", count)
	}
	event := <-ch
	if event.Type != engine.EventResult || event.Result == nil || event.Result.IP != "2.2.2.2" {
		t.Fatalf("补发了错误结果: %+v", event)
	}
}
