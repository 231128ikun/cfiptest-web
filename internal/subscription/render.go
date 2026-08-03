package subscription

import (
	"fmt"
	"strconv"
	"strings"

	"iptest-web/internal/library"
)

// 模板占位符 -> 取值（与前端 web/js/composer.js 保持一致；库字段能提供的都在这里）。
var placeholders = map[string]func(e library.Entry) string{
	"{ip}":          func(e library.Entry) string { return e.IP },
	"{port}":        func(e library.Entry) string { return strconv.Itoa(e.Port) },
	"{country}":     func(e library.Entry) string { return e.Country },
	"{countryCode}": func(e library.Entry) string { return e.CountryCode },
	"{emoji}":       func(e library.Entry) string { return e.Emoji },
	"{city}":        func(e library.Entry) string { return e.CityZh },
	"{region}":      func(e library.Entry) string { return e.RegionZh },
	"{latency}":     func(e library.Entry) string { return strconv.FormatInt(e.TCPLatencyMs, 10) },
	"{speed}": func(e library.Entry) string {
		if !e.SpeedValid {
			return ""
		}
		return strconv.FormatFloat(speedValue(e), 'f', 0, 64)
	},
	"{dc}":         func(e library.Entry) string { return e.DataCenter },
	"{dataCenter}": func(e library.Entry) string { return e.DataCenter },
	"{locCode}":    func(e library.Entry) string { return e.CountryCode },
	"{asn}": func(e library.Entry) string {
		if e.ASN == 0 {
			return ""
		}
		return strconv.FormatUint(uint64(e.ASN), 10)
	},
	"{asnOrg}": func(e library.Entry) string { return e.ASNOrg },
}

func speedValue(e library.Entry) float64 {
	if e.DownloadSpeedKBs != 0 {
		return e.DownloadSpeedKBs
	}
	return e.SpeedKBs
}

// RenderLine 把一条库记录按模板渲染成一行订阅文本。
func RenderLine(template string, e library.Entry) string {
	out := template
	for ph, get := range placeholders {
		out = strings.ReplaceAll(out, ph, get(e))
	}
	return out
}

// RenderTXT 按模板渲染多条记录。
func RenderTXT(template string, entries []library.Entry) []string {
	lines := make([]string, 0, len(entries))
	for _, e := range entries {
		lines = append(lines, RenderLine(template, e))
	}
	return lines
}

// RenderCSV 输出 CSV（固定列：ip,port,country,countryCode,city,latencyMs,speedKBs）。
func RenderCSV(entries []library.Entry) []string {
	lines := make([]string, 0, len(entries)+1)
	lines = append(lines, "ip,port,country,countryCode,city,latencyMs,speedKBs")
	for _, e := range entries {
		speed := ""
		if e.SpeedValid {
			speed = strconv.FormatFloat(speedValue(e), 'f', 0, 64)
		}
		lines = append(lines, fmt.Sprintf("%s,%d,%s,%s,%s,%d,%s",
			e.IP, e.Port, e.Country, e.CountryCode, e.CityZh, e.TCPLatencyMs, speed))
	}
	return lines
}
