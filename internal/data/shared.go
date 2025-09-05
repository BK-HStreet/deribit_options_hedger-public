package data

import (
	"math"
	"sync/atomic"
	"unsafe"
)

const MaxOptions = 40
const cacheLine = 64

// SharedBook: cache-line optimized structure for shared memory.
// Holds the index price and a fixed number of depth entries.
type SharedBook struct {
	IndexPrice   float64
	LastUpdateNs int64                // Last update timestamp (nanoseconds)
	_            [cacheLine - 16]byte // Padding for cache line alignment
	Books        [MaxOptions]DepthEntry
}

// DepthEntry: 64-byte aligned order book entry, optimized for branch prediction.
type DepthEntry struct {
	BidPrice     float64
	BidQty       float64
	AskPrice     float64
	AskQty       float64
	LastUpdateNs int64
	_            [cacheLine - 40]byte // Padding for cache line alignment
}

// Update: represents a single order book update, optimized for stack allocation.
type Update struct {
	SymbolIdx  int32   // Symbol index identifier
	IsBid      bool    // true if bid, false if ask
	Price      float64 // Price of the update
	Qty        float64 // Quantity at this price
	IndexPrice float64 // Current index price
	UpdateTime int64   // Update timestamp in nanoseconds
}

var shared = &SharedBook{}

// SetIndexPrice atomically updates the index price and its timestamp.
func SetIndexPrice(v float64) {
	now := Nanotime()
	atomic.StoreUint64((*uint64)(unsafe.Pointer(&shared.IndexPrice)), math.Float64bits(v))
	atomic.StoreInt64(&shared.LastUpdateNs, now)
}

// GetIndexPrice atomically reads the latest index price.
func GetIndexPrice() float64 {
	return math.Float64frombits(atomic.LoadUint64((*uint64)(unsafe.Pointer(&shared.IndexPrice))))
}

// WriteDepthFast atomically updates bid/ask values for a given symbol index.
func WriteDepthFast(idx int, bid, bidQty, ask, askQty float64) {
	entry := &shared.Books[idx]
	now := Nanotime()

	// Atomic updates (order matters to ensure consistency)
	atomic.StoreUint64((*uint64)(unsafe.Pointer(&entry.BidPrice)), math.Float64bits(bid))
	atomic.StoreUint64((*uint64)(unsafe.Pointer(&entry.BidQty)), math.Float64bits(bidQty))
	atomic.StoreUint64((*uint64)(unsafe.Pointer(&entry.AskPrice)), math.Float64bits(ask))
	atomic.StoreUint64((*uint64)(unsafe.Pointer(&entry.AskQty)), math.Float64bits(askQty))
	atomic.StoreInt64(&entry.LastUpdateNs, now)
}

// ReadDepthFast atomically reads the depth entry at the given index.
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

// Nanotime is a runtime intrinsic providing nanosecond-precision timestamps.
//
//go:linkname Nanotime runtime.nanotime
func Nanotime() int64

// SharedMemoryPtr returns the base pointer of the shared memory structure.
func SharedMemoryPtr() uintptr {
	return uintptr(unsafe.Pointer(shared))
}
