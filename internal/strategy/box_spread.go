package strategy

import (
	"Options_Hedger/internal/data"
	"math"
	"strconv"
	"strings"
	"sync"
	"time"
)

type BoxSpreadEngine struct {
	updates chan data.DepthEntry
	signals chan Signal
}

type Signal struct {
	CallSym string
	PutSym  string
	CallBid float64
	PutAsk  float64
}

func NewBoxSpreadEngine(ch chan data.DepthEntry) *BoxSpreadEngine {
	return &BoxSpreadEngine{
		updates: ch,
		signals: make(chan Signal, 1024),
	}
}

func (e *BoxSpreadEngine) Signals() <-chan Signal {
	return e.signals
}

func (e *BoxSpreadEngine) Updates() chan<- data.DepthEntry {
	return e.updates
}

// ✅ Dedup Cache (strike 조합+expiry)
var lastSignal sync.Map

func dedupKey(expiry string, low, high float64) string {
	return expiry + "|" + strconv.FormatFloat(low, 'f', -1, 64) + "|" + strconv.FormatFloat(high, 'f', -1, 64)
}

func alreadySignaled(key string) bool {
	v, ok := lastSignal.Load(key)
	if ok && time.Since(v.(time.Time)) < 5*time.Millisecond {
		return true
	}
	lastSignal.Store(key, time.Now())
	return false
}

// ✅ Incremental 기반 박스 스프레드 탐지 benkim..밀리는거 없는지 탐지 필요
func (e *BoxSpreadEngine) Run() {
	for depth := range e.updates {
		// start := time.Now()
		// triggered := false // ✅ 탐지 여부 플래그

		parts := strings.Split(depth.Instrument, "-")
		if len(parts) < 3 {
			continue
		}
		strike, _ := strconv.ParseFloat(parts[2], 64)
		expiry := parts[1]
		isCall := strings.HasSuffix(depth.Instrument, "-C")

		for _, sym := range data.Symbols() {
			if sym == depth.Instrument || !strings.Contains(sym, expiry) {
				continue
			}
			other := data.GetBestQuote(sym)
			if other.Instrument == "" {
				continue
			}
			otherStrike, _ := strconv.ParseFloat(strings.Split(other.Instrument, "-")[2], 64)
			otherIsCall := strings.HasSuffix(other.Instrument, "-C")

			if isCall && !otherIsCall {
				if e.checkBoxSpread(depth, other, strike, otherStrike, expiry) {
					// triggered = true
				}
			} else if !isCall && otherIsCall {
				if e.checkBoxSpread(other, depth, otherStrike, strike, expiry) {
					// triggered = true
				}
			}
		}

		// benkim..소요시간체크
		// if triggered {
		// 	elapsed := time.Since(start).Microseconds()
		// 	log.Printf("[PERF] %s incremental scan took %dµs", depth.Instrument, elapsed)
		// }
	}
}

// ✅ 조건 검증 + Dedup 적용
func (e *BoxSpreadEngine) checkBoxSpread(call data.DepthEntry, put data.DepthEntry, callStrike, putStrike float64, expiry string) bool {
	low := math.Min(callStrike, putStrike)
	high := math.Max(callStrike, putStrike)
	if low == high {
		return false
	}

	key := dedupKey(expiry, low, high)
	if alreadySignaled(key) {
		return false
	}

	totalCost := (call.AskPrice + put.AskPrice) - (call.BidPrice + put.BidPrice)
	if totalCost < (high - low) {
		sig := Signal{
			CallSym: call.Instrument,
			PutSym:  put.Instrument,
			CallBid: call.AskPrice,
			PutAsk:  put.AskPrice,
		}
		select {
		case e.signals <- sig:
			// log.Printf("[SIGNAL] BoxSpread FOUND | Low=%.0f High=%.0f Premium=%.4f", low, high, totalCost)
		default:
		}
		return true
	}
	return false
}
