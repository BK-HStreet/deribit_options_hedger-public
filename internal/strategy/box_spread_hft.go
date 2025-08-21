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

// OptionInfo: compact option metadata (32 bytes)
type OptionInfo struct {
	Strike float64 // 8
	Expiry uint16  // 2 (indexed by map)
	Index  int16   // 2
	IsCall bool    // 1
	_      [19]byte
}

// BoxSignal: minimal HFT signal payload
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

	// Cache-friendly option table
	options     [data.MaxOptions]OptionInfo
	optionCount int32

	// Fast lookups
	expiryMap  [16]uint16
	strikeMap  [data.MaxOptions]float64
	pairLookup [data.MaxOptions][data.MaxOptions]bool

	// Dedup & runtime state
	recentSignals uint64
	lastCheck     int64
	notifier      notify.Notifier

	// Runtime params
	minStrikeGap float64 // min strike distance (USD)
	debounceNs   int64   // debounce window (ns)
	minProfitUSD float64 // profit floor threshold (USD)
	flatnessMax  float64 // legacy symmetric flatness cap: |netBTC|/Q (0=off)
	maxQty       float64 // max executable qty cap (0=unlimited)

	// Fees per 1 BTC notional per leg (either fixed USD or percent). If percent>0, it takes precedence.
	feePerLegUSD  float64 // fixed fee per leg in USD
	feePerLegRate float64 // percentage per leg on notional (e.g., 0.0001 = 0.01%)

	// Worst-case price band for S* selection
	useBandCheck bool    // if true, use [smin,smax] or fallback ±bandPct
	smin         float64 // user-fixed lower bound for S* (USD/BTC). If <=0, fallback is used
	smax         float64 // user-fixed upper bound for S* (USD/BTC). If <=0, fallback is used
	bandPct      float64 // fallback ±band fraction around indexPrice (e.g., 0.10 = ±10%)

	// Directional flatness gate (optional). Works on slope := dPnL/dS per 1 qty = -netBTC/Q.
	useDirFlatness bool    // true: enforce directional flatness band; false: legacy symmetric rules
	favorSlope     int8    // -1: favor down (price↓ beneficial), 0: neutral, +1: favor up (price↑ beneficial)
	flatnessMinBTC float64 // min required |slope| in BTC per 1 qty (0 = no lower bound)
	flatnessMaxBTC float64 // max allowed |slope| in BTC per 1 qty for the favorable side (0 = no upper bound)
}

// NewBoxSpreadHFT applies sensible defaults for HFT usage.
// Defaults: minProfit=1 USD, feeRate=0.01% per leg, flatnessMax=0.02 BTC/qty (legacy),
// band disabled by default; if enabled, fallback ±10%; debounce=10µs; minStrikeGap=1000 USD.
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
		useBandCheck:  false,
		smin:          0,
		smax:          0,
		bandPct:       0.10, // ±10% fallback if band check enabled and smin/smax unset

		useDirFlatness: false,
		favorSlope:     0,
		flatnessMinBTC: 0.0,
		flatnessMaxBTC: 0.0,
	}
}

func (e *BoxSpreadHFT) SetNotifier(n notify.Notifier) { e.notifier = n }

// InitializeHFT ingests the pre-selected symbols universe for detection.
// Expected symbol format: UNDERLYING-EXPIRY-STRIKE-C|P (e.g., BTC-15AUG25-116000-C)
// Expiries are indexed to compact OptionInfo entries.
//
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

// buildPairLookup precomputes eligible pairs by same expiry and different strikes.
//
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

// Run consumes updates and triggers detection.
//
//go:noinline
func (e *BoxSpreadHFT) Run() {
	for update := range e.updates {
		e.processUpdateHFT(update)
	}
}

// processUpdateHFT debounces and checks pairs related to the updated symbol.
//
//go:noinline
func (e *BoxSpreadHFT) processUpdateHFT(update data.Update) {
	idx := int(update.SymbolIdx)
	if idx >= int(e.optionCount) {
		return
	}
	// Debounce
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

// passFlatnessDirectional applies either legacy symmetric flatness or directional band on slope.
// slope := dPnL/dS per 1 qty = -netBTC/Q.
func (e *BoxSpreadHFT) passFlatnessDirectional(slope float64) bool {
	// Helper abs without importing math
	abs := slope
	if abs < 0 {
		abs = -abs
	}

	if !e.useDirFlatness {
		// Legacy symmetric rules (backward-compatible)
		if e.flatnessMinBTC > 0 && abs < e.flatnessMinBTC {
			return false
		}
		upper := e.flatnessMaxBTC
		if upper <= 0 {
			upper = e.flatnessMax // fallback to legacy cap if specific max not set
		}
		if upper > 0 && abs > upper {
			return false
		}
		return true
	}

	// Directional band: favorSlope enforces the sign and [min, max] on the favorable side
	switch {
	case e.favorSlope > 0: // favor up → slope must be positive
		if slope <= 0 {
			return false
		}
		if e.flatnessMinBTC > 0 && slope < e.flatnessMinBTC {
			return false
		}
		if e.flatnessMaxBTC > 0 && slope > e.flatnessMaxBTC {
			return false
		}
		return true
	case e.favorSlope < 0: // favor down → slope must be negative
		neg := -slope
		if neg <= 0 { // slope >= 0
			return false
		}
		if e.flatnessMinBTC > 0 && neg < e.flatnessMinBTC {
			return false
		}
		if e.flatnessMaxBTC > 0 && neg > e.flatnessMaxBTC {
			return false
		}
		return true
	default: // neutral but using [min,max] if provided
		if e.flatnessMinBTC > 0 && abs < e.flatnessMinBTC {
			return false
		}
		if e.flatnessMaxBTC > 0 && abs > e.flatnessMaxBTC {
			return false
		}
		return true
	}
}

// checkBoxFast evaluates both Long Box and Short Box for a given strike pair (same expiry).
// It emits signals when worst-case profit floor exceeds minProfitUSD and flatness gates pass.
//
//go:noinline
func (e *BoxSpreadHFT) checkBoxFast(idx1, idx2 int, indexPrice float64) {
	opt1 := &e.options[idx1]
	opt2 := &e.options[idx2]

	// Strike ordering (branch-only, avoids math.Min/Max)
	lowStrike := opt1.Strike
	highStrike := opt2.Strike
	if lowStrike > highStrike {
		lowStrike, highStrike = highStrike, lowStrike
	}
	// Enforce min strike gap
	if highStrike-lowStrike < e.minStrikeGap {
		return
	}

	// Coarse dedup on (lowStrike, highStrike)
	hash := uint64(lowStrike)*1000 + uint64(highStrike)
	bit := hash & 63
	if (e.recentSignals>>bit)&1 == 1 {
		return
	}
	atomic.OrUint64(&e.recentSignals, 1<<bit)

	// Find 4 legs at the same expiry
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

	// Top-of-book snapshot
	lc := data.ReadDepthFast(int(lcIdx)) // C(K_low)
	lp := data.ReadDepthFast(int(lpIdx)) // P(K_low)
	hc := data.ReadDepthFast(int(hcIdx)) // C(K_high)
	hp := data.ReadDepthFast(int(hpIdx)) // P(K_high)

	// Sanity checks
	if lc.AskPrice <= 0 || lp.AskPrice <= 0 || hc.AskPrice <= 0 || hp.AskPrice <= 0 {
		return
	}
	if lc.BidPrice <= 0 || lp.BidPrice <= 0 || hc.BidPrice <= 0 || hp.BidPrice <= 0 {
		return
	}
	if indexPrice <= 0 {
		return
	}

	// Executable qty for LONG BOX pattern (+C_low@ask, +P_high@ask, -P_low@bid, -C_high@bid)
	Qlong := lc.AskQty
	if hp.AskQty < Qlong {
		Qlong = hp.AskQty
	}
	if lp.BidQty < Qlong {
		Qlong = lp.BidQty
	}
	if hc.BidQty < Qlong {
		Qlong = hc.BidQty
	}
	if Qlong < 0 {
		Qlong = 0
	}

	// Executable qty for SHORT BOX pattern (+C_high@ask, +P_low@ask, -C_low@bid, -P_high@bid)
	Qshort := hc.AskQty
	if lp.AskQty < Qshort {
		Qshort = lp.AskQty
	}
	if lc.BidQty < Qshort {
		Qshort = lc.BidQty
	}
	if hp.BidQty < Qshort {
		Qshort = hp.BidQty
	}
	if Qshort < 0 {
		Qshort = 0
	}

	// Apply global max qty cap
	if m := e.maxQty; m > 0 {
		if Qlong > m {
			Qlong = m
		}
		if Qshort > m {
			Qshort = m
		}
	}
	if Qlong <= 0 && Qshort <= 0 {
		return
	}

	// Worst-case price band selection for S*
	var Smin, Smax float64
	if e.useBandCheck {
		Smin, Smax = e.smin, e.smax
		if Smin <= 0 || Smax <= 0 || Smax < Smin {
			b := e.bandPct
			if b <= 0 {
				b = 0.10 // safe default
			}
			Smin = indexPrice * (1 - b)
			Smax = indexPrice * (1 + b)
		}
	} else {
		Smin, Smax = indexPrice, indexPrice
	}

	// Fixed payoff component (USD per 1 qty)
	fixedUSD := (highStrike - lowStrike)

	emit := func(side int8, q float64, profitFloorUSD float64) {
		if q <= 0 {
			return
		}
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
		// netBTC per total qty (sign drives worst-case S* and slope)
		netBTC := (lc.AskPrice + hp.AskPrice - lp.BidPrice - hc.BidPrice) * Qlong
		// slope := dPnL/dS per 1 qty = -netBTC/Q
		slope := (-netBTC) / Qlong
		if e.passFlatnessDirectional(slope) {
			Sstar := Smax
			if netBTC < 0 {
				Sstar = Smin
			}
			fees := (e.feePerLegUSD * 4.0 * Qlong)
			if e.feePerLegRate > 0 {
				fees += (e.feePerLegRate * Sstar * 4.0 * Qlong)
			}
			profitFloor := fixedUSD*Qlong - netBTC*Sstar - fees
			if profitFloor >= e.minProfitUSD {
				emit(BoxLong, Qlong, profitFloor)
			}
		}
	}

	// ===== SHORT BOX =====
	if Qshort > 0 {
		// netBTC per total qty
		netBTC := (hc.AskPrice + lp.AskPrice - lc.BidPrice - hp.BidPrice) * Qshort
		// slope := dPnL/dS per 1 qty = -netBTC/Q
		slope := (-netBTC) / Qshort
		if e.passFlatnessDirectional(slope) {
			Sstar := Smax
			if netBTC < 0 {
				Sstar = Smin
			}
			fees := (e.feePerLegUSD * 4.0 * Qshort)
			if e.feePerLegRate > 0 {
				fees += (e.feePerLegRate * Sstar * 4.0 * Qshort)
			}
			// For short box, fixed payoff sign is negative at expiry
			profitFloor := -fixedUSD*Qshort - netBTC*Sstar - fees
			if profitFloor >= e.minProfitUSD {
				emit(BoxShort, Qshort, profitFloor)
			}
		}
	}
}

// Signals exposes the non-blocking signal channel to downstream executors.
func (e *BoxSpreadHFT) Signals() <-chan BoxSignal { return e.signals }

// ResetSignalMask clears the coarse dedup bitmask; call periodically.
func (e *BoxSpreadHFT) ResetSignalMask() { atomic.StoreUint64(&e.recentSignals, 0) }
