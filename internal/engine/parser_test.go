package engine

import "testing"

func TestParseTargets(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  []Target
	}{
		{"标准 IPv4:端口", "1.2.3.4:443", []Target{{"1.2.3.4", 443, ""}}},
		{"纯 IPv4 保持未指定端口", "1.2.3.4", []Target{{"1.2.3.4", 0, ""}}},
		{"空格分隔", "1.2.3.4 2053", []Target{{"1.2.3.4", 2053, ""}}},
		{"中文冒号", "1.2.3.4：8443", []Target{{"1.2.3.4", 8443, ""}}},
		{"IPv6 带端口", "[2001:db8::1]:443", []Target{{"2001:db8::1", 443, ""}}},
		{"纯 IPv6 保持未指定端口", "2001:db8::1", []Target{{"2001:db8::1", 0, ""}}},
		{"带注释", "1.2.3.4:443 # 香港节点", []Target{{"1.2.3.4", 443, ""}}},
		{"注释行跳过", "# 这是注释", nil},
		{"空行跳过", "  ", nil},
		{"CSV 元数据取首列", "1.2.3.4:443,US,AS13335", []Target{{"1.2.3.4", 443, ""}}},
		{"去重", "1.2.3.4:443\n1.2.3.4:443\n1.2.3.4 443", []Target{{"1.2.3.4", 443, ""}}},
		{"无效端口", "1.2.3.4:0", nil},
		{"端口超界", "1.2.3.4:99999", nil},
		{"无效 IP 段", "1.2.3.999:443", nil},
		{"垃圾文本", "hello world", nil},
		{"多行混合", "1.1.1.1:443\n\n# 注释\n2.2.2.2 2053\n[2606:4700::1]:443",
			[]Target{{"1.1.1.1", 443, ""}, {"2.2.2.2", 2053, ""}, {"2606:4700::1", 443, ""}}},
		{"CRLF 换行", "1.1.1.1:443\r\n2.2.2.2:2053\r", []Target{{"1.1.1.1", 443, ""}, {"2.2.2.2", 2053, ""}}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ParseTargets(tt.input)
			if len(got) != len(tt.want) {
				t.Fatalf("ParseTargets(%q) 得到 %d 条 %v, 期望 %d 条 %v",
					tt.input, len(got), got, len(tt.want), tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("第 %d 条: 得到 %+v, 期望 %+v", i, got[i], tt.want[i])
				}
			}
		})
	}
}

// TestParseTargetsCIDR 覆盖 CIDR 与普通 IP 混写——用户粘贴的列表里
// 两者常常并存，不能因为出现一行网段就丢掉其余行。
func TestParseTargetsCIDR(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  int // 期望目标数（IP 是随机抽的，只能校验数量与端口）
		port  int // 期望端口，0 表示不校验
	}{
		{"裸网段保持未指定端口", "1.2.0.0/16", 256, -1},
		{"单 IP 网段", "1.2.3.4/32", 1, -1},
		{"网段带端口", "10.0.0.0/16:2053", 256, 2053},
		{"网段带中文冒号端口", "10.0.0.0/16：8443", 256, 8443},
		{"网段带注释", "10.0.0.0/24 # 测试段", 1, -1},
		{"IPv6 网段", "2606:4700::/32", 1024, -1},
		{"IPv6 网段带端口", "[2606:4700::/32]:8443", 1024, 8443},
		{"CIDR 与普通 IP 混写", "1.1.1.1:443\n10.0.0.0/16\n2.2.2.2:2053", 258, 0},
		{"非法网段被跳过但保留其余行", "1.2.3.0/99\n1.1.1.1:443", 1, 443},
		{"网段的 CSV 行取首列", "10.0.0.0/24,JP,AS13335", 1, -1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ParseTargets(tt.input)
			if len(got) != tt.want {
				t.Fatalf("ParseTargets(%q) 得到 %d 条, 期望 %d 条", tt.input, len(got), tt.want)
			}
			if tt.port != 0 {
				for _, g := range got {
					wantPort := tt.port
					if wantPort == -1 {
						wantPort = 0
					}
					if g.Port != wantPort {
						t.Errorf("目标 %+v 端口应为 %d", g, wantPort)
					}
				}
			}
		})
	}
}

// TestParseTargetsCIDRDedupes 确认展开后的目标仍会去重：
// 重叠网段（/16 覆盖其中的 /24）不应产出重复目标。
func TestParseTargetsCIDRDedupes(t *testing.T) {
	got := ParseTargetsWithCIDR("10.0.0.0/24\n10.0.0.0/24", SampleAll, 0)
	if len(got) != 256 {
		t.Errorf("同一网段写两遍得到 %d 条，期望 256（应去重）", len(got))
	}
}

// TestParseTargetsWithCIDRMode 确认抽样模式能穿透到解析层。
func TestParseTargetsWithCIDRMode(t *testing.T) {
	if got := ParseTargetsWithCIDR("10.0.0.0/24", SampleNPerSubnet, 7); len(got) != 7 {
		t.Errorf("每 /24 取 7 个得到 %d 条，期望 7", len(got))
	}
	if got := ParseTargetsWithCIDR("10.0.0.0/24", SampleAll, 0); len(got) != 256 {
		t.Errorf("全取得到 %d 条，期望 256", len(got))
	}
}

func TestParseTraceResponse(t *testing.T) {
	body := "fl=123f45\nh=speed.cloudflare.com\nip=104.16.0.1\nts=1720000000.123\ncolo=NRT\nloc=JP\nuag=Mozilla/5.0\n\nwarp=off\n"
	got := parseTraceResponse(body)

	checks := map[string]string{
		"ip":   "104.16.0.1",
		"colo": "NRT",
		"loc":  "JP",
		"uag":  "Mozilla/5.0",
		"warp": "off",
	}
	for k, want := range checks {
		if got[k] != want {
			t.Errorf("parseTraceResponse[%q] = %q, 期望 %q", k, got[k], want)
		}
	}
}

func TestGetIPType(t *testing.T) {
	cases := map[string]string{
		"1.2.3.4":      "IPv4",
		"2001:db8::1":  "IPv6",
		"":             "未知",
		"not-an-ip":    "无效IP",
		"255.255.0.10": "IPv4",
	}
	for in, want := range cases {
		if got := getIPType(in); got != want {
			t.Errorf("getIPType(%q) = %q, 期望 %q", in, got, want)
		}
	}
}

func TestResolveDefaultPorts(t *testing.T) {
	targets := []Target{{IP: "1.2.3.4", Port: 0}, {IP: "1.2.3.4", Port: 443}, {IP: "1.2.3.5", Port: 2053}}

	tls := ResolveDefaultPorts(targets, true)
	if len(tls) != 2 || tls[0].Port != 443 || tls[1].Port != 2053 {
		t.Fatalf("TLS 端口解析错误: %+v", tls)
	}

	plain := ResolveDefaultPorts(targets, false)
	if len(plain) != 3 || plain[0].Port != 80 || plain[1].Port != 443 || plain[2].Port != 2053 {
		t.Fatalf("非 TLS 端口解析错误: %+v", plain)
	}

	if targets[0].Port != 0 {
		t.Fatalf("ResolveDefaultPorts 不应修改原切片: %+v", targets)
	}
}

func TestColoLocRegex(t *testing.T) {
	body := "ip=1.2.3.4\ncolo=NRT\nloc=JP\nuag=Mozilla/5.0"
	m := reColoLoc.FindStringSubmatch(body)
	if len(m) <= 2 || m[1] != "NRT" || m[2] != "JP" {
		t.Errorf("正则提取失败: %v", m)
	}
}

func TestParseTargetsCSVUsesPortColumn(t *testing.T) {
	targets := ParseTargetsWithCIDR("ip,port,country\n1.1.1.1,2053,US\n2001:db8::1,8443,JP\n", SampleOnePerSubnet, 1)
	if len(targets) != 2 {
		t.Fatalf("CSV 解析得到 %d 个目标，期望 2: %+v", len(targets), targets)
	}
	if targets[0].IP != "1.1.1.1" || targets[0].Port != 2053 {
		t.Fatalf("IPv4 CSV 端口解析错误: %+v", targets[0])
	}
	if targets[1].IP != "2001:db8::1" || targets[1].Port != 8443 {
		t.Fatalf("IPv6 CSV 端口解析错误: %+v", targets[1])
	}
}
