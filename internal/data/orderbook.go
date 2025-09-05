package data

const (
	maxSymbols = 40
)

var (
	symbolToIndex [maxSymbols]int32 // direct indexing
	symbolNames   [maxSymbols]string
	updateCh      chan Update
	symbolCount   int32
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
