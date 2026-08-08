package subscription

import (
	"strings"

	"iptest-web/internal/library"
)

// CandidatePriority 是候选排序优先级：2=记录已匹配（最优先），1=字段值未知（测后可能匹配）。
// 返回 0 表示已知字段不匹配。
func (g Group) CandidatePriority(e library.Entry) int {
	unknown := false
	// 已知值不匹配 → 排除
	if g.CountryCode != "" && e.CountryCode != "" && !strings.EqualFold(e.CountryCode, g.CountryCode) {
		return 0
	}
	if len(g.Cities) > 0 && e.CityZh != "" && !containsFold(g.Cities, e.CityZh) {
		return 0
	}
	if len(g.DataCenters) > 0 && e.DataCenter != "" && !containsFold(g.DataCenters, e.DataCenter) {
		return 0
	}
	if len(g.Regions) > 0 && e.RegionZh != "" && !containsFold(g.Regions, e.RegionZh) {
		return 0
	}
	if len(g.ASNs) > 0 && e.ASN != 0 && !containsUint(g.ASNs, e.ASN) {
		return 0
	}
	if len(g.Ports) > 0 && !containsInt(g.Ports, e.Port) {
		return 0
	}
	// 约束字段值未知 → 仍作候选（测后可能匹配），优先级次之
	if g.CountryCode != "" && e.CountryCode == "" {
		unknown = true
	}
	if len(g.Cities) > 0 && e.CityZh == "" {
		unknown = true
	}
	if len(g.DataCenters) > 0 && e.DataCenter == "" {
		unknown = true
	}
	if len(g.Regions) > 0 && e.RegionZh == "" {
		unknown = true
	}
	if len(g.ASNs) > 0 && e.ASN == 0 {
		unknown = true
	}
	if unknown {
		return 1
	}
	return 2
}

// LatencyOK 判断实测延迟是否满足分组范围；0 表示不限。
func (g Group) LatencyOK(ms int64) bool {
	if g.LatencyMinMs > 0 && ms < g.LatencyMinMs {
		return false
	}
	if g.MaxLatencyMs > 0 && ms > g.MaxLatencyMs {
		return false
	}
	return ms > 0
}

// SpeedOK 判断实测速度是否满足分组范围；未测或无效视为不满足。
func (g Group) SpeedOK(kbs float64, valid bool) bool {
	if !valid {
		return false
	}
	if g.MinSpeedKBs > 0 && kbs < g.MinSpeedKBs {
		return false
	}
	if g.MaxSpeedKBs > 0 && kbs > g.MaxSpeedKBs {
		return false
	}
	return true
}

func containsFold(list []string, v string) bool {
	for _, x := range list {
		if strings.EqualFold(strings.TrimSpace(x), strings.TrimSpace(v)) {
			return true
		}
	}
	return false
}

func containsUint(list []uint, v uint) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

func containsInt(list []int, v int) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}
