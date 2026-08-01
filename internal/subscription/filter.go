package subscription

import (
	"strings"

	"iptest-web/internal/library"
)

// SoftMatch 是候选粗筛：只看国家与端口。
// 国家未知的条目（未测过）也视为候选——测完才知道属于哪个国家；
// 延迟/速度阈值不在此判断，需要现测。
func (g Group) SoftMatch(e library.Entry) bool {
	if g.CountryCode != "" && e.CountryCode != "" && !strings.EqualFold(e.CountryCode, g.CountryCode) {
		return false
	}
	if len(g.Ports) > 0 && !containsInt(g.Ports, e.Port) {
		return false
	}
	return true
}

// CandidatePriority 是候选排序优先级：2=记录国家已匹配（最优先），1=国家未知（测后可能匹配）。
// 返回 0 表示不匹配（SoftMatch 应已排除，这里兜底）。
func (g Group) CandidatePriority(e library.Entry) int {
	if g.CountryCode == "" {
		if len(g.Ports) > 0 && !containsInt(g.Ports, e.Port) {
			return 0
		}
		return 2
	}
	if e.CountryCode == "" {
		return 1
	}
	if strings.EqualFold(e.CountryCode, g.CountryCode) {
		return 2
	}
	return 0
}

// RequiresSpeed 判断该分组是否需要测速结果。
func (g Group) RequiresSpeed(sub Subscription) bool {
	return sub.EnableSpeed && (g.RequireSpeed || g.MinSpeedKBs > 0)
}

// LatencyOK 判断实测延迟是否满足分组阈值；0 表示不限。
func (g Group) LatencyOK(ms int64) bool {
	return g.MaxLatencyMs <= 0 || (ms > 0 && ms <= g.MaxLatencyMs)
}

// SpeedOK 判断实测速度是否满足分组阈值；未测或无效视为不满足。
func (g Group) SpeedOK(kbs float64, valid bool) bool {
	if !valid {
		return false
	}
	if g.MinSpeedKBs > 0 && kbs < g.MinSpeedKBs {
		return false
	}
	return true
}

// CountryMatches 判断实测国家码是否属于该分组（分组不限国家时恒真）。
func (g Group) CountryMatches(code string) bool {
	if g.CountryCode == "" {
		return true
	}
	return strings.EqualFold(code, g.CountryCode)
}

func containsInt(list []int, v int) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}
