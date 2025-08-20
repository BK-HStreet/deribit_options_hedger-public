package strategy

import (
	"Options_Hedger/internal/data"
	"Options_Hedger/internal/notify"
	"strconv"
	"strings"
	"sync/atomic"
)

const (
	BoxLong  int8 = +1
	BoxShort int8 = -1
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
	Profit       float64 // USD profit floor (worst-case)
	UpdateTimeNs int64
	Side         int8 // +1: Long Box, -1: Short Box
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
		useBandCheck:  false,  // true=동적밴드를 위아래 1% 사용하겠다는 의미
		smin:          0,      // 0 → index 기반 fallback 사용
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

	// ----- strike order (avoid math.Min/Max) -----
	lowStrike := opt1.Strike
	highStrike := opt2.Strike
	if lowStrike > highStrike {
		lowStrike, highStrike = highStrike, lowStrike
	}
	// 최소 스트라이크 폭
	if highStrike-lowStrike < e.minStrikeGap {
		return
	}

	// ----- dedup mask (coarse) -----
	hash := uint64(lowStrike)*1000 + uint64(highStrike)
	bit := hash & 63
	if (e.recentSignals>>bit)&1 == 1 {
		return
	}
	atomic.OrUint64(&e.recentSignals, 1<<bit)

	// ----- find 4 legs (same expiry) -----
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
					lcIdx = int16(i) // low strike call
				}
			} else {
				if lpIdx == -1 {
					lpIdx = int16(i) // low strike put
				}
			}
		} else if s == highStrike {
			if o.IsCall {
				if hcIdx == -1 {
					hcIdx = int16(i) // high strike call
				}
			} else {
				if hpIdx == -1 {
					hpIdx = int16(i) // high strike put
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

	// ----- top-of-book snapshot -----
	lc := data.ReadDepthFast(int(lcIdx)) // C(K_low)
	lp := data.ReadDepthFast(int(lpIdx)) // P(K_low)
	hc := data.ReadDepthFast(int(hcIdx)) // C(K_high)
	hp := data.ReadDepthFast(int(hpIdx)) // P(K_high)

	// sanity checks
	if lc.AskQty <= 0 || lp.AskQty <= 0 || hc.AskQty <= 0 || hp.AskQty <= 0 {
		// need both ask & bid across legs; continue checks below
	}
	if lc.BidQty <= 0 || lp.BidQty <= 0 || hc.BidQty <= 0 || hp.BidQty <= 0 {
		// same
	}
	if lc.AskPrice <= 0 || lp.AskPrice <= 0 || hc.AskPrice <= 0 || hp.AskPrice <= 0 {
		return
	}
	if lc.BidPrice <= 0 || lp.BidPrice <= 0 || hc.BidPrice <= 0 || hp.BidPrice <= 0 {
		return
	}
	if indexPrice <= 0 {
		return
	}

	// ----- executable qty Q for BOTH patterns -----
	Qlong := lc.AskQty // +C_low @ask
	if hp.AskQty < Qlong {
		Qlong = hp.AskQty // +P_high @ask
	}
	if lp.BidQty < Qlong {
		Qlong = lp.BidQty // -P_low  @bid
	}
	if hc.BidQty < Qlong {
		Qlong = hc.BidQty // -C_high @bid
	}
	if Qlong <= 0 {
		Qlong = 0
	}
	Qshort := hc.AskQty // +C_high @ask
	if lp.AskQty < Qshort {
		Qshort = lp.AskQty // +P_low  @ask
	}
	if lc.BidQty < Qshort {
		Qshort = lc.BidQty // -C_low  @bid
	}
	if hp.BidQty < Qshort {
		Qshort = hp.BidQty // -P_high @bid
	}
	if Qshort <= 0 {
		Qshort = 0
	}

	// apply global maxQty limit
	if m := e.maxQty; m > 0 {
		if Qlong > m {
			Qlong = m
		}
		if Qshort > m {
			Qshort = m
		}
	}
	// nothing executable
	if Qlong <= 0 && Qshort <= 0 {
		return
	}

	// ----- dynamic band for fee-rate notional -----
	var Smin, Smax float64
	if e.useBandCheck {
		Smin, Smax = e.smin, e.smax
		if Smin <= 0 || Smax <= 0 || Smax < Smin {
			Smin = indexPrice * 0.9
			Smax = indexPrice * 1.1
		}
	} else {
		Smin, Smax = indexPrice, indexPrice
	}

	// ----- fixed leg payoff (USD) -----
	fixedUSD := (highStrike - lowStrike)

	// helper inline abs
	abs := func(x float64) float64 {
		if x < 0 {
			return -x
		}
		return x
	}

	emit := func(side int8, q float64, profitFloorUSD float64) {
		if q <= 0 {
			return
		}
		// flatness filter already applied outside
		if profitFloorUSD < e.minProfitUSD {
			return
		}
		sig := BoxSignal{
			LowCallIdx:   lcIdx,
			LowPutIdx:    lpIdx,
			HighCallIdx:  hcIdx,
			HighPutIdx:   hpIdx,
			LowStrike:    lowStrike,
			HighStrike:   highStrike,
			Profit:       profitFloorUSD,
			UpdateTimeNs: data.Nanotime(),
			Side:         side,
		}
		select {
		case e.signals <- sig:
		default:
		}
	}

	// ===== LONG BOX =====
	if Qlong > 0 {
		// cashflow in BTC (per 1 qty), pay asks and receive bids
		// +C_low@ask, +P_high@ask, -P_low@bid, -C_high@bid
		netBTC_long := (lc.AskPrice + hp.AskPrice - lp.BidPrice - hc.BidPrice) * Qlong

		// worst-case S selection
		SstarL := Smax
		if netBTC_long < 0 {
			SstarL = Smin
		}

		// fee (USD)
		feesL := (e.feePerLegUSD * 4.0 * Qlong)
		if e.feePerLegRate > 0 {
			feesL += (e.feePerLegRate * SstarL * 4.0 * Qlong)
		}

		// worst-case profit floor: PnL(S) = (K2-K1)*Q - netBTC*S - fees
		profitFloorL := fixedUSD*Qlong - netBTC_long*SstarL - feesL

		// flatness filter: |netBTC|/Q <= flatnessMax
		if fmax := e.flatnessMax; fmax > 0 {
			if (abs(netBTC_long) / Qlong) > fmax {
				// do not emit
			} else {
				if profitFloorL >= e.minProfitUSD {
					emit(BoxLong, Qlong, profitFloorL)
				}
			}
		} else {
			if profitFloorL >= e.minProfitUSD {
				emit(BoxLong, Qlong, profitFloorL)
			}
		}
	}

	// ===== SHORT BOX =====
	if Qshort > 0 {
		// cashflow in BTC (per 1 qty), pay asks and receive bids
		// +C_high@ask, +P_low@ask, -C_low@bid, -P_high@bid
		netBTC_short := (hc.AskPrice + lp.AskPrice - lc.BidPrice - hp.BidPrice) * Qshort

		// worst-case S selection
		SstarS := Smax
		if netBTC_short < 0 {
			SstarS = Smin
		}

		// fee (USD)
		feesS := (e.feePerLegUSD * 4.0 * Qshort)
		if e.feePerLegRate > 0 {
			feesS += (e.feePerLegRate * SstarS * 4.0 * Qshort)
		}

		// worst-case profit floor: PnL(S) = -(K2-K1)*Q - netBTC*S - fees
		profitFloorS := -fixedUSD*Qshort - netBTC_short*SstarS - feesS

		// flatness filter
		if fmax := e.flatnessMax; fmax > 0 {
			if (abs(netBTC_short) / Qshort) > fmax {
				// do not emit
			} else {
				if profitFloorS >= e.minProfitUSD {
					emit(BoxShort, Qshort, profitFloorS)
				}
			}
		} else {
			if profitFloorS >= e.minProfitUSD {
				emit(BoxShort, Qshort, profitFloorS)
			}
		}
	}
}

// 외부에서 시그널 수신
func (e *BoxSpreadHFT) Signals() <-chan BoxSignal { return e.signals }

// 비트마스크 리셋 (주기적으로 호출)
func (e *BoxSpreadHFT) ResetSignalMask() { atomic.StoreUint64(&e.recentSignals, 0) }
