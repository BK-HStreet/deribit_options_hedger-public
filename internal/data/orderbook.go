package data

import (
	"math"
	"sync/atomic"
	"unsafe"
)

const MaxOptions = 40
const cacheLine = 64

type DepthEntry struct {
	Instrument string
	BidPrice   float64
	BidQty     float64
	AskPrice   float64
	AskQty     float64
	_          [cacheLine - unsafe.Sizeof("") - 4*8]byte
}

var (
	symbolIndex      = make(map[string]int)
	symbols          []string
	books            [MaxOptions]DepthEntry
	updateCh         chan DepthEntry
	atomicIndexPrice uint64 // ✅ 글로벌 BTC Index Price (USD)
)

func InitOrderBooks(syms []string, ch chan DepthEntry) {
	updateCh = ch
	symbols = syms
	for i, s := range syms {
		symbolIndex[s] = i
		books[i].Instrument = s
	}
}

// ✅ 글로벌 IndexPrice 저장
func SetIndexPrice(v float64) {
	atomic.StoreUint64(&atomicIndexPrice, math.Float64bits(v))
}

// ✅ 글로벌 IndexPrice 읽기
func GetIndexPrice() float64 {
	return math.Float64frombits(atomic.LoadUint64(&atomicIndexPrice))
}

func ApplyUpdate(symbol string, isBid bool, price, qty, idxPrice float64) {
	idx, ok := symbolIndex[symbol]
	if !ok {
		return
	}
	entry := &books[idx]

	if isBid {
		atomic.StoreUint64((*uint64)(unsafe.Pointer(&entry.BidPrice)), math.Float64bits(price))
		atomic.StoreUint64((*uint64)(unsafe.Pointer(&entry.BidQty)), math.Float64bits(qty))
	} else {
		atomic.StoreUint64((*uint64)(unsafe.Pointer(&entry.AskPrice)), math.Float64bits(price))
		atomic.StoreUint64((*uint64)(unsafe.Pointer(&entry.AskQty)), math.Float64bits(qty))
	}

	// ✅ IndexPrice 갱신: NaN이거나 0 이하일 경우 갱신하지 않음
	if !math.IsNaN(idxPrice) && idxPrice > 0 {
		SetIndexPrice(idxPrice)
	}

	if updateCh != nil {
		select {
		case updateCh <- *entry:
		default:
		}
	}
}

func GetBestQuote(symbol string) DepthEntry {
	idx, ok := symbolIndex[symbol]
	if !ok {
		return DepthEntry{}
	}
	entry := &books[idx]
	return DepthEntry{
		Instrument: entry.Instrument,
		BidPrice:   math.Float64frombits(atomic.LoadUint64((*uint64)(unsafe.Pointer(&entry.BidPrice)))),
		BidQty:     math.Float64frombits(atomic.LoadUint64((*uint64)(unsafe.Pointer(&entry.BidQty)))),
		AskPrice:   math.Float64frombits(atomic.LoadUint64((*uint64)(unsafe.Pointer(&entry.AskPrice)))),
		AskQty:     math.Float64frombits(atomic.LoadUint64((*uint64)(unsafe.Pointer(&entry.AskQty)))),
	}
}

func Symbols() []string {
	return symbols
}
