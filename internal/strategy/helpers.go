// File: internal/strategy/helpers.go
package strategy

import (
	"strings"
	"time"
)

// Target from the external program. (유지)
type HedgeTarget struct {
	Side     int8
	QtyBTC   float64
	BaseUSD  float64
	IndexUSD float64
	Seq      uint64
}

// ── math helpers ─────────────────────────────────────────────────────────────
func minf(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}
func maxf(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}
func min3(a, b, c float64) float64 {
	if a < b {
		if a < c {
			return a
		}
		return c
	}
	if b < c {
		return b
	}
	return c
}
func absf(x float64) float64 {
	if x < 0 {
		return -x
	}
	return x
}

func isAllDigits(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}
func isAllUpper(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < 'A' || s[i] > 'Z' {
			return false
		}
	}
	return true
}
func monToNum(m string) (int, bool) {
	switch strings.ToUpper(m) {
	case "JAN":
		return 1, true
	case "FEB":
		return 2, true
	case "MAR":
		return 3, true
	case "APR":
		return 4, true
	case "MAY":
		return 5, true
	case "JUN":
		return 6, true
	case "JUL":
		return 7, true
	case "AUG":
		return 8, true
	case "SEP":
		return 9, true
	case "OCT":
		return 10, true
	case "NOV":
		return 11, true
	case "DEC":
		return 12, true
	}
	return 0, false
}
func atoi(s string) int {
	n := 0
	for i := 0; i < len(s); i++ {
		n = n*10 + int(s[i]-'0')
	}
	return n
}
func atoi2(s string) int { // 2자리 정수
	return int(s[0]-'0')*10 + int(s[1]-'0')
}

// Supports many format of options name:
//   - "2006-01-02"
//   - "20060102", "YYMMDD"(예: "250905")
//   - "5SEP25", "30AUG24", "30AUG2024"
func parseExpiryTimeUTC(s string, _ time.Time) (time.Time, bool) {
	s = strings.TrimSpace(strings.ToUpper(s))
	if s == "" {
		return time.Time{}, false
	}

	// YYYY-MM-DD
	if len(s) == 10 && s[4] == '-' && s[7] == '-' {
		t, err := time.ParseInLocation("2006-01-02", s, time.UTC)
		return t, err == nil
	}
	// YYYYMMDD
	if len(s) == 8 && isAllDigits(s) {
		t, err := time.ParseInLocation("20060102", s, time.UTC)
		return t, err == nil
	}
	// YYMMDD
	if len(s) == 6 && isAllDigits(s) {
		yy := "20" + s[:2]
		t, err := time.ParseInLocation("20060102", yy+s[2:], time.UTC)
		return t, err == nil
	}
	// Deribit format: 5SEP25 / 30AUG24
	if len(s) >= 6 && len(s) <= 7 {
		dayLen := len(s) - 5
		day := s[:dayLen]
		mon := s[dayLen : dayLen+3]
		yy := s[len(s)-2:]
		if isAllDigits(day) && isAllUpper(mon) && isAllDigits(yy) {
			if m, ok := monToNum(mon); ok {
				year := 2000 + atoi2(yy)
				dd := atoi(day)
				return time.Date(year, time.Month(m), dd, 0, 0, 0, 0, time.UTC), true
			}
		}
	}
	// 30AUG2024 (4-digit year)
	if len(s) == 9 || len(s) == 8 {
		dayLen := len(s) - 7
		day := s[:dayLen]
		mon := s[dayLen : dayLen+3]
		yyyy := s[len(s)-4:]
		if isAllDigits(day) && isAllUpper(mon) && isAllDigits(yyyy) {
			if m, ok := monToNum(mon); ok {
				year := atoi(yyyy)
				dd := atoi(day)
				return time.Date(year, time.Month(m), dd, 0, 0, 0, 0, time.UTC), true
			}
		}
	}
	return time.Time{}, false
}
