package data

import (
	"log"
	"os"
)

const (
	maxSymbols = 40
)

var (
	symbolToIndex [maxSymbols]int32 // direct indexing
	symbolNames   [maxSymbols]string
	updateCh      chan Update
	symbolCount   int32
	obDebug       = os.Getenv("DATA_OB_DEBUG") == "1"
)

func InitOrderBooks(syms []string, ch chan Update) {
	updateCh = ch
	count := len(syms)
	if count > maxSymbols {
		count = maxSymbols
	}

	for i := 0; i < count; i++ {
		symbolToIndex[i] = int32(i)
		symbolNames[i] = syms[i]
	}
	symbolCount = int32(count)
}

func ApplyUpdateFast(symbolIdx int32, isBid bool, price, qty, idxPrice float64) {
	if symbolIdx < 0 || symbolIdx >= symbolCount {
		if obDebug {
			log.Printf("[OB][WRN] ApplyUpdateFast: bad idx=%d isBid=%t price=%.8f", symbolIdx, isBid, price)
		}
		return
	}

	idx := int(symbolIdx)
	current := ReadDepthFast(idx)

	if isBid {
		WriteDepthFast(idx, price, qty, current.AskPrice, current.AskQty)
	} else {
		WriteDepthFast(idx, current.BidPrice, current.BidQty, price, qty)
	}

	if idxPrice > 0 {
		SetIndexPrice(idxPrice)
	}

	after := ReadDepthFast(idx)
	if obDebug {
		name := GetSymbolName(symbolIdx)
		now := Nanotime()
		freshNs := int64( /* match strategy */ 30_000) * 1_000_000 // 30s → ns; for refernce only
		isFresh := after.LastUpdateNs > 0 && (now-after.LastUpdateNs) <= freshNs

		if isBid {
			log.Printf("[OB][WRITE] %s idx=%d side=BID in: p=%.10f q=%.6f idx=%.2f | before: B=%.10f/Q=%.6f A=%.10f/Q=%.6f | after: B=%.10f/Q=%.6f A=%.10f/Q=%.6f ts=%d fresh=%t",
				name, idx, price, qty, idxPrice,
				current.BidPrice, current.BidQty, current.AskPrice, current.AskQty,
				after.BidPrice, after.BidQty, after.AskPrice, after.AskQty, after.LastUpdateNs, isFresh)
		} else {
			log.Printf("[OB][WRITE] %s idx=%d side=ASK in: p=%.10f q=%.6f idx=%.2f | before: B=%.10f/Q=%.6f A=%.10f/Q=%.6f | after: B=%.10f/Q=%.6f A=%.10f/Q=%.6f ts=%d fresh=%t",
				name, idx, price, qty, idxPrice,
				current.BidPrice, current.BidQty, current.AskPrice, current.AskQty,
				after.BidPrice, after.BidQty, after.AskPrice, after.AskQty, after.LastUpdateNs, isFresh)
		}
	}

	// non-blocking channel trasnfer
	select {
	case updateCh <- Update{
		SymbolIdx:  symbolIdx,
		IsBid:      isBid,
		Price:      price,
		Qty:        qty,
		IndexPrice: idxPrice,
		UpdateTime: Nanotime(),
	}:
	default:
	}
}

func GetSymbolName(idx int32) string {
	if idx < 0 || idx >= symbolCount {
		return ""
	}
	return symbolNames[idx]
}

func GetSymbolCount() int32 {
	return symbolCount
}
