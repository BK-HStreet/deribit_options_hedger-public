package data

import (
	"sync/atomic"
	"unsafe"
)

const MaxOptions = 40
const cacheLine = 64

// DepthEntry holds current best bid/ask snapshot (cache-line aligned)
type DepthEntry struct {
	Instrument string
	BidPrice   float64
	BidQty     float64
	AskPrice   float64
	AskQty     float64
	_          [cacheLine - unsafe.Sizeof("") - 4*8]byte
}

// OrderBook stores all levels for a single symbol
type OrderBook struct {
	Bids map[float64]float64 // price -> qty
	Asks map[float64]float64 // price -> qty
	Best atomic.Pointer[DepthEntry]
}

var (
	books       [MaxOptions]*OrderBook
	symbolIndex = make(map[string]int)
	updateCh    chan DepthEntry
)

// InitOrderBooks initializes all orderbooks for given symbols
func InitOrderBooks(symbols []string, ch chan DepthEntry) {
	updateCh = ch
	for i, sym := range symbols {
		ob := &OrderBook{
			Bids: make(map[float64]float64, 16),
			Asks: make(map[float64]float64, 16),
		}
		entry := &DepthEntry{Instrument: sym}
		ob.Best.Store(entry)
		books[i] = ob
		symbolIndex[sym] = i
	}
}

// ApplyUpdate updates a price level and recomputes best bid/ask
func ApplyUpdate(symbol string, isBid bool, price, qty float64) {
	idx, ok := symbolIndex[symbol]
	if !ok {
		return
	}
	ob := books[idx]

	if isBid {
		if qty == 0 {
			delete(ob.Bids, price)
		} else {
			ob.Bids[price] = qty
		}
	} else {
		if qty == 0 {
			delete(ob.Asks, price)
		} else {
			ob.Asks[price] = qty
		}
	}

	var bestBid, bestAsk float64
	var bidQty, askQty float64

	for p, q := range ob.Bids {
		if p > bestBid {
			bestBid = p
			bidQty = q
		}
	}
	for p, q := range ob.Asks {
		if bestAsk == 0 || p < bestAsk {
			bestAsk = p
			askQty = q
		}
	}

	entry := &DepthEntry{
		Instrument: symbol,
		BidPrice:   bestBid,
		BidQty:     bidQty,
		AskPrice:   bestAsk,
		AskQty:     askQty,
	}
	ob.Best.Store(entry)

	if updateCh != nil {
		select {
		case updateCh <- *entry:
		default:
		}
	}
}

// GetBestQuote returns the current best bid/ask for a symbol
func GetBestQuote(symbol string) DepthEntry {
	idx, ok := symbolIndex[symbol]
	if !ok {
		return DepthEntry{}
	}
	ob := books[idx]
	ptr := ob.Best.Load()
	if ptr != nil {
		return *ptr
	}
	return DepthEntry{}
}
