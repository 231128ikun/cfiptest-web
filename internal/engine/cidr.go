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

	// IPv6：按 /48 子网抽样（见 ipv6SampleCount 说明）
	return ipv6SampleCount(mode, n, ones), nil
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
// IPv6：以 /48 为抽样单元（对应 IPv4 的 /24，Cloudflare 活跃列表就是 /48）。
// 前缀短于 /48 时随机抽至多 1024 个不同的 /48 子网、每个子网取 N 个；
// 前缀不低于 /48 时网段不足一个 /48，仅在自身范围内取 N 个。
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

// ipv6SampleCount 返回一个 IPv6 网段按模式抽样后产出的地址数。
// 前缀短于 /64：抽 min(2^(64-ones), 1024) 个 /64 子网，各取 N 个；
// 前缀不低于 /64：网段不足一个 /64，仅取 N 个（受可用地址数约束）。
//
// IPv6 以 /48 为抽样单元（对应 IPv4 的 /24）：同一 /48 内的地址几乎总是路由到
// 同一批 Cloudflare 机房，每段抽 N 个即可代表。官方聚合段（/29~/32）里绝大多数
// 地址并未部署服务，直接随机抽样几乎必然全部失败，因此官方 IPv6 模式改为使用
// baipiao 维护的活跃 /48 列表（见 DefaultActiveIPv6RangeSources）。对更短的前缀
// 每个网段最多抽 ipv6MaxSubnets 个不同的 /48 子网，把规模控制在可测的量级。
const (
	ipv6SubnetBits    = 48 // 抽样单元前缀长度（对应 IPv4 的 /24）
	ipv6MaxSubnetBits = 10 // 前缀短于 /48 时，最多抽 2^10 = 1024 个 /48 子网
	ipv6MaxSubnets    = 1 << ipv6MaxSubnetBits
	ipv6PerSubnetCap  = 256 // 每个 /48 内最多抽取的地址数
)

// ipv6SubnetCount 返回网段内 /48 子网的个数；前缀短于 /48 时上限 1024。
func ipv6SubnetCount(ones int) int {
	if ones >= ipv6SubnetBits {
		return 1
	}
	host := ipv6SubnetBits - ones
	if host > ipv6MaxSubnetBits {
		return ipv6MaxSubnets
	}
	return 1 << host
}

// ipv6PerSubnetCount 返回每个 /64 子网内应抽取的地址数。
func ipv6PerSubnetCount(mode SampleMode, n int) int {
	switch mode {
	case SampleAll:
		return ipv6PerSubnetCap
	case SampleNPerSubnet:
		if n < 1 {
			return 1
		}
		if n > ipv6PerSubnetCap {
			return ipv6PerSubnetCap
		}
		return n
	default:
		return 1
	}
}

// ipv6SampleCount 计算单个网段的抽样数量（见上方注释）。
func ipv6SampleCount(mode SampleMode, n int, ones int) int {
	perSubnet := ipv6PerSubnetCount(mode, n)
	if ones < ipv6SubnetBits {
		return ipv6SubnetCount(ones) * perSubnet
	}
	host := 128 - ones
	if host < 8 && perSubnet > 1<<host {
		return 1 << host
	}
	return perSubnet
}

// expandIPv6 展开一个 IPv6 网段：
// 前缀短于 /48 时，随机抽至多 1024 个不同的 /48 子网，每个子网内随机取 N 个主机；
// 前缀不低于 /48 时，直接在网段可用主机范围内随机取 N 个。
func expandIPv6(ipNet *net.IPNet, mode SampleMode, n int, port int) []Target {
	ones, _ := ipNet.Mask.Size()
	perSubnet := ipv6PerSubnetCount(mode, n)

	base := ipNet.IP.Mask(ipNet.Mask).To16()
	if base == nil {
		return nil
	}

	if ones < ipv6SubnetBits {
		subnets := ipv6SubnetCount(ones)
		targets := make([]Target, 0, subnets*perSubnet)
		seen := make(map[uint64]struct{}, subnets)
		// 网段极大时抽中重复 /48 的概率可忽略；尝试次数给足余量，
		// 即使极端情况下抽不满也直接返回，避免死循环。
		for len(seen) < subnets && len(seen) < (subnets+32)*64 {
			idx := rand.Uint64() & ipv6SubnetMask(ones)
			if _, dup := seen[idx]; dup {
				continue
			}
			seen[idx] = struct{}{}
			for i := 0; i < perSubnet; i++ {
				targets = append(targets, Target{IP: ipv6AtSubnet(base, idx, ones), Port: port})
			}
		}
		return targets
	}

	// 网段不足一个 /64：可用主机位可能不足 N 个，先截断再抽样
	hostBits := 128 - ones
	if hostBits < 8 && perSubnet > 1<<hostBits {
		perSubnet = 1 << hostBits
	}
	seen := make(map[string]struct{}, perSubnet)
	targets := make([]Target, 0, perSubnet)
	// 抽样可能撞重复，给足尝试次数后就此收手（网段太小时天然取不满）
	for attempt := 0; len(targets) < perSubnet && attempt < perSubnet*4+16; attempt++ {
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

// ipv6SubnetMask 返回 2^(48-ones)-1，用于把随机数限定在网段内 /48 的序号范围。
func ipv6SubnetMask(ones int) uint64 {
	bits := ipv6SubnetBits - ones
	if bits <= 0 {
		return 0
	}
	if bits >= 64 {
		return ^uint64(0)
	}
	return 1<<bits - 1
}

// ipv6AtSubnet 返回 base 网段内第 idx 个 /48 子网中的随机主机地址。
func ipv6AtSubnet(base net.IP, idx uint64, ones int) string {
	ip := make(net.IP, net.IPv6len)
	copy(ip, base)

	// 子网序号 idx（< 2^(48-ones)）逐位写入 bits [ones, 48)；
	// base 在该区间的位全为零，因此直接 OR 不会覆盖网络前缀。
	// 注意字节内位序：addrBit%8 是「从高位起」的位号，掩码须用 1<<(7-addrBit%8)，
	// 否则对 /29 这类非字节对齐前缀会生成落在网段之外的地址。
	for b := 0; b < ipv6SubnetBits-ones; b++ {
		if idx&(1<<uint(b)) != 0 {
			addrBit := ones + b
			ip[addrBit/8] |= 1 << uint(7-addrBit%8)
		}
	}
	// 主机位（bits 48..127，后 10 字节）随机
	var h uint64 = rand.Uint64()
	for i := 15; i >= 6; i-- {
		ip[i] = byte(h)
		h >>= 8
	}
	return ip.String()
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
