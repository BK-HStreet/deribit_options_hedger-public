package fix

import (
	"log"
	"time"

	"OptionsHedger/internal/model"

	"github.com/quickfixgo/enum"
	"github.com/quickfixgo/field"
	"github.com/quickfixgo/fix44/newordersingle"
	"github.com/quickfixgo/quickfix"
	"github.com/shopspring/decimal"
)

func SendOrder(d model.Depth) {
	// ✅ NewOrderSingle 생성
	order := newordersingle.New(
		field.NewClOrdID("BOX"+time.Now().Format("150405")),
		field.NewSide(enum.Side_BUY),
		field.NewTransactTime(time.Now()),
		field.NewOrdType(enum.OrdType_LIMIT),
	)

	// ✅ 필드 설정
	order.Set(field.NewSymbol(d.Instrument))
	order.Set(field.NewOrderQty(decimal.NewFromFloat(d.Quantity), 0))
	order.Set(field.NewPrice(decimal.NewFromFloat(d.Bid), 0))
	order.Set(field.NewTimeInForce(enum.TimeInForce_IMMEDIATE_OR_CANCEL))

	// ✅ 세션 자동 선택 (quickfix.cfg 기반)
	if err := quickfix.Send(order); err != nil {
		log.Println("[FIX] SendOrder error:", err)
	} else {
		log.Printf("[FIX] Sent NewOrderSingle %s @ %.2f", d.Instrument, d.Bid)
	}
}
