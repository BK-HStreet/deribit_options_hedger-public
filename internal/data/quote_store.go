package data

// Depth represents top-of-book bid/ask for an option.
type Depth struct {
	Instrument string
	Bid        float64
	Ask        float64
	BidQty     float64
	AskQty     float64
}

// QuoteStore optimized for HFT: pre-allocated slice + index map.
type QuoteStore struct {
	symbolIndex map[string]int
	quotes      []Depth
}

// NewQuoteStore initializes the store with known symbols.
func NewQuoteStore(symbols []string) *QuoteStore {
	idxMap := make(map[string]int, len(symbols))
	quotes := make([]Depth, len(symbols))
	for i, sym := range symbols {
		idxMap[sym] = i
		quotes[i] = Depth{Instrument: sym}
	}
	return &QuoteStore{symbolIndex: idxMap, quotes: quotes}
}

// Update applies bid/ask updates for a given symbol.
// Update applies bid/ask updates for a given symbol.
func (qs *QuoteStore) Update(sym string, bid, bidQty, ask, askQty float64) {
	if idx, ok := qs.symbolIndex[sym]; ok {
		d := &qs.quotes[idx]
		d.Bid = bid
		d.BidQty = bidQty
		d.Ask = ask
		d.AskQty = askQty
	}
}

// Get returns the current Depth for a symbol.
func (qs *QuoteStore) Get(sym string) Depth {
	if idx, ok := qs.symbolIndex[sym]; ok {
		return qs.quotes[idx]
	}
	return Depth{}
}

// Snapshot returns a copy of all quotes.
func (qs *QuoteStore) Snapshot() []Depth {
	cp := make([]Depth, len(qs.quotes))
	copy(cp, qs.quotes)
	return cp
}
