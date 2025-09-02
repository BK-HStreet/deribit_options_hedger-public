// File: internal/servers/hedge_http.go
package servers

import (
	"Options_Hedger/internal/strategy"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"strings"
	"sync/atomic"
)

// ─────────────────────────────────────────────────────────────────────────────
// 공용 인터페이스: 어떤 전략 엔진이든 HTTP로 깨우고(SetTarget) 사용할 수 있게
// ─────────────────────────────────────────────────────────────────────────────

type HedgeHTTPEngine interface {
	Wake()
	SetTarget(strategy.HedgeTarget)
}

// 선택 인터페이스: 메인마켓 PnL 업데이트를 받을 수 있는 엔진(예: ExpectedMoveCalendar)
type mmUpdatable interface {
	UpdateMainMarketPNL(pnlUSD float64, seq uint64)
}

// ─────────────────────────────────────────────────────────────────────────────
// 메시지 포맷
// ─────────────────────────────────────────────────────────────────────────────

type hedgeHTTPMsg struct {
	Seq      uint64  `json:"seq"`
	Type     string  `json:"type"` // "SNAPSHOT" | "CLOSE_ALL"
	Side     string  `json:"side"` // "LONG" | "SHORT" | "FLAT"
	QtyBTC   float64 `json:"qty_btc"`
	BaseUSD  float64 `json:"base_usd"`
	IndexUSD float64 `json:"index_usd,omitempty"` // (옵션) IndexFromTarget일 때 사용
	TsMs     int64   `json:"ts_ms"`
}

// 메인 마켓 미실현 PnL 업데이트
type hedgeMMUpdate struct {
	Seq        uint64  `json:"seq"`
	MainPNLUSD float64 `json:"main_pnl_usd"`
	TsMs       int64   `json:"ts_ms"`
}

// ─────────────────────────────────────────────────────────────────────────────

func ServeHedgeHTTP(e HedgeHTTPEngine) {
	addr := strings.TrimSpace(os.Getenv("HEDGE_HTTP_ADDR"))
	if addr == "" {
		addr = "127.0.0.1:7071"
	}

	var lastSeq uint64   // /hedge/target 의 seq 중복 방지
	var lastMMSeq uint64 // /hedge/update_mm 의 seq 중복 방지(선택)

	mux := http.NewServeMux()

	// 1) 메인에서 헷지 타깃(SNAPSHOT/CLOSE_ALL) 전달
	mux.HandleFunc("/hedge/target", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
		defer r.Body.Close()

		var m hedgeHTTPMsg
		if err := json.NewDecoder(r.Body).Decode(&m); err != nil {
			http.Error(w, "bad json", http.StatusBadRequest)
			return
		}

		log.Printf("[HEDGE-HTTP] /hedge/target seq=%d type=%s side=%s qty=%.8f base=%.2f idx=%.2f",
			m.Seq, m.Type, m.Side, m.QtyBTC, m.BaseUSD, m.IndexUSD)

		// seq 중복 차단
		for {
			prev := atomic.LoadUint64(&lastSeq)
			if m.Seq != 0 && m.Seq <= prev {
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(`{"ok":true,"ignored":"stale_seq"}`))
				return
			}
			if m.Seq == 0 || atomic.CompareAndSwapUint64(&lastSeq, prev, m.Seq) {
				break
			}
		}

		side := int8(0)
		switch strings.ToUpper(m.Side) {
		case "LONG":
			side = +1
		case "SHORT":
			side = -1
		}

		switch strings.ToUpper(m.Type) {
		case "CLOSE_ALL":
			e.SetTarget(strategy.HedgeTarget{Side: 0, QtyBTC: 0, BaseUSD: 0, IndexUSD: 0, Seq: m.Seq})
		default: // "SNAPSHOT"
			e.SetTarget(strategy.HedgeTarget{
				Side:     side,
				QtyBTC:   m.QtyBTC,
				BaseUSD:  m.BaseUSD,
				IndexUSD: m.IndexUSD,
				Seq:      m.Seq,
			})
			e.Wake()
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	})

	// 2) 메인에서 미실현 PnL(USD) 업데이트
	mux.HandleFunc("/hedge/update_mm", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
		defer r.Body.Close()

		var m hedgeMMUpdate
		if err := json.NewDecoder(r.Body).Decode(&m); err != nil {
			http.Error(w, "bad json", http.StatusBadRequest)
			return
		}

		// 옵션: seq 중복 방지 (있어도 되고 없어도 됨)
		for {
			prev := atomic.LoadUint64(&lastMMSeq)
			if m.Seq != 0 && m.Seq <= prev {
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(`{"ok":true,"ignored":"stale_seq"}`))
				return
			}
			if m.Seq == 0 || atomic.CompareAndSwapUint64(&lastMMSeq, prev, m.Seq) {
				break
			}
		}

		// 엔진이 PnL 업데이트를 지원하면 전달
		if up, ok := e.(mmUpdatable); ok {
			up.UpdateMainMarketPNL(m.MainPNLUSD, m.Seq)
		}
		// 지원하지 않더라도 OK (무해)

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	})

	go func() {
		log.Printf("[HEDGE-HTTP] listening on http://%s (POST /hedge/target, POST /hedge/update_mm)", addr)
		if err := http.ListenAndServe(addr, mux); err != nil {
			log.Printf("[HEDGE-HTTP] server stopped: %v", err)
		}
	}()
}
