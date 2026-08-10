package engine

import "testing"

func TestNormalizeCountry(t *testing.T) {
	tests := []struct{ in, want string }{
		{"US", "US"}, {"us", "US"}, {"usa", "US"}, {"USA", "US"}, {"\u7f8e\u56fd", "US"},
		{"United States", "US"}, {" UNITED STATES ", "US"},
		{"HK", "HK"}, {"hk", "HK"}, {"hkg", "HK"}, {"\u9999\u6e2f", "HK"}, {"Hong Kong", "HK"},
		{"JP", "JP"}, {"\u65e5\u672c", "JP"}, {"japan", "JP"},
		{"KR", "KR"}, {"\u97e9\u56fd", "KR"}, {"Korea", "KR"},
		{"SG", "SG"}, {"\u65b0\u52a0\u5761", "SG"}, {"Singapore", "SG"},
		{"", ""},
	}
	for _, tt := range tests {
		if got := NormalizeCountry(tt.in); got != tt.want {
			t.Errorf("NormalizeCountry(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}
