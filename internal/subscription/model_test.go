package subscription

import (
	"testing"

	"iptest-web/internal/library"
)

func TestCandidatePriorityAndThresholds(t *testing.T) {
	group := Group{
		CountryCode:  "US",
		Ports:        []int{443},
		MaxLatencyMs: 300,
		MinSpeedKBs:  1000,
		RequireSpeed: true,
	}
	cases := []struct {
		name  string
		entry library.Entry
		want  int
	}{
		{name: "known match", entry: library.Entry{IP: "1.1.1.1", Port: 443, CountryCode: "US"}, want: 2},
		{name: "unknown country", entry: library.Entry{IP: "1.1.1.2", Port: 443}, want: 1},
		{name: "country mismatch", entry: library.Entry{IP: "1.1.1.3", Port: 443, CountryCode: "JP"}, want: 0},
		{name: "port mismatch", entry: library.Entry{IP: "1.1.1.4", Port: 2053, CountryCode: "US"}, want: 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := group.CandidatePriority(tc.entry); got != tc.want {
				t.Fatalf("CandidatePriority() = %d, want %d", got, tc.want)
			}
		})
	}
	if !group.LatencyOK(120) || group.LatencyOK(301) {
		t.Fatal("延迟阈值判断错误")
	}
	if !group.SpeedOK(2000, true) || group.SpeedOK(500, true) || group.SpeedOK(2000, false) {
		t.Fatal("速度阈值判断错误")
	}
}

func TestRender(t *testing.T) {
	e := library.Entry{
		IP: "1.2.3.4", Port: 443, Country: "美国", CountryCode: "US",
		Emoji: "🇺🇸", TCPLatencyMs: 123, SpeedKBs: 4567, SpeedValid: true, CityZh: "洛杉矶",
	}
	line := RenderLine("{ip}:{port}#{emoji}{country} {latency}ms {speed}kB/s", e)
	want := "1.2.3.4:443#🇺🇸美国 123ms 4567kB/s"
	if line != want {
		t.Fatalf("模板渲染错误: got=%q want=%q", line, want)
	}
	if got := RenderLine(DefaultTemplate, e); got != "1.2.3.4:443#美国" {
		t.Fatalf("默认模板错误: %q", got)
	}
	e.SpeedValid = false
	if got := RenderLine("{speed}", e); got != "" {
		t.Fatalf("无效速度应渲染为空: %q", got)
	}
}
