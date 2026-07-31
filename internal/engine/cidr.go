package engine

import (
	"fmt"
	"math/rand/v2"
	"net"
	"strconv"
	"strings"
)

// SampleMode 决定如何从一个 CIDR 网段中抽取待测 IP。
//
// 抽样的依据：同一个 /24 内的 IP 几乎总是路由到同一个 Cloudflare 机房，
// 所以测其中 1 个就足以代表整个 /24。这是把官方段的 152 万个地址
// 压缩到 5956 个的关键——全测在实践中没有收益，只是慢几百倍。
// 该策略沿用 XIU2/CloudflareSpeedTest 的做法。
type SampleMode int

const (
	// SampleOnePerSubnet 每个 /24 随机取 1 个（默认）。
	SampleOnePerSubnet SampleMode = iota
	// SampleNPerSubnet 每个 /24 随机取 N 个（不重复，N ≥ 256 时等同全取）。
	SampleNPerSubnet
	// SampleAll 每个 /24 全取 256 个。仅对 IPv4 有意义，数量级极大。
	SampleAll
)

// ParseSampleMode 把前端传来的字符串转成 SampleMode，无法识别时返回默认模式。
func ParseSampleMode(s string) SampleMode {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "n", "nper", "nPerSubnet", "n_per_subnet":
		return SampleNPerSubnet
	case "all":
		return SampleAll
	default:
		return SampleOnePerSubnet
	}
}

// IsCIDR 判断一行文本是否是 CIDR 写法（含 "/" 且能被解析）。
func IsCIDR(s string) bool {
	s = strings.TrimSpace(s)
	if !strings.Contains(s, "/") {
		return false
	}
	_, _, err := net.ParseCIDR(s)
	return err == nil
}

// CountCIDR 返回按给定模式抽样后该网段产出的 IP 数量，不实际生成。
//
// 供前端在用户点「开始测试」之前显示「共 N 个 IP」——生成 152 万个
// net.IP 只为了数一下个数是没必要的开销。
func CountCIDR(cidr string, mode SampleMode, n int) (int, error) {
	_, ipNet, err := net.ParseCIDR(strings.TrimSpace(cidr))
	if err != nil {
		return 0, fmt.Errorf("无法解析网段 %q: %w", cidr, err)
	}

	ones, bits := ipNet.Mask.Size()
	if bits == 32 {
		perSubnet := ipsPerSubnet(mode, n)
		// /24 及更小的网段本身就是一个 /24（或其一部分），算 1 个子网
		if ones >= 24 {
			// 掩码比 /24 更长时，可用地址数可能不足 perSubnet
			avail := 1 << (32 - ones)
			if perSubnet > avail {
				perSubnet = avail
			}
			return perSubnet, nil
		}
		return (1 << (24 - ones)) * perSubnet, nil
	}

	// IPv6：每个网段固定抽样若干个（见 ExpandCIDR 说明）
	return ipv6SampleCount(mode, n), nil
}

// CountCIDRs 汇总多个网段的抽样数量。无法解析的网段被跳过并计入 skipped。
func CountCIDRs(cidrs []string, mode SampleMode, n int) (total int, skipped []string) {
	for _, c := range cidrs {
		cnt, err := CountCIDR(c, mode, n)
		if err != nil {
			skipped = append(skipped, c)
			continue
		}
		total += cnt
	}
	return total, skipped
}

// ExpandCIDR 把一个 CIDR 网段按抽样模式展开为待测 Target 列表。
//
// IPv4：遍历网段内每个 /24 子网，各随机取若干个末段。
// IPv6：网段通常极大（/32 有 2^96 个地址），无法穷举，因此对整段
// 随机抽取固定数量的地址——这与 CFST 的处理方式一致。
func ExpandCIDR(cidr string, mode SampleMode, n int, port int) ([]Target, error) {
	_, ipNet, err := net.ParseCIDR(strings.TrimSpace(cidr))
	if err != nil {
		return nil, fmt.Errorf("无法解析网段 %q: %w", cidr, err)
	}
	// 0 表示用户没有指定端口，执行阶段根据 TLS 规则补 443/80。
	if port < 0 || port > 65535 {
		port = 443
	}

	if _, bits := ipNet.Mask.Size(); bits == 32 {
		return expandIPv4(ipNet, mode, n, port), nil
	}
	return expandIPv6(ipNet, mode, n, port), nil
}

// ExpandCIDRs 依次展开多个网段并拼接结果。
// 无法解析的网段被跳过（计入 skipped），不影响其余网段——
// 用户粘贴的一大段文本里有一行写错，不该让整批作废。
func ExpandCIDRs(cidrs []string, mode SampleMode, n int, port int) (targets []Target, skipped []string) {
	for _, c := range cidrs {
		ts, err := ExpandCIDR(c, mode, n, port)
		if err != nil {
			skipped = append(skipped, c)
			continue
		}
		targets = append(targets, ts...)
	}
	return targets, skipped
}

// ipsPerSubnet 返回每个 /24 子网应抽取的 IP 数。
func ipsPerSubnet(mode SampleMode, n int) int {
	switch mode {
	case SampleAll:
		return 256
	case SampleNPerSubnet:
		if n < 1 {
			return 1
		}
		if n > 256 {
			return 256
		}
		return n
	default:
		return 1
	}
}

// expandIPv4 遍历网段内的每个 /24，逐个抽样。
func expandIPv4(ipNet *net.IPNet, mode SampleMode, n int, port int) []Target {
	ones, _ := ipNet.Mask.Size()
	perSubnet := ipsPerSubnet(mode, n)

	base := ipNet.IP.Mask(ipNet.Mask).To4()
	if base == nil {
		return nil
	}

	// /25 ~ /32：网段本身小于一个 /24，只能在其自身范围内抽样
	if ones > 24 {
		size := 1 << (32 - ones)
		if perSubnet > size {
			perSubnet = size
		}
		offsets := pickOffsets(size, perSubnet)
		targets := make([]Target, 0, len(offsets))
		for _, off := range offsets {
			targets = append(targets, Target{IP: ipv4At(base, off), Port: port})
		}
		return targets
	}

	subnets := 1 << (24 - ones) // 网段内 /24 的个数
	targets := make([]Target, 0, subnets*perSubnet)
	for i := 0; i < subnets; i++ {
		// 第 i 个 /24 的起始地址：base + i*256
		start := i << 8
		for _, off := range pickOffsets(256, perSubnet) {
			targets = append(targets, Target{IP: ipv4At(base, start+off), Port: port})
		}
	}
	return targets
}

// pickOffsets 从 [0, size) 中取 count 个不重复的偏移。
// count == size 时按顺序全取；否则随机抽取。
func pickOffsets(size, count int) []int {
	if count >= size {
		all := make([]int, size)
		for i := range all {
			all[i] = i
		}
		return all
	}
	if count == 1 {
		return []int{rand.IntN(size)}
	}
	perm := rand.Perm(size)[:count]
	return perm
}

// ipv4At 返回 base 加上 offset 后的 IPv4 字符串。
func ipv4At(base net.IP, offset int) string {
	v := uint32(base[0])<<24 | uint32(base[1])<<16 | uint32(base[2])<<8 | uint32(base[3])
	v += uint32(offset)
	return strconv.Itoa(int(v>>24&0xff)) + "." +
		strconv.Itoa(int(v>>16&0xff)) + "." +
		strconv.Itoa(int(v>>8&0xff)) + "." +
		strconv.Itoa(int(v&0xff))
}

// ipv6SampleCount 返回一个 IPv6 网段抽样的地址数。
//
// IPv6 网段无法按 /24 划分子网穷举，故采用固定采样数：
// 默认 1 个，指定 N 时取 N（上限 4096，避免用户误填天文数字），
// "全部"模式对 IPv6 无意义，退化为一个较大的固定值。
func ipv6SampleCount(mode SampleMode, n int) int {
	const ipv6Cap = 4096
	switch mode {
	case SampleAll:
		return 256
	case SampleNPerSubnet:
		if n < 1 {
			return 1
		}
		if n > ipv6Cap {
			return ipv6Cap
		}
		return n
	default:
		return 1
	}
}

// expandIPv6 在网段的可变位范围内随机抽取地址。
func expandIPv6(ipNet *net.IPNet, mode SampleMode, n int, port int) []Target {
	count := ipv6SampleCount(mode, n)
	ones, bits := ipNet.Mask.Size()
	hostBits := bits - ones

	base := ipNet.IP.Mask(ipNet.Mask).To16()
	if base == nil {
		return nil
	}

	// 主机位为 0 时网段只含一个地址（/128）
	if hostBits == 0 {
		return []Target{{IP: base.String(), Port: port}}
	}

	seen := make(map[string]struct{}, count)
	targets := make([]Target, 0, count)
	// 抽样可能撞重复，给足尝试次数后就此收手（网段太小时天然取不满 count）
	for attempt := 0; len(targets) < count && attempt < count*4+16; attempt++ {
		ip := randomIPv6In(base, hostBits)
		s := ip.String()
		if _, dup := seen[s]; dup {
			continue
		}
		seen[s] = struct{}{}
		targets = append(targets, Target{IP: s, Port: port})
	}
	return targets
}

// randomIPv6In 在 base 的低 hostBits 位上填随机值。
func randomIPv6In(base net.IP, hostBits int) net.IP {
	ip := make(net.IP, net.IPv6len)
	copy(ip, base)

	// 从最低字节向高位填充，直到覆盖 hostBits 位
	for bit := 0; bit < hostBits; bit += 8 {
		idx := net.IPv6len - 1 - bit/8
		remaining := hostBits - bit
		r := byte(rand.IntN(256))
		if remaining < 8 {
			// 该字节只有低 remaining 位属于主机位，高位须保留网络前缀
			mask := byte(1<<remaining - 1)
			ip[idx] = base[idx]&^mask | r&mask
		} else {
			ip[idx] = r
		}
	}
	return ip
}
