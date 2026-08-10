package engine

import (
	"regexp"
	"strings"
)

// NormalizeCountry 把国家标识统一归一化为大写二字码（ISO 3166-1 alpha-2）：
//
//	"US" / "us" / "USA" / "美国" / "United States" → "US"
//	"HK" / "香港" / "Hong Kong"                     → "HK"
//
// 识别范围：二字/三字码（三字码按常见对照表收敛到二字码）、中文国家/地区名、
// 常见英文名。无法识别时返回原串去空白后的大写形式（保留语义，便于日志排查）；
// 空串返回空串。
func NormalizeCountry(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	up := strings.ToUpper(s)
	if reCountryTag.MatchString(up) {
		if a2, ok := countryAlpha3[up]; ok {
			return a2
		}
		return up
	}
	if a2, ok := countryByName[countryKey(s)]; ok {
		return a2
	}
	return up
}

// countryKey 生成国家名查找键：小写并去掉空白与常见标点，使
// " 美 国 "、"美国"、"United States" 等写法可命中同一表项。
func countryKey(s string) string {
	k := strings.ToLower(strings.TrimSpace(s))
	for _, r := range []string{" ", "\t", "　", "、", "，", ",", "；", ";", "（", "）", "(", ")", "·", "-", "_", "–"} {
		k = strings.ReplaceAll(k, r, "")
	}
	return k
}

var reLetters = regexp.MustCompile(`^[A-Za-z]{2,3}$`)

// countryAlpha3 常见三字码 → 二字码。
var countryAlpha3 = map[string]string{
	"USA": "US", "JPN": "JP", "KOR": "KR", "SGP": "SG", "HKG": "HK", "TWN": "TW", "MAC": "MO", "CHN": "CN",
	"NLD": "NL", "DEU": "DE", "GBR": "GB", "FRA": "FR", "RUS": "RU", "FIN": "FI", "LVA": "LV", "POL": "PL",
	"SWE": "SE", "ESP": "ES", "CHE": "CH", "AUT": "AT", "EST": "EE", "CZE": "CZ", "TUR": "TR", "CAN": "CA",
	"KAZ": "KZ", "BGR": "BG", "AUS": "AU", "ITA": "IT", "DNK": "DK", "IND": "IN", "THA": "TH", "BRA": "BR",
	"IDN": "ID", "MYS": "MY", "VNM": "VN", "PHL": "PH", "NZL": "NZ", "NOR": "NO", "BEL": "BE", "IRL": "IE",
	"PRT": "PT", "GRC": "GR", "ROU": "RO", "UKR": "UA", "LTU": "LT", "SVK": "SK", "SVN": "SI", "HRV": "HR",
	"SRB": "RS", "HUN": "HU", "ISL": "IS", "LUX": "LU", "CYP": "CY", "MLT": "MT", "MEX": "MX", "ARG": "AR",
	"CHL": "CL", "COL": "CO", "PER": "PE", "URY": "UY", "EGY": "EG", "ZAF": "ZA", "NGA": "NG", "KEN": "KE",
	"GHA": "GH", "MAR": "MA", "ISR": "IL", "SAU": "SA", "ARE": "AE", "QAT": "QA", "KWT": "KW", "BGD": "BD",
	"PAK": "PK", "LKA": "LK", "NPL": "NP", "MMR": "MM", "KHM": "KH", "LAO": "LA", "BRN": "BN", "MNG": "MN",
}

// countryByName 中文/英文国家或地区名（经 countryKey 清洗后）→ 二字码。
var countryByName = map[string]string{
	// 中国及特别行政区
	"中国": "CN", "中华人民共和国": "CN", "中国大陆": "CN",
	"香港": "HK", "中国香港": "HK", "hongkong": "HK", "hongkongchina": "HK",
	"澳门": "MO", "中国澳门": "MO", "macau": "MO", "macao": "MO",
	"台湾": "TW", "中国台湾": "TW", "taiwan": "TW",
	// 美国 / 日本 / 韩国 / 新加坡
	"美国": "US", "美利坚合众国": "US", "unitedstates": "US", "unitedstatesofamerica": "US", "usa": "US", "america": "US",
	"日本": "JP", "japan": "JP",
	"韩国": "KR", "南韩": "KR", "大韩民国": "KR", "southkorea": "KR", "korea": "KR", "republicofkorea": "KR",
	"新加坡": "SG", "singapore": "SG",
	// 欧洲
	"荷兰": "NL", "netherlands": "NL", "holland": "NL",
	"德国": "DE", "germany": "DE",
	"英国": "GB", "联合王国": "GB", "大不列颠": "GB", "unitedkingdom": "GB", "uk": "GB", "greatbritain": "GB", "britain": "GB",
	"法国": "FR", "france": "FR",
	"俄罗斯": "RU", "俄国": "RU", "russia": "RU",
	"芬兰": "FI", "finland": "FI",
	"拉脱维亚": "LV", "latvia": "LV",
	"波兰": "PL", "poland": "PL",
	"瑞典": "SE", "sweden": "SE",
	"西班牙": "ES", "spain": "ES",
	"瑞士": "CH", "switzerland": "CH",
	"奥地利": "AT", "austria": "AT",
	"爱沙尼亚": "EE", "estonia": "EE",
	"捷克": "CZ", "捷克共和国": "CZ", "czech": "CZ", "czechia": "CZ", "czechrepublic": "CZ",
	"土耳其": "TR", "turkey": "TR",
	"保加利亚": "BG", "bulgaria": "BG",
	"意大利": "IT", "italy": "IT",
	"丹麦": "DK", "denmark": "DK",
	"挪威": "NO", "norway": "NO",
	"比利时": "BE", "belgium": "BE",
	"爱尔兰": "IE", "ireland": "IE",
	"葡萄牙": "PT", "portugal": "PT",
	"希腊": "GR", "greece": "GR",
	"罗马尼亚": "RO", "romania": "RO",
	"乌克兰": "UA", "ukraine": "UA",
	"立陶宛": "LT", "lithuania": "LT",
	"斯洛伐克": "SK", "slovakia": "SK",
	"斯洛文尼亚": "SI", "slovenia": "SI",
	"克罗地亚": "HR", "croatia": "HR",
	"塞尔维亚": "RS", "serbia": "RS",
	"匈牙利": "HU", "hungary": "HU",
	"冰岛": "IS", "iceland": "IS",
	"卢森堡": "LU", "luxembourg": "LU",
	"塞浦路斯": "CY", "cyprus": "CY",
	"马耳他": "MT", "malta": "MT",
	"格鲁吉亚": "GE", "georgia": "GE",
	// 亚洲
	"印度": "IN", "india": "IN",
	"泰国": "TH", "thailand": "TH",
	"印度尼西亚": "ID", "indonesia": "ID",
	"马来西亚": "MY", "malaysia": "MY",
	"越南": "VN", "vietnam": "VN",
	"菲律宾": "PH", "philippines": "PH",
	"巴基斯坦": "PK", "pakistan": "PK",
	"孟加拉国": "BD", "bangladesh": "BD",
	"斯里兰卡": "LK", "srilanka": "LK",
	"尼泊尔": "NP", "nepal": "NP",
	"缅甸": "MM", "myanmar": "MM",
	"柬埔寨": "KH", "cambodia": "KH",
	"老挝": "LA", "laos": "LA",
	"文莱": "BN", "brunei": "BN",
	"蒙古": "MN", "mongolia": "MN",
	"哈萨克斯坦": "KZ", "kazakhstan": "KZ",
	"以色列": "IL", "israel": "IL",
	"沙特阿拉伯": "SA", "saudiarabia": "SA",
	"阿联酋": "AE", "阿拉伯联合酋长国": "AE", "unitedarabemirates": "AE", "uae": "AE",
	"卡塔尔": "QA", "qatar": "QA",
	"科威特": "KW", "kuwait": "KW",
	"伊朗": "IR", "iran": "IR",
	"伊拉克": "IQ", "iraq": "IQ",
	"约旦": "JO", "jordan": "JO",
	"黎巴嫩": "LB", "lebanon": "LB",
	// 美洲
	"加拿大": "CA", "canada": "CA",
	"墨西哥": "MX", "mexico": "MX",
	"巴西": "BR", "brazil": "BR",
	"阿根廷": "AR", "argentina": "AR",
	"智利": "CL", "chile": "CL",
	"哥伦比亚": "CO", "colombia": "CO",
	"秘鲁": "PE", "peru": "PE",
	"乌拉圭": "UY", "uruguay": "UY",
	"委内瑞拉": "VE", "venezuela": "VE",
	"厄瓜多尔": "EC", "ecuador": "EC",
	// 大洋洲 / 非洲
	"澳大利亚": "AU", "australia": "AU",
	"新西兰": "NZ", "newzealand": "NZ",
	"埃及": "EG", "egypt": "EG",
	"南非": "ZA", "southafrica": "ZA",
	"尼日利亚": "NG", "nigeria": "NG",
	"肯尼亚": "KE", "kenya": "KE",
	"加纳": "GH", "ghana": "GH",
	"摩洛哥": "MA", "morocco": "MA",
	"埃塞俄比亚": "ET", "ethiopia": "ET",
	"阿尔及利亚": "DZ", "algeria": "DZ",
	"突尼斯": "TN", "tunisia": "TN",
	"乌干达": "UG", "uganda": "UG",
	"坦桑尼亚": "TZ", "tanzania": "TZ",
}

// RegisterCountryNames 用已加载的 locations.json（本地国家/地区对照表）补充
// 中文名 → 二字码映射；静态表已有的条目优先，不覆盖。
func RegisterCountryNames(locs map[string]Location) {
	for _, loc := range locs {
		code := strings.ToUpper(strings.TrimSpace(loc.Cca2))
		name := strings.TrimSpace(loc.Country)
		if code == "" || name == "" || reLetters.MatchString(name) {
			continue
		}
		key := countryKey(name)
		if _, exists := countryByName[key]; !exists {
			countryByName[key] = code
		}
	}
}
