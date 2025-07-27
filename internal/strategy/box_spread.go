package strategy

import (
	"OptionsHedger/internal/data"
	"log"
)

type BoxSpreadEngine struct {
	store   *data.QuoteStore
	signals chan Signal
}

type Signal struct {
	CallSym string
	PutSym  string
	CallBid float64
	PutAsk  float64
}

func NewBoxSpreadEngine(store *data.QuoteStore) *BoxSpreadEngine {
	return &BoxSpreadEngine{
		store:   store,
		signals: make(chan Signal, 100),
	}
}

func (e *BoxSpreadEngine) Signals() <-chan Signal {
	return e.signals
}

func (e *BoxSpreadEngine) CheckOpportunity(d data.Depth) {
	// ✅ 간단한 박스스프레드 탐지 로직
	// (CallBid - PutAsk) 차이가 임계값 이상일 때 시그널 생성
	if d.Bid > 0 && d.Ask > 0 && (d.Bid-d.Ask) > 0.01 {
		sig := Signal{
			CallSym: d.Instrument,
			PutSym:  d.Instrument,
			CallBid: d.Bid,
			PutAsk:  d.Ask,
		}
		select {
		case e.signals <- sig:
			log.Printf("[SIGNAL] BoxSpread Opportunity %s Bid=%.4f / Ask=%.4f", d.Instrument, d.Bid, d.Ask)
		default:
		}
	}
}
