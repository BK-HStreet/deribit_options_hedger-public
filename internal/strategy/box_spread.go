package strategy

import (
	"OptionsHedger/internal/data"
	"log"
	"strings"
	"time"
)

type BoxSpreadEngine struct {
	store   *data.QuoteStore
	signals chan BoxSignal
}

// BoxSignal은 박스스프레드 조건이 충족될 때 전송되는 시그널 데이터
type BoxSignal struct {
	CallSym string
	PutSym  string
	CallBid float64
	PutAsk  float64
}

// ✅ BoxSpreadEngine 생성자
func NewBoxSpreadEngine(store *data.QuoteStore) *BoxSpreadEngine {
	return &BoxSpreadEngine{
		store:   store,
		signals: make(chan BoxSignal, 20), // 버퍼 늘림
	}
}

// ✅ 전략 엔진 시작 (주기적으로 QuoteStore 스냅샷 평가)
func (e *BoxSpreadEngine) Start() {
	go func() {
		ticker := time.NewTicker(200 * time.Millisecond) // 더 빠른 체크
		defer ticker.Stop()

		for range ticker.C {
			quotes := e.store.Snapshot()
			e.evaluate(quotes)
		}
	}()
}

// ✅ 옵션 쌍을 평가하여 BoxSpread 조건 충족 시 시그널 전송
func (e *BoxSpreadEngine) evaluate(quotes map[string]data.OptionQuote) {
	for callSym, call := range quotes {
		if !strings.HasSuffix(callSym, "-C") {
			continue
		}
		putSym := strings.Replace(callSym, "-C", "-P", 1)
		put, ok := quotes[putSym]
		if !ok {
			continue
		}

		// BoxSpread 진입 조건 예시: 콜 Bid > 0 && 풋 Ask > 0 && 스프레드 조건 만족
		if call.Bid > 0 && put.Ask > 0 && (put.Ask-call.Bid) > 0 && (put.Ask-call.Bid) < 0.1 {
			log.Printf("[STRATEGY] BoxSpread condition met: %s Bid %.4f / %s Ask %.4f",
				callSym, call.Bid, putSym, put.Ask)

			sig := BoxSignal{
				CallSym: callSym,
				PutSym:  putSym,
				CallBid: call.Bid,
				PutAsk:  put.Ask,
			}

			select {
			case e.signals <- sig:
			default:
			}
		}
	}
}

// ✅ Signals 채널 리턴
func (e *BoxSpreadEngine) Signals() <-chan BoxSignal {
	return e.signals
}
