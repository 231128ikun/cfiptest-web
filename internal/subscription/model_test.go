package subscription

import (
	"os"
	"path/filepath"
	"testing"

	"iptest-web/internal/library"
)

func sampleSubscription() Subscription {
	s := Subscription{
		Name:        "综合订阅",
		EnableSpeed: true,
		Groups: []Group{
			{Name: "美国", CountryCode: "us", Country: "美国", Count: 2, MaxLatencyMs: 300, MinSpeedKBs: 1000, RequireSpeed: true},
			{Name: "日本", CountryCode: "jp", Count: 1},
		},
		Output: Output{Path: "out/sub.txt", Template: "{ip}:{port}#{country}"},
	}
	if err := s.Validate(); err != nil {
		panic(err)
	}
	return s
}

func TestValidateNormalizes(t *testing.T) {
	s := sampleSubscription()
	if s.Groups[0].CountryCode != "US" {
		t.Fatalf("国家码应大写: %q", s.Groups[0].CountryCode)
	}
	if !s.EnableSpeed {
		t.Fatal("RequireSpeed 应隐式开启 EnableSpeed")
	}
	if s.Output.Format != "txt" {
		t.Fatalf("默认格式应为 txt: %q", s.Output.Format)
	}
}

func TestValidateErrors(t *testing.T) {
	cases := []Subscription{
		{Name: "", Groups: []Group{{Name: "x", Count: 1}}},
		{Name: "x", Groups: nil},
		{Name: "x", Groups: []Group{{Name: "a", Count: 0}}},
		{Name: "x", Groups: []Group{{Name: "a", Count: 1}, {Name: "a", Count: 1}}},
		{Name: "x", Groups: []Group{{Name: "a", Count: 1, Ports: []int{70000}}}},
		{Name: "x", Groups: []Group{{Name: "a", Count: 1}}, Output: Output{Format: "xml"}},
	}
	for i, c := range cases {
		if err := c.Validate(); err == nil {
			t.Fatalf("case %d 应校验失败", i)
		}
	}
}

func TestSaveLoadSubscriptions(t *testing.T) {
	dir := t.TempDir()
	subs := []Subscription{sampleSubscription()}
	if err := SaveSubscriptions(dir, subs); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, SubscriptionsFile)); err != nil {
		t.Fatal(err)
	}
	got, err := LoadSubscriptions(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Name != "综合订阅" || got[0].Groups[0].CountryCode != "US" {
		t.Fatalf("往返不一致: %+v", got)
	}
}

func TestSoftMatchAndThresholds(t *testing.T) {
	s := sampleSubscription()
	usGroup := s.Groups[0]
	// 国家已知且匹配
	e := library.Entry{IP: "1.1.1.1", Port: 443, CountryCode: "US"}
	if !usGroup.SoftMatch(e) {
		t.Fatal("US 条目应匹配美国组")
	}
	// 国家未知：国家限定组视为可测候选
	if !usGroup.SoftMatch(library.Entry{IP: "1.1.1.2", Port: 443}) {
		t.Fatal("国家未知条目应作为美国组候选")
	}
	// 国家明确不匹配
	if usGroup.SoftMatch(library.Entry{IP: "1.1.1.3", Port: 443, CountryCode: "JP"}) {
		t.Fatal("JP 条目不应匹配美国组")
	}
	// 端口限定
	g := Group{CountryCode: "JP", Ports: []int{2053}}
	if !g.SoftMatch(library.Entry{IP: "x", Port: 2053, CountryCode: "JP"}) {
		t.Fatal("端口 2053 应匹配")
	}
	if g.SoftMatch(library.Entry{IP: "x", Port: 443, CountryCode: "JP"}) {
		t.Fatal("端口 443 不应匹配")
	}
	// 阈值判断
	if !usGroup.LatencyOK(120) || usGroup.LatencyOK(301) {
		t.Fatal("延迟阈值判断错误")
	}
	if !usGroup.SpeedOK(2000, true) || usGroup.SpeedOK(500, true) || usGroup.SpeedOK(2000, false) {
		t.Fatal("速度阈值判断错误")
	}
	if !usGroup.CountryMatches("US") || usGroup.CountryMatches("JP") {
		t.Fatal("国家匹配判断错误")
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
	// 无效速度应输出空
	e.SpeedValid = false
	if got := RenderLine("{speed}", e); got != "" {
		t.Fatalf("无效速度应渲染为空: %q", got)
	}
}
