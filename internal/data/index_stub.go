//go:build !linux

package data

import (
	"math"
	"sync/atomic"
)

var atomicIndexPrice uint64

func InitSharedMemory() error { return nil }

func WriteIndexPrice(v float64) {
	atomic.StoreUint64(&atomicIndexPrice, math.Float64bits(v))
}

func ReadIndexPrice() float64 {
	return math.Float64frombits(atomic.LoadUint64(&atomicIndexPrice))
}
