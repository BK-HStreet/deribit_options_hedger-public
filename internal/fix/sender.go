package fix

import (
	"fmt"
	"log"
	"sync"
	"sync/atomic"
	"time"

	"Options_Hedger/internal/model"

	"github.com/quickfixgo/enum"
	"github.com/quickfixgo/field"
	"github.com/quickfixgo/fix44/newordersingle"
	"github.com/quickfixgo/quickfix"
	"github.com/shopspring/decimal"
)

// -----------------------------------------------------------------------------
// Legacy API (kept for box-spread path)
// -----------------------------------------------------------------------------

// SendOrder sends a NewOrderSingle for box spread detection result.
func SendOrder(d model.Depth, side enum.Side) {
	// NewOrderSingle
	order := newordersingle.New(
		field.NewClOrdID(newClOrdID("BOX")),
		field.NewSide(side),
		field.NewTransactTime(time.Now()),
		field.NewOrdType(enum.OrdType_LIMIT),
	)
	order.Set(field.NewSymbol(d.Instrument))
	// order.Set(field.NewTimeInForce(enum.TimeInForce_IMMEDIATE_OR_CANCEL))
	order.Set(field.NewTimeInForce(enum.TimeInForce_GOOD_TILL_CANCEL))

	if side == enum.Side_BUY {
		order.Set(field.NewOrderQty(decimal.NewFromFloat(d.AskQty), 0))
		order.Set(field.NewPrice(decimal.NewFromFloat(d.Ask), 0))
	} else if side == enum.Side_SELL {
		order.Set(field.NewOrderQty(decimal.NewFromFloat(d.BidQty), 0))
		order.Set(field.NewPrice(decimal.NewFromFloat(d.Bid), 0))
	}

	if err := quickfix.Send(order); err != nil {
		log.Println("[FIX] SendOrder error:", err)
	} else {
		if side == enum.Side_BUY {
			log.Printf("[FIX] Sent BUY %s @ %.6f Qty=%.6f", d.Instrument, d.Ask, d.AskQty)
		} else {
			log.Printf("[FIX] Sent SELL %s @ %.6f Qty=%.6f", d.Instrument, d.Bid, d.BidQty)
		}
	}
}

// -----------------------------------------------------------------------------
// Generalized HFT helpers for IOC option orders (Deribit options).
// -----------------------------------------------------------------------------

// OrderReq represents a single IOC limit order to send.
type OrderReq struct {
	Symbol      string
	Side        enum.Side
	Price       float64
	Qty         float64
	TIF         enum.TimeInForce // usually IOC
	ClOrdPrefix string
}

var seq uint64

func newClOrdID(prefix string) string {
	// Example: EM4 15:04:05.123 - seq
	n := atomic.AddUint64(&seq, 1)
	return fmt.Sprintf("%s%s-%d", prefix, time.Now().UTC().Format("150405.000"), n)
}

// SendBatch sends multiple IOC orders concurrently.
// It returns the first error per order index (nil if success).
func SendBatch(reqs []OrderReq) []error {
	errs := make([]error, len(reqs))
	var wg sync.WaitGroup
	wg.Add(len(reqs))

	for i := range reqs {
		i := i
		go func() {
			defer wg.Done()
			req := reqs[i]
			pfx := req.ClOrdPrefix
			if pfx == "" {
				pfx = "ORD"
			}
			tif := req.TIF
			if tif == "" {
				// default to GTC (Good Till Cancel)
				// tif = enum.TimeInForce_IMMEDIATE_OR_CANCEL
				tif = enum.TimeInForce_GOOD_TILL_CANCEL
			}

			ord := newordersingle.New(
				field.NewClOrdID(newClOrdID(pfx)),
				field.NewSide(req.Side),
				field.NewTransactTime(time.Now()),
				field.NewOrdType(enum.OrdType_LIMIT),
			)
			ord.Set(field.NewSymbol(req.Symbol))
			ord.Set(field.NewTimeInForce(tif))
			ord.Set(field.NewOrderQty(decimal.NewFromFloat(req.Qty), 0))
			ord.Set(field.NewPrice(decimal.NewFromFloat(req.Price), 0))

			if err := quickfix.Send(ord); err != nil {
				errs[i] = err
				log.Printf("[FIX] SendBatch error: %v (sym=%s side=%s px=%.6f qty=%.6f)",
					err, req.Symbol, sideToStr(req.Side), req.Price, req.Qty)
				return
			}
			log.Printf("[FIX] IOC %s %s @ %.6f Qty=%.6f",
				sideToStr(req.Side), req.Symbol, req.Price, req.Qty)
		}()
	}

	wg.Wait()
	return errs
}

// sideToStr converts FIX enum.Side to human-readable string.
func sideToStr(s enum.Side) string {
	switch s {
	case enum.Side_BUY:
		return "BUY"
	case enum.Side_SELL:
		return "SELL"
	default:
		return fmt.Sprintf("Side(%s)", s)
	}
}
