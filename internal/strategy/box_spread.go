package strategy

import (
	"Options_Hedger/internal/data"
)

type BoxSpreadEngine struct {
	updates <-chan data.DepthEntry
	signals chan Signal
}

type Signal struct {
	CallSym string
	PutSym  string
	CallBid float64
	PutAsk  float64
}

func NewBoxSpreadEngine(ch <-chan data.DepthEntry) *BoxSpreadEngine {
	return &BoxSpreadEngine{
		updates: ch,
		signals: make(chan Signal, 100),
	}
}

func (e *BoxSpreadEngine) Signals() <-chan Signal {
	return e.signals
}

func (e *BoxSpreadEngine) Run() {
	for depth := range e.updates {
		// ✅ 여기에 박스 스프레드 탐지 로직
		if depth.BidPrice > 0 && depth.AskPrice > 0 && (depth.BidPrice-depth.AskPrice) > 0.01 {
			sig := Signal{
				CallSym: depth.Instrument,
				PutSym:  depth.Instrument,
				CallBid: depth.BidPrice,
				PutAsk:  depth.AskPrice,
			}
			select {
			case e.signals <- sig:
			default:
			}
		}
	}
}
