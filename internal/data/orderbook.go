package data

import (
	"math"
	"sync/atomic"
	"unsafe"
)

const MaxOptions = 40
const cacheLine = 64

// DepthEntry는 cache-line에 맞춰 정렬된 best bid/ask 저장용 구조체
type DepthEntry struct {
	Instrument string
	BidPrice   float64
	BidQty     float64
	AskPrice   float64
	AskQty     float64
	_          [cacheLine - unsafe.Sizeof("") - 4*8]byte
}

// lock-free BestQuote 배열
var (
	symbolIndex = make(map[string]int)
	symbols     []string
	books       [MaxOptions]DepthEntry
	updateCh    chan DepthEntry
)

// InitOrderBooks initializes all DepthEntry slots and index mapping
func InitOrderBooks(syms []string, ch chan DepthEntry) {
	updateCh = ch
	symbols = syms
	for i, s := range syms {
		symbolIndex[s] = i
		books[i].Instrument = s
	}
}

// ApplyUpdate applies best bid/ask update using atomic writes
func ApplyUpdate(symbol string, isBid bool, price, qty float64) {
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

	// Non-blocking event push
	if updateCh != nil {
		select {
		case updateCh <- *entry:
		default:
		}
	}
}

// GetBestQuote returns atomic snapshot of best bid/ask for a symbol
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
