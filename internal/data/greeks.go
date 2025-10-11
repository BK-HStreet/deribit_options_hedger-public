// File: internal/data/greeks.go
package data

import "sync/atomic"

// 옵션 1계약당 그릭스 (USD/일 단위: Theta 등)
type Greeks struct {
	Delta float64
	Gamma float64
	Theta float64
	Vega  float64
	Rho   float64
	TsMs  int64 // 수신 시각(ms)
}

var greeksTab [MaxOptions]atomic.Value // 각 인덱스에 최신 그릭스 저장

// 빠른 쓰기 (WS 수신 루틴에서 호출)
func WriteGreeksFast(idx int, g Greeks) {
	if idx < 0 || idx >= len(greeksTab) {
		return
	}
	greeksTab[idx].Store(g)
}

// 빠른 읽기 (전략에서 호출)
func ReadGreeksFast(idx int) (Greeks, bool) {
	if idx < 0 || idx >= len(greeksTab) {
		return Greeks{}, false
	}
	v := greeksTab[idx].Load()
	if v == nil {
		return Greeks{}, false
	}
	return v.(Greeks), true
}
