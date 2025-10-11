// File: internal/data/shared.go
package data

import (
	"math"
	"sync/atomic"
	"unsafe"
)

const MaxOptions = 40
const cacheLine = 64

type SharedBook struct {
	IndexPrice   float64
	LastUpdateNs int64 // nanosecond timestamp
	_            [cacheLine - 16]byte
	Books        [MaxOptions]DepthEntry
}

type DepthEntry struct {
	BidPrice     float64
	BidQty       float64
	AskPrice     float64
	AskQty       float64
	LastUpdateNs int64
	_            [cacheLine - 40]byte
}

// Update info - Stack assignment optimization
type Update struct {
	SymbolIdx  int32 // identifying symbols with index
	IsBid      bool
	Price      float64
	Qty        float64
	IndexPrice float64
	UpdateTime int64 // nanosecond
}

var shared = &SharedBook{}

func SetIndexPrice(v float64) {
	now := Nanotime()
	atomic.StoreUint64((*uint64)(unsafe.Pointer(&shared.IndexPrice)), math.Float64bits(v))
	atomic.StoreInt64(&shared.LastUpdateNs, now)
}

func GetIndexPrice() float64 {
	return math.Float64frombits(atomic.LoadUint64((*uint64)(unsafe.Pointer(&shared.IndexPrice))))
}

func WriteDepthFast(idx int, bid, bidQty, ask, askQty float64) {
	entry := &shared.Books[idx]
	now := Nanotime()

	// atomic update
	atomic.StoreUint64((*uint64)(unsafe.Pointer(&entry.BidPrice)), math.Float64bits(bid))
	atomic.StoreUint64((*uint64)(unsafe.Pointer(&entry.BidQty)), math.Float64bits(bidQty))
	atomic.StoreUint64((*uint64)(unsafe.Pointer(&entry.AskPrice)), math.Float64bits(ask))
	atomic.StoreUint64((*uint64)(unsafe.Pointer(&entry.AskQty)), math.Float64bits(askQty))
	atomic.StoreInt64(&entry.LastUpdateNs, now)
}

func ReadDepthFast(idx int) DepthEntry {
	entry := &shared.Books[idx]
	return DepthEntry{
		BidPrice:     math.Float64frombits(atomic.LoadUint64((*uint64)(unsafe.Pointer(&entry.BidPrice)))),
		BidQty:       math.Float64frombits(atomic.LoadUint64((*uint64)(unsafe.Pointer(&entry.BidQty)))),
		AskPrice:     math.Float64frombits(atomic.LoadUint64((*uint64)(unsafe.Pointer(&entry.AskPrice)))),
		AskQty:       math.Float64frombits(atomic.LoadUint64((*uint64)(unsafe.Pointer(&entry.AskQty)))),
		LastUpdateNs: atomic.LoadInt64(&entry.LastUpdateNs),
	}
}

// Runtime nanosecond optimization
//
//go:linkname Nanotime runtime.nanotime
func Nanotime() int64

func SharedMemoryPtr() uintptr {
	return uintptr(unsafe.Pointer(shared))
}
