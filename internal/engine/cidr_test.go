package engine

import (
	"net"
	"strings"
	"testing"
)

func TestCountCIDR(t *testing.T) {
	tests := []struct {
		cidr string
		mode SampleMode
		n    int
		want int
	}{
		// 每 /24 取 1 个
		{"1.2.3.4/32", SampleOnePerSubnet, 1, 1},
		{"1.2.3.0/24", SampleOnePerSubnet, 1, 1},
		{"1.2.0.0/16", SampleOnePerSubnet, 1, 256},
		{"104.16.0.0/13", SampleOnePerSubnet, 1, 2048},
		// 每 /24 取 N 个
		{"1.2.3.0/24", SampleNPerSubnet, 5, 5},
		{"1.2.0.0/16", SampleNPerSubnet, 2, 512},
		// 掩码比 /24 长时，可用地址不足 N 个应被截断
		{"1.2.3.0/30", SampleNPerSubnet, 10, 4},
		{"1.2.3.4/32", SampleNPerSubnet, 10, 1},
		// 全取
		{"1.2.3.0/24", SampleAll, 0, 256},
		{"1.2.0.0/16", SampleAll, 0, 65536},
		// IPv6：/64 为抽样单元；前缀短于 /64 时最多抽 1024 个 /64 子网
		{"2606:4700::/32", SampleOnePerSubnet, 1, 1024},
		{"2606:4700::/32", SampleNPerSubnet, 8, 8192},
		{"2606:4700::/48", SampleNPerSubnet, 2, 2048},
		// 前缀不低于 /64：网段不足一个 /64，只取 N 个
		{"2606:4700::/64", SampleOnePerSubnet, 1, 1},
		{"2606:4700::/64", SampleNPerSubnet, 8, 8},
		{"2606:4700::/126", SampleNPerSubnet, 10, 4},
		{"2606:4700::1/128", SampleNPerSubnet, 10, 1},
	}

	for _, tc := range tests {
		got, err := CountCIDR(tc.cidr, tc.mode, tc.n)
		if err != nil {
			t.Errorf("CountCIDR(%q, %v, %d) 返回错误: %v", tc.cidr, tc.mode, tc.n, err)
			continue
		}
		if got != tc.want {
			t.Errorf("CountCIDR(%q, %v, %d) = %d，期望 %d", tc.cidr, tc.mode, tc.n, got, tc.want)
		}
	}
}

func TestCountCIDRInvalid(t *testing.T) {
	for _, bad := range []string{"", "not-an-ip", "1.2.3.4", "1.2.3.4/33", "1.2.3.4/abc", "300.1.1.1/24"} {
		if _, err := CountCIDR(bad, SampleOnePerSubnet, 1); err == nil {
			t.Errorf("CountCIDR(%q) 期望报错，却成功了", bad)
		}
	}
}

// TestOfficialRangesCount 锁定官方段在默认抽样下的规模。
// 这个数字是「官方优选模式可用」的前提：152 万个地址压到约 6 千个，
// 才能在几分钟内测完。数字若变化说明抽样逻辑或内置段被改动了。
func TestOfficialRangesCount(t *testing.T) {
	total, skipped := CountCIDRs(BuiltinCFRanges.IPv4, SampleOnePerSubnet, 1)
	if len(skipped) > 0 {
		t.Fatalf("内置官方段中有无法解析的条目: %v", skipped)
	}
	const want = 5956
	if total != want {
		t.Errorf("官方 IPv4 段按每 /24 取 1 个 = %d 个 IP，期望 %d", total, want)
	}

	allTotal, _ := CountCIDRs(BuiltinCFRanges.IPv4, SampleAll, 0)
	const wantAll = 1524736
	if allTotal != wantAll {
		t.Errorf("官方 IPv4 段全取 = %d 个 IP，期望 %d", allTotal, wantAll)
	}
}

func TestExpandCIDRIPv4(t *testing.T) {
	// /16 每 /24 取 1 个：应得 256 个，且每个 /24 恰好命中一次
	targets, err := ExpandCIDR("10.1.0.0/16", SampleOnePerSubnet, 1, 8443)
	if err != nil {
		t.Fatalf("展开失败: %v", err)
	}
	if len(targets) != 256 {
		t.Fatalf("得到 %d 个目标，期望 256", len(targets))
	}

	thirdOctets := make(map[string]int)
	for _, tg := range targets {
		ip := net.ParseIP(tg.IP)
		if ip == nil || ip.To4() == nil {
			t.Fatalf("产出了非法 IPv4: %q", tg.IP)
		}
		if !strings.HasPrefix(tg.IP, "10.1.") {
			t.Errorf("IP %q 落在 10.1.0.0/16 之外", tg.IP)
		}
		if tg.Port != 8443 {
			t.Errorf("端口 = %d，期望 8443", tg.Port)
		}
		octets := strings.Split(tg.IP, ".")
		thirdOctets[octets[2]]++
	}
	if len(thirdOctets) != 256 {
		t.Errorf("覆盖了 %d 个不同的 /24，期望 256（每个子网应恰好取 1 个）", len(thirdOctets))
	}
}

func TestExpandCIDRAllIsExhaustive(t *testing.T) {
	targets, err := ExpandCIDR("192.168.5.0/24", SampleAll, 0, 443)
	if err != nil {
		t.Fatalf("展开失败: %v", err)
	}
	if len(targets) != 256 {
		t.Fatalf("得到 %d 个目标，期望 256", len(targets))
	}
	seen := make(map[string]struct{}, 256)
	for _, tg := range targets {
		seen[tg.IP] = struct{}{}
	}
	if len(seen) != 256 {
		t.Errorf("全取模式产出 %d 个不重复 IP，期望 256（不应有重复）", len(seen))
	}
	for _, want := range []string{"192.168.5.0", "192.168.5.1", "192.168.5.255"} {
		if _, ok := seen[want]; !ok {
			t.Errorf("全取模式缺少 %s", want)
		}
	}
}

func TestExpandCIDRSmallSubnet(t *testing.T) {
	// /30 全取应得 4 个且都在网段内
	targets, err := ExpandCIDR("172.16.8.4/30", SampleAll, 0, 443)
	if err != nil {
		t.Fatalf("展开失败: %v", err)
	}
	if len(targets) != 4 {
		t.Fatalf("得到 %d 个目标，期望 4", len(targets))
	}
	_, ipNet, _ := net.ParseCIDR("172.16.8.4/30")
	for _, tg := range targets {
		if !ipNet.Contains(net.ParseIP(tg.IP)) {
			t.Errorf("IP %q 落在 172.16.8.4/30 之外", tg.IP)
		}
	}
}

func TestExpandCIDRIPv6(t *testing.T) {
	_, ipNet, _ := net.ParseCIDR("2606:4700::/64")
	targets, err := ExpandCIDR("2606:4700::/64", SampleNPerSubnet, 16, 2053)
	if err != nil {
		t.Fatalf("展开失败: %v", err)
	}
	if len(targets) != 16 {
		t.Fatalf("得到 %d 个目标，期望 16", len(targets))
	}
	seen := make(map[string]struct{})
	for _, tg := range targets {
		ip := net.ParseIP(tg.IP)
		if ip == nil {
			t.Fatalf("产出了非法 IP: %q", tg.IP)
		}
		if ip.To4() != nil {
			t.Errorf("IPv6 段产出了 IPv4 地址: %q", tg.IP)
		}
		if !ipNet.Contains(ip) {
			t.Errorf("IP %q 落在 2606:4700::/64 之外", tg.IP)
		}
		if tg.Port != 2053 {
			t.Errorf("端口 = %d，期望 2053", tg.Port)
		}
		seen[tg.IP] = struct{}{}
	}
	if len(seen) != 16 {
		t.Errorf("产出 %d 个不重复地址，期望 16", len(seen))
	}
}

func TestExpandCIDRIPv6Subnets(t *testing.T) {
	// /56 = 256 个 /64 子网，每段取 1 个：应得 256 个且 /64 前缀各不相同
	_, ipNet, _ := net.ParseCIDR("2606:4700:0:0::/56")
	targets, err := ExpandCIDR("2606:4700:0:0::/56", SampleOnePerSubnet, 1, 443)
	if err != nil {
		t.Fatalf("展开失败: %v", err)
	}
	if len(targets) != 256 {
		t.Fatalf("得到 %d 个目标，期望 256", len(targets))
	}
	seen := make(map[string]struct{}, 256)
	for _, tg := range targets {
		ip := net.ParseIP(tg.IP)
		if ip == nil || ip.To4() != nil {
			t.Fatalf("产出了非法 IPv6: %q", tg.IP)
		}
		if !ipNet.Contains(ip) {
			t.Errorf("IP %q 落在 2606:4700::/56 之外", tg.IP)
		}
		b := ip.To16()
		prefix := net.IP(append([]byte(nil), b[:8]...)).String()
		seen[prefix] = struct{}{}
	}
	if len(seen) != 256 {
		t.Errorf("覆盖了 %d 个不同的 /64 子网，期望 256", len(seen))
	}
}
func TestExpandCIDRIPv6Single(t *testing.T) {
	targets, err := ExpandCIDR("2606:4700::1/128", SampleOnePerSubnet, 1, 443)
	if err != nil {
		t.Fatalf("展开失败: %v", err)
	}
	if len(targets) != 1 || targets[0].IP != "2606:4700::1" {
		t.Errorf("/128 展开为 %+v，期望单个 2606:4700::1", targets)
	}
}

func TestExpandCIDRsSkipsInvalid(t *testing.T) {
	targets, skipped := ExpandCIDRs(
		[]string{"10.0.0.0/24", "垃圾数据", "10.1.0.0/24"},
		SampleOnePerSubnet, 1, 443)
	if len(targets) != 2 {
		t.Errorf("得到 %d 个目标，期望 2（坏条目应被跳过而不影响其余）", len(targets))
	}
	if len(skipped) != 1 || skipped[0] != "垃圾数据" {
		t.Errorf("skipped = %v，期望仅含「垃圾数据」", skipped)
	}
}

func TestExpandCIDRDefaultsBadPort(t *testing.T) {
	for _, port := range []int{-1, 70000} {
		targets, err := ExpandCIDR("10.0.0.0/24", SampleOnePerSubnet, 1, port)
		if err != nil {
			t.Fatalf("展开失败: %v", err)
		}
		if targets[0].Port != 443 {
			t.Errorf("端口 %d 应回落到 443，实际 %d", port, targets[0].Port)
		}
	}
	targets, err := ExpandCIDR("10.0.0.0/24", SampleOnePerSubnet, 1, 0)
	if err != nil || targets[0].Port != 0 {
		t.Errorf("端口 0 应保留为未指定，实际 %+v, err=%v", targets, err)
	}
}

func TestIsCIDR(t *testing.T) {
	for _, ok := range []string{"1.2.3.0/24", "104.16.0.0/13", "2606:4700::/32", "1.2.3.4/32"} {
		if !IsCIDR(ok) {
			t.Errorf("IsCIDR(%q) = false，期望 true", ok)
		}
	}
	for _, bad := range []string{"1.2.3.4", "", "abc", "1.2.3.0/99", "1.2.3.0/"} {
		if IsCIDR(bad) {
			t.Errorf("IsCIDR(%q) = true，期望 false", bad)
		}
	}
}

func TestParseSampleMode(t *testing.T) {
	if ParseSampleMode("all") != SampleAll {
		t.Error(`ParseSampleMode("all") 未返回 SampleAll`)
	}
	if ParseSampleMode("n") != SampleNPerSubnet {
		t.Error(`ParseSampleMode("n") 未返回 SampleNPerSubnet`)
	}
	// 未知输入回落到默认模式，而不是报错或返回零散值
	for _, s := range []string{"", "随便写", "one"} {
		if ParseSampleMode(s) != SampleOnePerSubnet {
			t.Errorf("ParseSampleMode(%q) 未回落到 SampleOnePerSubnet", s)
		}
	}
}
