package engine

import (
	"context"
	"strings"
	"testing"
)

func TestIsAutoSpeedURL(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want bool
	}{
		{"", true},
		{"auto", true},
		{"AUTO", true},
		{"  auto  ", true},
		{"自动选择", true},
		{"speed.cloudflare.com/__down", false},
		{"http://a/b", false},
	} {
		if got := isAutoSpeedURL(tc.in); got != tc.want {
			t.Errorf("isAutoSpeedURL(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestNormalizeDownloadURL(t *testing.T) {
	for _, tc := range []struct {
		raw    string
		scheme string
		want   string
	}{
		{"", "https", ""},
		{"speed.cloudflare.com/__down?bytes=1", "https", "https://speed.cloudflare.com/__down?bytes=1"},
		{"speed.cloudflare.com/__down?bytes=1", "http", "http://speed.cloudflare.com/__down?bytes=1"},
		{"//speed.cloudflare.com/__down", "http", "http://speed.cloudflare.com/__down"},
		{"//speed.cloudflare.com/__down", "https", "https://speed.cloudflare.com/__down"},
		{"https://speed.cloudflare.com/__down", "http", "https://speed.cloudflare.com/__down"},
		{"http://a/b", "https", "http://a/b"},
	} {
		if got := normalizeDownloadURL(tc.raw, tc.scheme); got != tc.want {
			t.Errorf("normalizeDownloadURL(%q, %q) = %q, want %q", tc.raw, tc.scheme, got, tc.want)
		}
	}
}

func TestIsChinaMobileASN(t *testing.T) {
	for _, tc := range []struct {
		asn  uint
		org  string
		want bool
	}{
		{9808, "", true},
		{24400, "China Mobile", true},
		{0, "China Mobile Communications Corporation", true},
		{0, "China Mobile Guangdong", true},
		{0, "CHINANET Guangdong", false},
		{4134, "China Telecom", false},
		{4837, "China Unicom", false},
		{0, "", false},
	} {
		if got := isChinaMobileASN(tc.asn, tc.org); got != tc.want {
			t.Errorf("isChinaMobileASN(%d, %q) = %v, want %v", tc.asn, tc.org, got, tc.want)
		}
	}
}

func TestResolveSpeedURLCustom(t *testing.T) {
	r := &Runner{}
	url, label := r.ResolveSpeedURL(context.Background(), "speed.cloudflare.com/__down?bytes=1", true)
	if url != "https://speed.cloudflare.com/__down?bytes=1" {
		t.Fatalf("url = %q", url)
	}
	if label != "自定义" {
		t.Fatalf("label = %q", label)
	}
	// 自定义 URL 显式带 http 时保留，即使 EnableTLS=true
	url, _ = r.ResolveSpeedURL(context.Background(), "http://a/b", true)
	if url != "http://a/b" {
		t.Fatalf("explicit http url = %q", url)
	}
}

func TestResolveSpeedURLAutoFallbackCloudflare(t *testing.T) {
	// ownASN 为 nil（无 ASN 库 / 探测失败）→ 回退 Cloudflare，且不触发网络。
	r := &Runner{}
	url, label := r.ResolveSpeedURL(context.Background(), "auto", true)
	if !strings.HasPrefix(url, "https://speed.cloudflare.com/") {
		t.Fatalf("url = %q, want cloudflare", url)
	}
	if !strings.Contains(label, "Cloudflare") || !strings.Contains(label, "自动选择") {
		t.Fatalf("label = %q", label)
	}
}

func TestResolveSpeedURLAutoMobile(t *testing.T) {
	r := &Runner{ownASN: func(ctx context.Context) (uint, string) {
		return 9808, "China Mobile Communications Corporation"
	}}
	url, label := r.ResolveSpeedURL(context.Background(), "自动选择", false)
	if !strings.HasPrefix(url, "http://") {
		t.Fatalf("http scheme expected, url = %q", url)
	}
	if !strings.Contains(url, "cf.090227.xyz") && !strings.Contains(url, "speed.okl.abrdns.com") {
		t.Fatalf("url = %q, want mobile source", url)
	}
	if !strings.Contains(label, "移动") {
		t.Fatalf("label = %q", label)
	}
}

func TestResolveSpeedURLAutoNonMobileUsesCloudflare(t *testing.T) {
	r := &Runner{ownASN: func(ctx context.Context) (uint, string) {
		return 4134, "China Telecom"
	}}
	url, _ := r.ResolveSpeedURL(context.Background(), "auto", true)
	if !strings.HasPrefix(url, "https://speed.cloudflare.com/") {
		t.Fatalf("非移动运营商应使用 Cloudflare，url = %q", url)
	}
}

func TestResolveSpeedURLAutoCachesResult(t *testing.T) {
	calls := 0
	r := &Runner{ownASN: func(ctx context.Context) (uint, string) {
		calls++
		return 9808, "China Mobile"
	}}
	_, _ = r.ResolveSpeedURL(context.Background(), "auto", true)
	_, _ = r.ResolveSpeedURL(context.Background(), "auto", true)
	if calls != 1 {
		t.Fatalf("探测次数 = %d, want 1（结果应缓存）", calls)
	}
}

func TestSpeedSourceEventIsSideLogJSON(t *testing.T) {
	ev := speedSourceEvent("Cloudflare（自动选择）")
	if ev.Type != EventAuto {
		t.Fatalf("type = %s", ev.Type)
	}
	if !strings.Contains(ev.Message, `"log"`) || !strings.Contains(ev.Message, "测速源") {
		t.Fatalf("message = %q", ev.Message)
	}
}
