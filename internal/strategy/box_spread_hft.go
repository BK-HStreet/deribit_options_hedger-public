// package strategy

// import (
// 	"Options_Hedger/internal/data"
// 	"Options_Hedger/internal/notify"
// 	"math"
// 	"strconv"
// 	"strings"
// 	"sync/atomic"
// )

// // ✅ 메모리 효율적인 옵션 정보 (32바이트)
// type OptionInfo struct {
// 	Strike float64  // 8
// 	Expiry uint16   // 2 (인덱스화)
// 	Index  int16    // 2
// 	IsCall bool     // 1
// 	_      [19]byte // 패딩
// }

// // ✅ 시그널 구조체 최적화
// type BoxSignal struct {
// 	LowCallIdx   int16
// 	LowPutIdx    int16
// 	HighCallIdx  int16
// 	HighPutIdx   int16
// 	LowStrike    float64
// 	HighStrike   float64
// 	Profit       float64
// 	UpdateTimeNs int64
// }

// type BoxSpreadHFT struct {
// 	updates chan data.Update
// 	signals chan BoxSignal

// 	// ✅ 캐시 최적화된 데이터 구조
// 	options     [data.MaxOptions]OptionInfo
// 	optionCount int32

// 	// ✅ 빠른 룩업 테이블 (컴파일 타임 최적화)
// 	expiryMap  [16]uint16                             // 만료일 -> 인덱스
// 	strikeMap  [data.MaxOptions]float64               // 인덱스 -> 행사가
// 	pairLookup [data.MaxOptions][data.MaxOptions]bool // 매치 가능한 페어

// 	// ✅ 중복 제거 (비트마스크)
// 	recentSignals uint64 // 최근 64개 신호 비트마스크
// 	lastCheck     int64  // 마지막 체크 시간
// 	notifier      notify.Notifier
// }

// func NewBoxSpreadHFT(ch chan data.Update) *BoxSpreadHFT {
// 	return &BoxSpreadHFT{
// 		updates: ch,
// 		signals: make(chan BoxSignal, 128), // 버퍼 크기 줄임
// 	}
// }

// func (e *BoxSpreadHFT) SetNotifier(n notify.Notifier) { e.notifier = n }

// //go:noinline
// func (e *BoxSpreadHFT) InitializeHFT(symbols []string) {
// 	count := len(symbols)
// 	if count > data.MaxOptions {
// 		count = data.MaxOptions
// 	}

// 	expiryIndex := make(map[string]uint16)
// 	var expiryCounter uint16

// 	for i := 0; i < count; i++ {
// 		parts := strings.Split(symbols[i], "-")
// 		if len(parts) != 4 {
// 			continue
// 		}

// 		strike, err := strconv.ParseFloat(parts[2], 64)
// 		if err != nil {
// 			continue
// 		}

// 		expiry := parts[1]
// 		if _, exists := expiryIndex[expiry]; !exists {
// 			expiryIndex[expiry] = expiryCounter
// 			e.expiryMap[expiryCounter] = expiryCounter
// 			expiryCounter++
// 		}

// 		e.options[i] = OptionInfo{
// 			Strike: strike,
// 			Expiry: expiryIndex[expiry],
// 			Index:  int16(i),
// 			IsCall: parts[3] == "C",
// 		}
// 		e.strikeMap[i] = strike
// 	}

// 	e.optionCount = int32(count)
// 	e.buildPairLookup()
// }

// //go:noinline
// func (e *BoxSpreadHFT) buildPairLookup() {
// 	count := int(e.optionCount)
// 	for i := 0; i < count; i++ {
// 		for j := i + 1; j < count; j++ {
// 			opt1 := &e.options[i]
// 			opt2 := &e.options[j]

// 			// 같은 만료일이고 다른 행사가인 경우만 매치
// 			if opt1.Expiry == opt2.Expiry && opt1.Strike != opt2.Strike {
// 				e.pairLookup[i][j] = true
// 				e.pairLookup[j][i] = true
// 			}
// 		}
// 	}
// }

// //go:noinline
// func (e *BoxSpreadHFT) Run() {
// 	for update := range e.updates {
// 		e.processUpdateHFT(update)
// 	}
// 	// var update data.Update

// 	// for {
// 	// 	select {
// 	// 	case update = <-e.updates:
// 	// 		e.processUpdateHFT(update)
// 	// 	default:
// 	// 		// 논블로킹으로 계속 처리
// 	// 	}
// 	// }
// }

// //go:noinline
// func (e *BoxSpreadHFT) processUpdateHFT(update data.Update) {
// 	idx := int(update.SymbolIdx)
// 	if idx >= int(e.optionCount) {
// 		return
// 	}

// 	// updatedOpt := &e.options[idx]
// 	currentTime := update.UpdateTime

// 	// ✅ 중복 체크 최적화 (10μs 이내 무시)
// 	if currentTime-e.lastCheck < 10000 { // 10μs
// 		return
// 	}
// 	e.lastCheck = currentTime

// 	// ✅ 페어 룩업 테이블 사용으로 O(1) 검색
// 	count := int(e.optionCount)
// 	for i := range count {
// 		if !e.pairLookup[idx][i] {
// 			continue
// 		}

// 		e.checkBoxFast(idx, i, update.IndexPrice)
// 	}
// }

// //go:noinline
// func (e *BoxSpreadHFT) checkBoxFast(idx1, idx2 int, indexPrice float64) {
// 	opt1 := &e.options[idx1]
// 	opt2 := &e.options[idx2]

// 	lowStrike := math.Min(opt1.Strike, opt2.Strike)
// 	highStrike := math.Max(opt1.Strike, opt2.Strike)

// 	if highStrike-lowStrike < 1000 { // 최소 $1000 차이
// 		return
// 	}

// 	// ✅ 해시 기반 중복 제거 (비트 연산)
// 	hash := uint64(lowStrike)*1000 + uint64(highStrike)
// 	bit := hash % 64
// 	if (e.recentSignals>>bit)&1 == 1 {
// 		return
// 	}
// 	atomic.OrUint64(&e.recentSignals, 1<<bit)

// 	// ✅ 4개 옵션 빠른 검색
// 	var lowCallIdx, lowPutIdx, highCallIdx, highPutIdx int16 = -1, -1, -1, -1

// 	count := int(e.optionCount)
// 	for i := 0; i < count; i++ {
// 		opt := &e.options[i]
// 		if opt.Expiry != opt1.Expiry {
// 			continue
// 		}

// 		if opt.Strike == lowStrike {
// 			if opt.IsCall && lowCallIdx == -1 {
// 				lowCallIdx = int16(i)
// 			} else if !opt.IsCall && lowPutIdx == -1 {
// 				lowPutIdx = int16(i)
// 			}
// 		} else if opt.Strike == highStrike {
// 			if opt.IsCall && highCallIdx == -1 {
// 				highCallIdx = int16(i)
// 			} else if !opt.IsCall && highPutIdx == -1 {
// 				highPutIdx = int16(i)
// 			}
// 		}

// 		// 조기 종료
// 		if lowCallIdx != -1 && lowPutIdx != -1 && highCallIdx != -1 && highPutIdx != -1 {
// 			break
// 		}
// 	}

// 	if lowCallIdx == -1 || lowPutIdx == -1 || highCallIdx == -1 || highPutIdx == -1 {
// 		return
// 	}

// 	// ✅ 인라인 수익성 계산
// 	lowCall := data.ReadDepthFast(int(lowCallIdx))
// 	lowPut := data.ReadDepthFast(int(lowPutIdx))
// 	highCall := data.ReadDepthFast(int(highCallIdx))
// 	highPut := data.ReadDepthFast(int(highPutIdx))

// 	// 수량 유효성 체크
// 	if lowCall.AskQty <= 0 || lowPut.BidQty <= 0 ||
// 		highCall.BidQty <= 0 || highPut.AskQty <= 0 {
// 		return
// 	}
// 	// 가격 유효성 체크
// 	if lowCall.AskPrice <= 0 || lowPut.BidPrice <= 0 ||
// 		highCall.BidPrice <= 0 || highPut.AskPrice <= 0 || indexPrice <= 0 {
// 		return
// 	}

// 	netCost := (lowCall.AskPrice + highPut.AskPrice - lowPut.BidPrice - highCall.BidPrice) * indexPrice
// 	expectedValue := highStrike - lowStrike
// 	profit := expectedValue - netCost

// 	if profit > 1.0 { // $5 최소 수익

// 		signal := BoxSignal{
// 			LowCallIdx:   lowCallIdx,
// 			LowPutIdx:    lowPutIdx,
// 			HighCallIdx:  highCallIdx,
// 			HighPutIdx:   highPutIdx,
// 			LowStrike:    lowStrike,
// 			HighStrike:   highStrike,
// 			Profit:       profit,
// 			UpdateTimeNs: data.Nanotime(),
// 		}

// 		// 논블로킹 전송
// 		select {
// 		case e.signals <- signal:
// 		default:
// 		}
// 	}
// }

// func (e *BoxSpreadHFT) Signals() <-chan BoxSignal {
// 	return e.signals
// }

// // ✅ 주기적으로 비트마스크 클리어 (별도 고루틴에서)
//
//	func (e *BoxSpreadHFT) ResetSignalMask() {
//		atomic.StoreUint64(&e.recentSignals, 0)
//	}

package strategy

import (
	"Options_Hedger/internal/data"
	"Options_Hedger/internal/notify"
	"strconv"
	"strings"
	"sync/atomic"
)

// ✅ 메모리 효율적인 옵션 정보 (32바이트)
type OptionInfo struct {
	Strike float64 // 8
	Expiry uint16  // 2 (인덱스화)
	Index  int16   // 2
	IsCall bool    // 1
	_      [19]byte
}

// ✅ 시그널 구조체 (HFT 최소 필드)
type BoxSignal struct {
	LowCallIdx   int16
	LowPutIdx    int16
	HighCallIdx  int16
	HighPutIdx   int16
	LowStrike    float64
	HighStrike   float64
	Profit       float64 // 채택 기준으로 사용한 USD profit (floor or now)
	UpdateTimeNs int64
}

type BoxSpreadHFT struct {
	updates chan data.Update
	signals chan BoxSignal

	// ✅ 캐시 최적화된 데이터 구조
	options     [data.MaxOptions]OptionInfo
	optionCount int32

	// ✅ 빠른 룩업 테이블
	expiryMap  [16]uint16
	strikeMap  [data.MaxOptions]float64
	pairLookup [data.MaxOptions][data.MaxOptions]bool

	// ✅ 중복 제거 (비트마스크)
	recentSignals uint64
	lastCheck     int64
	notifier      notify.Notifier

	// ✅ 런타임 파라미터
	minStrikeGap float64 // 최소 행사가 간격 (USD)
	debounceNs   int64   // 디바운스 윈도우 (ns)
	minProfitUSD float64 // ≥ $1
	flatnessMax  float64 // |netBTC|/Q 한도 (0=off)
	maxQty       float64 // 체결 상한 (0=무제한)

	// 수수료: (둘 다 지원) — 퍼센트가 설정되면 퍼센트 방식 우선
	feePerLegUSD  float64 // USD per 1BTC·leg
	feePerLegRate float64 // 퍼센트(예: 0.0001 = 0.01%) per 1BTC·leg·notional

	// 밴드 체크
	useBandCheck bool
	smin         float64 // 고정 밴드 하한(USD/BTC). 0이면 fallback 사용
	smax         float64 // 고정 밴드 상한(USD/BTC). 0이면 fallback 사용
}

// 사용자 설정 적용: (1) MinProfitUSD=1, (2) fee=0.01%(per-leg), (3) flatness=0.02,
// (4) 밴드 보장 사용, (5) maxQty=0, (6) minStrikeGap=1000, (7) debounce=10000ns, (8) signals=128, (9) reset 기본
func NewBoxSpreadHFT(ch chan data.Update) *BoxSpreadHFT {
	return &BoxSpreadHFT{
		updates:       ch,
		signals:       make(chan BoxSignal, 128),
		minStrikeGap:  1000,
		debounceNs:    10000, // 10µs
		minProfitUSD:  1.0,
		flatnessMax:   0.02,
		maxQty:        0,
		feePerLegUSD:  0.0,
		feePerLegRate: 0.0001, // 0.01%
		useBandCheck:  true,
		smin:          0, // 0 → index 기반 fallback 사용
		smax:          0,
	}
}

func (e *BoxSpreadHFT) SetNotifier(n notify.Notifier) { e.notifier = n }

//go:noinline
func (e *BoxSpreadHFT) InitializeHFT(symbols []string) {
	count := len(symbols)
	if count > data.MaxOptions {
		count = data.MaxOptions
	}

	expiryIndex := make(map[string]uint16)
	var expiryCounter uint16

	for i := 0; i < count; i++ {
		parts := strings.Split(symbols[i], "-")
		if len(parts) != 4 {
			continue
		}
		strike, err := strconv.ParseFloat(parts[2], 64)
		if err != nil {
			continue
		}
		expiry := parts[1]
		if _, ok := expiryIndex[expiry]; !ok {
			expiryIndex[expiry] = expiryCounter
			e.expiryMap[expiryCounter] = expiryCounter
			expiryCounter++
		}
		e.options[i] = OptionInfo{
			Strike: strike,
			Expiry: expiryIndex[expiry],
			Index:  int16(i),
			IsCall: parts[3] == "C",
		}
		e.strikeMap[i] = strike
	}
	e.optionCount = int32(count)
	e.buildPairLookup()
}

//go:noinline
func (e *BoxSpreadHFT) buildPairLookup() {
	count := int(e.optionCount)
	for i := 0; i < count; i++ {
		oi := &e.options[i]
		for j := i + 1; j < count; j++ {
			oj := &e.options[j]
			if oi.Expiry == oj.Expiry && oi.Strike != oj.Strike {
				e.pairLookup[i][j] = true
				e.pairLookup[j][i] = true
			}
		}
	}
}

//go:noinline
func (e *BoxSpreadHFT) Run() {
	for update := range e.updates {
		e.processUpdateHFT(update)
	}
}

//go:noinline
func (e *BoxSpreadHFT) processUpdateHFT(update data.Update) {
	idx := int(update.SymbolIdx)
	if idx >= int(e.optionCount) {
		return
	}
	// 디바운스
	now := update.UpdateTime
	if now-e.lastCheck < e.debounceNs {
		return
	}
	e.lastCheck = now

	count := int(e.optionCount)
	for i := 0; i < count; i++ {
		if !e.pairLookup[idx][i] {
			continue
		}
		e.checkBoxFast(idx, i, update.IndexPrice)
	}
}

//go:noinline
func (e *BoxSpreadHFT) checkBoxFast(idx1, idx2 int, indexPrice float64) {
	opt1 := &e.options[idx1]
	opt2 := &e.options[idx2]

	// 분기 min/max (math.Min/Max 회피)
	lowStrike := opt1.Strike
	highStrike := opt2.Strike
	if lowStrike > highStrike {
		lowStrike, highStrike = highStrike, lowStrike
	}
	// 최소 스트라이크 폭
	if highStrike-lowStrike < e.minStrikeGap {
		return
	}

	// 저비용 해시 중복 제거
	hash := uint64(lowStrike)*1000 + uint64(highStrike)
	bit := hash & 63
	if (e.recentSignals>>bit)&1 == 1 {
		return
	}
	atomic.OrUint64(&e.recentSignals, 1<<bit)

	// 동일 만기에서 4 leg 찾기 (단일 패스)
	expiry := opt1.Expiry
	var lcIdx, lpIdx, hcIdx, hpIdx int16 = -1, -1, -1, -1
	n := int(e.optionCount)
	for i := 0; i < n; i++ {
		o := &e.options[i]
		if o.Expiry != expiry {
			continue
		}
		s := o.Strike
		if s == lowStrike {
			if o.IsCall {
				if lcIdx == -1 {
					lcIdx = int16(i)
				}
			} else {
				if lpIdx == -1 {
					lpIdx = int16(i)
				}
			}
		} else if s == highStrike {
			if o.IsCall {
				if hcIdx == -1 {
					hcIdx = int16(i)
				}
			} else {
				if hpIdx == -1 {
					hpIdx = int16(i)
				}
			}
		}
		if lcIdx != -1 && lpIdx != -1 && hcIdx != -1 && hpIdx != -1 {
			break
		}
	}
	if lcIdx == -1 || lpIdx == -1 || hcIdx == -1 || hpIdx == -1 {
		return
	}

	// top-of-book 스냅샷
	lc := data.ReadDepthFast(int(lcIdx)) // +C(K_low)  buy@ask
	lp := data.ReadDepthFast(int(lpIdx)) // -P(K_low)  sell@bid
	hc := data.ReadDepthFast(int(hcIdx)) // -C(K_high) sell@bid
	hp := data.ReadDepthFast(int(hpIdx)) // +P(K_high) buy@ask

	// 교차 가능/수량 체크
	if lc.AskQty <= 0 || lp.BidQty <= 0 || hc.BidQty <= 0 || hp.AskQty <= 0 {
		return
	}
	if lc.AskPrice <= 0 || lp.BidPrice <= 0 || hc.BidPrice <= 0 || hp.AskPrice <= 0 {
		return
	}
	if indexPrice <= 0 {
		return
	}

	// 실행 수량 Q (min qty)
	Q := lc.AskQty
	if lp.BidQty < Q {
		Q = lp.BidQty
	}
	if hc.BidQty < Q {
		Q = hc.BidQty
	}
	if hp.AskQty < Q {
		Q = hp.AskQty
	}
	if Q <= 0 {
		return
	}
	if m := e.maxQty; m > 0 && Q > m {
		Q = m
	}

	// 경제성 (BTC 정산)
	// netBTC = (매수 ask 합 - 매도 bid 합) * Q
	netBTC := (lc.AskPrice + hp.AskPrice - lp.BidPrice - hc.BidPrice) * Q
	fixedUSD := (highStrike - lowStrike) * Q
	if fixedUSD <= 0 {
		return
	}

	// ---- 수수료/슬리피지 (USD) ----
	// 퍼센트 수수료가 설정되어 있으면 우선 적용: rate * S* * 4 * Q
	// 밴드 미설정 시 S*는 indexPrice 기반 fallback.
	var Smin, Smax float64
	if e.useBandCheck {
		Smin, Smax = e.smin, e.smax
		if Smin <= 0 || Smax <= 0 || Smax < Smin {
			// 동적 fallback 밴드(±10%)
			Smin = indexPrice * 0.9
			Smax = indexPrice * 1.1
		}
	} else {
		Smin, Smax = indexPrice, indexPrice
	}

	// 최악점 선택: dPnL/dS = -netBTC
	Sstar := Smax
	if netBTC < 0 {
		Sstar = Smin
	}

	feesUSD := (e.feePerLegUSD * 4.0 * Q)
	if e.feePerLegRate > 0 {
		feesUSD += (e.feePerLegRate * Sstar * 4.0 * Q)
	}

	// ---- PnL 검사 (worst-case) ----
	// PnL(S) = fixedUSD - netBTC*S - feesUSD
	profitFloorUSD := fixedUSD - netBTC*Sstar - feesUSD

	// 평탄도 필터: F = |netBTC|/Q
	if fmax := e.flatnessMax; fmax > 0 {
		abs := netBTC
		if abs < 0 {
			abs = -abs
		}
		if (abs / Q) > fmax {
			return
		}
	}

	minProfit := e.minProfitUSD
	if profitFloorUSD < minProfit {
		return
	}

	// 시그널 전송 (논블로킹)
	sig := BoxSignal{
		LowCallIdx:   lcIdx,
		LowPutIdx:    lpIdx,
		HighCallIdx:  hcIdx,
		HighPutIdx:   hpIdx,
		LowStrike:    lowStrike,
		HighStrike:   highStrike,
		Profit:       profitFloorUSD,
		UpdateTimeNs: data.Nanotime(),
	}
	select {
	case e.signals <- sig:
	default:
	}
}

// 외부에서 시그널 수신
func (e *BoxSpreadHFT) Signals() <-chan BoxSignal { return e.signals }

// 비트마스크 리셋 (주기적으로 호출)
func (e *BoxSpreadHFT) ResetSignalMask() { atomic.StoreUint64(&e.recentSignals, 0) }
