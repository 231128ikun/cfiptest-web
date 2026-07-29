package engine

import "testing"

func TestParseTargets(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  []Target
	}{
		{"标准 IPv4:端口", "1.2.3.4:443", []Target{{"1.2.3.4", 443}}},
		{"纯 IPv4 默认 443", "1.2.3.4", []Target{{"1.2.3.4", 443}}},
		{"空格分隔", "1.2.3.4 2053", []Target{{"1.2.3.4", 2053}}},
		{"中文冒号", "1.2.3.4：8443", []Target{{"1.2.3.4", 8443}}},
		{"IPv6 带端口", "[2001:db8::1]:443", []Target{{"2001:db8::1", 443}}},
		{"纯 IPv6 默认 443", "2001:db8::1", []Target{{"2001:db8::1", 443}}},
		{"带注释", "1.2.3.4:443 # 香港节点", []Target{{"1.2.3.4", 443}}},
		{"注释行跳过", "# 这是注释", nil},
		{"空行跳过", "  ", nil},
		{"CSV 元数据取首列", "1.2.3.4:443,US,AS13335", []Target{{"1.2.3.4", 443}}},
		{"去重", "1.2.3.4:443\n1.2.3.4:443\n1.2.3.4 443", []Target{{"1.2.3.4", 443}}},
		{"无效端口", "1.2.3.4:0", nil},
		{"端口超界", "1.2.3.4:99999", nil},
		{"无效 IP 段", "1.2.3.999:443", nil},
		{"垃圾文本", "hello world", nil},
		{"多行混合", "1.1.1.1:443\n\n# 注释\n2.2.2.2 2053\n[2606:4700::1]:443",
			[]Target{{"1.1.1.1", 443}, {"2.2.2.2", 2053}, {"2606:4700::1", 443}}},
		{"CRLF 换行", "1.1.1.1:443\r\n2.2.2.2:2053\r", []Target{{"1.1.1.1", 443}, {"2.2.2.2", 2053}}},
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

func TestColoLocRegex(t *testing.T) {
	body := "ip=1.2.3.4\ncolo=NRT\nloc=JP\nuag=Mozilla/5.0"
	m := reColoLoc.FindStringSubmatch(body)
	if len(m) <= 2 || m[1] != "NRT" || m[2] != "JP" {
		t.Errorf("正则提取失败: %v", m)
	}
}
