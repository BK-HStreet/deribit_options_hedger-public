package fix

import (
	"log"
	"time"

	"Options_Hedger/internal/model"

	"github.com/quickfixgo/enum"
	"github.com/quickfixgo/field"
	"github.com/quickfixgo/fix44/newordersingle"
	"github.com/quickfixgo/quickfix"
	"github.com/shopspring/decimal"
)

// SendOrder sends a NewOrderSingle for box spread detection result.
func SendOrder(d model.Depth, side enum.Side) {
	// ✅ NewOrderSingle 생성
	order := newordersingle.New(
		field.NewClOrdID("BOX"+time.Now().Format("150405")),
		field.NewSide(side),
		field.NewTransactTime(time.Now()),
		field.NewOrdType(enum.OrdType_LIMIT),
	)

	// Setting common fields
	order.Set(field.NewSymbol(d.Instrument))
	order.Set(field.NewTimeInForce(enum.TimeInForce_IMMEDIATE_OR_CANCEL))

	// Deciding Price & Qty according to Bid/Ask direction
	if side == enum.Side_BUY {
		order.Set(field.NewOrderQty(decimal.NewFromFloat(d.AskQty), 0))
		order.Set(field.NewPrice(decimal.NewFromFloat(d.Ask), 0))
	} else if side == enum.Side_SELL {
		order.Set(field.NewOrderQty(decimal.NewFromFloat(d.BidQty), 0))
		order.Set(field.NewPrice(decimal.NewFromFloat(d.Bid), 0))
	}

	// Auto selecting the session (based on quickfix.cfg)
	if err := quickfix.Send(order); err != nil {
		log.Println("[FIX] SendOrder error:", err)
	} else {
		if side == enum.Side_BUY {
			log.Printf("[FIX] Sent BUY %s @ %.2f Qty=%.4f", d.Instrument, d.Ask, d.AskQty)
		} else {
			log.Printf("[FIX] Sent SELL %s @ %.2f Qty=%.4f", d.Instrument, d.Bid, d.BidQty)
		}
	}
}
