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

// ✅ Incremental 기반 박스 스프레드 탐지
func (e *BoxSpreadEngine) Run() {
	for depth := range e.updates {
		// ✅ 어떤 옵션(symbol)에 대한 업데이트인지 찾기
		var symName string
		for _, sym := range data.Symbols() {
			quote := data.GetBestQuote(sym)
			if quote.BidPrice == depth.BidPrice && quote.AskPrice == depth.AskPrice &&
				quote.BidQty == depth.BidQty && quote.AskQty == depth.AskQty {
				symName = sym
				break
			}
		}
		if symName == "" {
			continue
		}

		parts := strings.Split(symName, "-")
		if len(parts) < 3 {
			continue
		}
		strike, _ := strconv.ParseFloat(parts[2], 64)
		expiry := parts[1]
		isCall := strings.HasSuffix(symName, "-C")

		for _, sym := range data.Symbols() {
			if sym == symName || !strings.Contains(sym, expiry) {
				continue
			}
			other := data.GetBestQuote(sym)
			if other.BidPrice == 0 && other.AskPrice == 0 {
				continue
			}

			otherParts := strings.Split(sym, "-")
			if len(otherParts) < 3 {
				continue
			}
			otherStrike, _ := strconv.ParseFloat(otherParts[2], 64)
			otherIsCall := strings.HasSuffix(sym, "-C")

			if isCall && !otherIsCall {
				e.checkBoxSpread(symName, sym, depth, other, strike, otherStrike, expiry)
			} else if !isCall && otherIsCall {
				e.checkBoxSpread(sym, symName, other, depth, otherStrike, strike, expiry)
			}
		}
	}
}

func (e *BoxSpreadEngine) checkBoxSpread(callSym, putSym string, call data.DepthEntry, put data.DepthEntry, callStrike, putStrike float64, expiry string) bool {
	low := math.Min(callStrike, putStrike)
	high := math.Max(callStrike, putStrike)
	if low == high {
		return false
	}

	key := dedupKey(expiry, low, high)
	if alreadySignaled(key) {
		return false
	}

	idxPrice := data.GetIndexPrice()
	usdCallAsk := call.AskPrice * idxPrice
	usdCallBid := call.BidPrice * idxPrice
	usdPutAsk := put.AskPrice * idxPrice
	usdPutBid := put.BidPrice * idxPrice

	// log.Printf("Box Check: %.4f, %.4f, %.4f, %.4f|| %.4f ||", usdCallAsk, usdCallBid, usdPutAsk, usdPutBid, idxPrice)
	return false // benkim.. 복원필

	totalCost := (usdCallAsk + usdPutAsk) - (usdCallBid + usdPutBid)
	if totalCost < (high - low) {
		sig := Signal{
			CallSym: callSym,
			PutSym:  putSym,
			CallBid: usdCallAsk,
			PutAsk:  usdPutAsk,
		}
		select {
		case e.signals <- sig:
		default:
		}
		return true
	}
	return false
}
