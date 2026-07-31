package engine

import (
	"net"
	"regexp"
	"strconv"
	"strings"
)

var (
	reIPv6BracketPort = regexp.MustCompile(`^\[([0-9a-fA-F:]+)\]:(\d+)$`)
	rePureIPv6        = regexp.MustCompile(`^[0-9a-fA-F:]+$`)
	reIPv4Port        = regexp.MustCompile(`^(\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3}):(\d+)$`)
	rePureIPv4        = regexp.MustCompile(`^\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3}$`)
	reIPv4CnPort      = regexp.MustCompile(`^(\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3})：(\d+)$`)
	reComplex         = regexp.MustCompile(`(\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3})\D+(\d+)`)

	reIPv4CIDRPort        = regexp.MustCompile(`^(\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3}/\d{1,2})[:：](\d+)$`)
	reIPv6BracketCIDRPort = regexp.MustCompile(`^\[([0-9a-fA-F:]+/\d{1,3})\]:(\d+)$`)
)

// ParseTargets 将原始文本（每行一个条目）解析为去重后的 Target 切片。
//
// CIDR 行按默认模式（每 /24 取 1 个）展开；需要指定抽样粒度时用 ParseTargetsWithCIDR。
func ParseTargets(raw string) []Target {
	return ParseTargetsWithCIDR(raw, SampleOnePerSubnet, 1)
}

// ParseTargetsWithCIDR 同 ParseTargets，但可指定 CIDR 行的抽样模式。
//
// 支持格式：
//   - "1.2.3.4:443"            标准
//   - "1.2.3.4"                纯 IPv4 → 默认 443
//   - "1.2.3.4 443"            空格分隔
//   - "1.2.3.4：443"           中文冒号
//   - "[2001:db8::1]:443"      IPv6 带端口
//   - "2001:db8::1"            纯 IPv6 → 默认 443
//   - "1.2.3.4:443 #注释"      带注释（注释被丢弃）
//   - "1.2.3.4:443,US,AS13335" CSV 元数据行（取首列）
//   - "104.16.0.0/13"          CIDR 网段 → 按抽样模式展开
//   - "104.16.0.0/13:2053"     CIDR 带端口
func ParseTargetsWithCIDR(raw string, mode SampleMode, n int) []Target {
	var targets []Target
	seen := make(map[string]struct{})

	add := func(t Target) {
		key := t.IP + "|" + strconv.Itoa(t.Port)
		if _, dup := seen[key]; dup {
			return
		}
		seen[key] = struct{}{}
		targets = append(targets, t)
	}

	for _, line := range strings.Split(raw, "\n") {
		// CIDR 行单独处理：一行可展开出成千上万个目标
		if cidr, port, ok := normalizeCIDRLine(line); ok {
			expanded, _ := ExpandCIDR(cidr, mode, n, port)
			for _, t := range expanded {
				add(t)
			}
			continue
		}
		if t, ok := normalizeLine(line); ok {
			add(t)
		}
	}
	return targets
}

// CollectCIDRs 挑出文本中所有合法的 CIDR 行（丢弃端口信息）。
//
// 供上层在真正展开前预估规模：全取模式下官方段是 152 万个地址，
// 先估后展开才不至于为了报一句「太多了」而先吃掉几百 MB 内存。
// 判定逻辑与 ParseTargetsWithCIDR 共用 normalizeCIDRLine，不会两边走样。
func CollectCIDRs(raw string) []string {
	var cidrs []string
	for _, line := range strings.Split(raw, "\n") {
		if cidr, _, ok := normalizeCIDRLine(line); ok {
			cidrs = append(cidrs, cidr)
		}
	}
	return cidrs
}

// normalizeCIDRLine 识别 CIDR 行，返回网段与端口（未指定端口时为 0，执行阶段再补）。
//
// 端口写法：
//   - "104.16.0.0/13"        → 443
//   - "104.16.0.0/13:2053"   → 2053
//   - "2606:4700::/32"       → 443（IPv6 段，冒号属于地址本身）
//   - "[2606:4700::/32]:80"  → 80
func normalizeCIDRLine(line string) (string, int, bool) {
	line = stripComment(line)
	if line == "" || !strings.Contains(line, "/") {
		return "", 0, false
	}

	// CSV 行取首列
	if fields := strings.Split(line, ","); len(fields) > 1 {
		return normalizeCIDRLine(strings.TrimSpace(fields[0]))
	}

	// [IPv6段]:port
	if m := reIPv6BracketCIDRPort.FindStringSubmatch(line); m != nil {
		if port, ok := parsePort(m[2]); ok && IsCIDR(m[1]) {
			return m[1], port, true
		}
	}

	// IPv4段:port —— IPv6 段的冒号是地址的一部分，故仅对 IPv4 写法拆端口
	if m := reIPv4CIDRPort.FindStringSubmatch(line); m != nil {
		if port, ok := parsePort(m[2]); ok && IsCIDR(m[1]) {
			return m[1], port, true
		}
	}

	// 裸网段（IPv4 或 IPv6）：保持未指定端口
	bare := strings.Trim(line, "[]")
	if IsCIDR(bare) {
		return bare, 0, true
	}
	return "", 0, false
}

// stripComment 去掉整行注释与行内注释，并修剪空白。
func stripComment(line string) string {
	line = strings.TrimSpace(line)
	if line == "" || strings.HasPrefix(line, "#") {
		return ""
	}
	if idx := strings.Index(line, "#"); idx > 0 {
		line = strings.TrimSpace(line[:idx])
	}
	return line
}

// normalizeLine 把单行文本规范化为一个 Target；无法识别时返回 false。
// CIDR 行不由本函数处理（见 normalizeCIDRLine）。
func normalizeLine(line string) (Target, bool) {
	line = stripComment(line)
	if line == "" {
		return Target{}, false
	}

	// CSV 元数据行：取首列递归规范化
	if fields := strings.Split(line, ","); len(fields) > 1 {
		return normalizeLine(strings.TrimSpace(fields[0]))
	}

	// 仍含 "/" 说明这是一行写坏的网段（合法网段已由 normalizeCIDRLine 处理）。
	// 必须在此拦掉：否则末尾的 reComplex 兜底会把 "1.2.3.0/99" 读成
	// IP 1.2.3.0 + 端口 99，把用户的笔误变成一个看似正常的目标。
	if strings.Contains(line, "/") {
		return Target{}, false
	}

	// [IPv6]:port
	if m := reIPv6BracketPort.FindStringSubmatch(line); m != nil {
		if port, ok := parsePort(m[2]); ok && net.ParseIP(m[1]) != nil {
			return Target{IP: m[1], Port: port}, true
		}
	}

	// 纯 IPv6（含冒号）→ 保持未指定端口
	if strings.Contains(line, ":") && rePureIPv6.MatchString(line) {
		ip := strings.Trim(line, "[]")
		if net.ParseIP(ip) != nil && net.ParseIP(ip).To4() == nil {
			return Target{IP: ip, Port: 0}, true
		}
	}

	// IPv4:port
	if m := reIPv4Port.FindStringSubmatch(line); m != nil {
		if port, ok := parsePort(m[2]); ok && isValidIPv4(m[1]) {
			return Target{IP: m[1], Port: port}, true
		}
	}

	// 中文冒号
	if m := reIPv4CnPort.FindStringSubmatch(line); m != nil {
		if port, ok := parsePort(m[2]); ok && isValidIPv4(m[1]) {
			return Target{IP: m[1], Port: port}, true
		}
	}

	// 空格分隔
	if parts := strings.Fields(line); len(parts) == 2 {
		if port, ok := parsePort(parts[1]); ok && isValidIPv4(parts[0]) {
			return Target{IP: parts[0], Port: port}, true
		}
	}

	// 纯 IPv4 → 保持未指定端口
	if rePureIPv4.MatchString(line) && isValidIPv4(line) {
		return Target{IP: line, Port: 0}, true
	}

	// 兜底：IPv4 + 非数字分隔 + 数字（如 "1.2.3.4 端口 443"）
	if m := reComplex.FindStringSubmatch(line); m != nil {
		if port, ok := parsePort(m[2]); ok && isValidIPv4(m[1]) {
			return Target{IP: m[1], Port: port}, true
		}
	}

	return Target{}, false
}

func isValidIPv4(ip string) bool {
	for _, octet := range strings.Split(ip, ".") {
		n, err := strconv.Atoi(octet)
		if err != nil || n < 0 || n > 255 {
			return false
		}
	}
	return true
}

func parsePort(s string) (int, bool) {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil || n < 1 || n > 65535 {
		return 0, false
	}
	return n, true
}
