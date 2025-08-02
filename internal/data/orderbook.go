package data

var (
	symbolIndex = make(map[string]int)
	symbols     []string
	updateCh    chan DepthEntry
)

// ✅ 옵션 초기화
func InitOrderBooks(syms []string, ch chan DepthEntry) {
	updateCh = ch
	symbols = syms
	for i, s := range syms {
		symbolIndex[s] = i
	}
}

// ✅ FIX/WS 업데이트 적용
func ApplyUpdate(symbol string, isBid bool, price, qty, idxPrice float64) {
	idx, ok := symbolIndex[symbol]
	if !ok || idx >= MaxOptions {
		return
	}

	current := ReadDepth(idx)

	if isBid {
		WriteDepth(idx, price, qty, current.AskPrice, current.AskQty)
	} else {
		WriteDepth(idx, current.BidPrice, current.BidQty, price, qty)
	}

	if idxPrice > 0 {
		SetIndexPrice(idxPrice)
	}

	if updateCh != nil {
		select {
		case updateCh <- ReadDepth(idx):
		default:
		}
	}
}

// ✅ 현재 BestQuote 읽기
func GetBestQuote(symbol string) DepthEntry {
	idx, ok := symbolIndex[symbol]
	if !ok || idx >= MaxOptions {
		return DepthEntry{}
	}
	return ReadDepth(idx)
}

// ✅ 옵션 심볼 리스트 반환
func Symbols() []string {
	return symbols
}
