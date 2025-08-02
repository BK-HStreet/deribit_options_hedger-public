package data

import (
	"math"
	"sync/atomic"
	"unsafe"
)

const MaxOptions = 40
const cacheLine = 64

// ✅ Go ↔ C++ 공용 메모리 레이아웃
type SharedBook struct {
	IndexPrice float64
	_          [cacheLine - 8]byte // 64바이트 캐시라인 패딩
	Books      [MaxOptions]DepthEntry
}

// ✅ DepthEntry는 고정 크기만 유지 (Instrument 제거)
type DepthEntry struct {
	BidPrice float64
	BidQty   float64
	AskPrice float64
	AskQty   float64
	_        [cacheLine - 4*8]byte // 64바이트 캐시라인 패딩
}

// ✅ 전역 공유 메모리 인스턴스
var shared = &SharedBook{}

// ✅ IndexPrice atomic 저장
func SetIndexPrice(v float64) {
	atomic.StoreUint64((*uint64)(unsafe.Pointer(&shared.IndexPrice)), math.Float64bits(v))
}

// ✅ IndexPrice atomic 읽기
func GetIndexPrice() float64 {
	return math.Float64frombits(atomic.LoadUint64((*uint64)(unsafe.Pointer(&shared.IndexPrice))))
}

// ✅ 지정 인덱스 Depth atomic 저장
func WriteDepth(idx int, bid, bidQty, ask, askQty float64) {
	entry := &shared.Books[idx]
	atomic.StoreUint64((*uint64)(unsafe.Pointer(&entry.BidPrice)), math.Float64bits(bid))
	atomic.StoreUint64((*uint64)(unsafe.Pointer(&entry.BidQty)), math.Float64bits(bidQty))
	atomic.StoreUint64((*uint64)(unsafe.Pointer(&entry.AskPrice)), math.Float64bits(ask))
	atomic.StoreUint64((*uint64)(unsafe.Pointer(&entry.AskQty)), math.Float64bits(askQty))
}

// ✅ 지정 인덱스 Depth atomic 읽기
func ReadDepth(idx int) DepthEntry {
	entry := &shared.Books[idx]
	return DepthEntry{
		BidPrice: math.Float64frombits(atomic.LoadUint64((*uint64)(unsafe.Pointer(&entry.BidPrice)))),
		BidQty:   math.Float64frombits(atomic.LoadUint64((*uint64)(unsafe.Pointer(&entry.BidQty)))),
		AskPrice: math.Float64frombits(atomic.LoadUint64((*uint64)(unsafe.Pointer(&entry.AskPrice)))),
		AskQty:   math.Float64frombits(atomic.LoadUint64((*uint64)(unsafe.Pointer(&entry.AskQty)))),
	}
}

// ✅ Go ↔ C++ 공유 메모리 포인터 노출
func SharedMemoryPtr() uintptr {
	return uintptr(unsafe.Pointer(shared))
}
