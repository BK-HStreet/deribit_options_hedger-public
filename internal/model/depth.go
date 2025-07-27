package model

import "github.com/shopspring/decimal"

// Depth represents top-of-book bid/ask for an option.
type Depth struct {
	Instrument string  // Deribit instrument name (e.g., BTC-27JUL25-118000-C)
	Bid        float64 // Best bid price
	Ask        float64 // Best ask price
	BidQty     float64 // Bid side quantity
	AskQty     float64 // Ask side quantity
}

// Helper methods to convert to decimal for FIX fields
func (d *Depth) AskDecimal() decimal.Decimal {
	return decimal.NewFromFloat(d.Ask)
}

func (d *Depth) BidDecimal() decimal.Decimal {
	return decimal.NewFromFloat(d.Bid)
}

func (d *Depth) BidQtyDecimal() decimal.Decimal {
	return decimal.NewFromFloat(d.BidQty)
}

func (d *Depth) AskQtyDecimal() decimal.Decimal {
	return decimal.NewFromFloat(d.AskQty)
}
