package model

import "github.com/shopspring/decimal"

// Depth represents top-of-book bid/ask for an option.
type Depth struct {
	Instrument string
	Bid        float64
	Ask        float64
	Quantity   float64 // ✅ added field for FIX order size
}

// Helper methods to convert to decimal for FIX fields
func (d *Depth) BidDecimal() decimal.Decimal {
	return decimal.NewFromFloat(d.Bid)
}

func (d *Depth) QuantityDecimal() decimal.Decimal {
	return decimal.NewFromFloat(d.Quantity)
}
