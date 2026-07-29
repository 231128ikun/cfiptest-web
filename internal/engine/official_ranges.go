package engine

// DefaultOfficialRangeSources 是 Cloudflare 官方 IP 段的默认获取地址。
//
// 官方 API 免鉴权可用，返回结构化 JSON（比抓 /ips-v4 纯文本更稳），
// 因此只列这一个；拉取失败时回落到下面的 BuiltinCFRanges。
var DefaultOfficialRangeSources = []string{
	"https://api.cloudflare.com/client/v4/ips",
}

// BuiltinCFRanges 是内置的 Cloudflare 官方 IP 段兜底数据。
//
// 官方段多年未变，且远端拉取失败时程序仍应能进入官方优选模式，
// 故硬编码一份。与远端返回不一致时以远端为准。
var BuiltinCFRanges = struct {
	IPv4 []string
	IPv6 []string
}{
	IPv4: []string{
		"173.245.48.0/20",
		"103.21.244.0/22",
		"103.22.200.0/22",
		"103.31.4.0/22",
		"141.101.64.0/18",
		"108.162.192.0/18",
		"190.93.240.0/20",
		"188.114.96.0/20",
		"197.234.240.0/22",
		"198.41.128.0/17",
		"162.158.0.0/15",
		"104.16.0.0/13",
		"104.24.0.0/14",
		"172.64.0.0/13",
		"131.0.72.0/22",
	},
	IPv6: []string{
		"2400:cb00::/32",
		"2606:4700::/32",
		"2803:f800::/32",
		"2405:b500::/32",
		"2405:8100::/32",
		"2a06:98c0::/29",
		"2c0f:f248::/32",
	},
}
