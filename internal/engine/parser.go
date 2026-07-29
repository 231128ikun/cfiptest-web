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
)

// ParseTargets 将原始文本（每行一个条目）解析为去重后的 Target 切片。
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
func ParseTargets(raw string) []Target {
	var targets []Target
	seen := make(map[string]struct{})

	for _, line := range strings.Split(raw, "\n") {
		t, ok := normalizeLine(line)
		if !ok {
			continue
		}
		key := t.IP + "|" + strconv.Itoa(t.Port)
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		targets = append(targets, t)
	}
	return targets
}

// normalizeLine 把单行文本规范化为一个 Target；无法识别时返回 false。
func normalizeLine(line string) (Target, bool) {
	line = strings.TrimSpace(line)
	if line == "" || strings.HasPrefix(line, "#") {
		return Target{}, false
	}

	// 去掉行内注释
	if idx := strings.Index(line, "#"); idx > 0 {
		line = strings.TrimSpace(line[:idx])
	}

	// CSV 元数据行：取首列递归规范化
	if fields := strings.Split(line, ","); len(fields) > 1 {
		return normalizeLine(strings.TrimSpace(fields[0]))
	}

	// [IPv6]:port
	if m := reIPv6BracketPort.FindStringSubmatch(line); m != nil {
		if port, ok := parsePort(m[2]); ok && net.ParseIP(m[1]) != nil {
			return Target{IP: m[1], Port: port}, true
		}
	}

	// 纯 IPv6（含冒号）→ 默认 443
	if strings.Contains(line, ":") && rePureIPv6.MatchString(line) {
		ip := strings.Trim(line, "[]")
		if net.ParseIP(ip) != nil && net.ParseIP(ip).To4() == nil {
			return Target{IP: ip, Port: 443}, true
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

	// 纯 IPv4 → 默认 443
	if rePureIPv4.MatchString(line) && isValidIPv4(line) {
		return Target{IP: line, Port: 443}, true
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
