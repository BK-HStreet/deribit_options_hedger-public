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

// 공통 HTTP 제어용 최소 메서드 집합
type HedgeHTTPEngine interface {
	Wake()
	SetTarget(strategy.HedgeTarget)
}

// (선택) 메인마켓 PnL 업데이트 지원 엔진
type HedgeMMUpdater interface {
	UpdateMainMarketPNL(pnlUSD float64, seq uint64)
}

type hedgeHTTPMsg struct {
	Seq      uint64  `json:"seq"`
	Type     string  `json:"type"` // "SNAPSHOT" | "CLOSE_ALL"
	Side     string  `json:"side"` // "LONG" | "SHORT" | "FLAT"
	QtyBTC   float64 `json:"qty_btc"`
	BaseUSD  float64 `json:"base_usd"`
	IndexUSD float64 `json:"index_usd,omitempty"` // (선택) IndexFromTarget일 때 사용
	TsMs     int64   `json:"ts_ms"`
}

type mmUpdateMsg struct {
	Seq               uint64  `json:"seq"`
	MMUnrealizedPNLUS float64 `json:"mm_unrealized_pnl_usd"`
	TsMs              int64   `json:"ts_ms"`
}

// ServeHedgeHTTP: 어떤 엔진이든 인터페이스를 만족하면 OK
func ServeHedgeHTTP(e HedgeHTTPEngine) {
	addr := strings.TrimSpace(os.Getenv("HEDGE_HTTP_ADDR"))
	if addr == "" {
		addr = "127.0.0.1:7071"
	}
	var lastSeq uint64
	var lastMMSeq uint64

	mux := http.NewServeMux()

	// 타깃 설정(진입/CloseAll)
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

		log.Printf("[HEDGE-HTTP] recv seq=%d type=%s side=%s qty=%.8f base=%.2f idx=%.2f",
			m.Seq, m.Type, m.Side, m.QtyBTC, m.BaseUSD, m.IndexUSD)

		// SEQ 디더플
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
			e.Wake() // 즉시 처리하도록 호출
		default:
			e.SetTarget(strategy.HedgeTarget{
				Side:     side,
				QtyBTC:   m.QtyBTC,
				BaseUSD:  m.BaseUSD,
				IndexUSD: m.IndexUSD, // IndexFromTarget 모드에서만 사용됨
				Seq:      m.Seq,
			})
			e.Wake()
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	})

	// 메인마켓 미실현 PnL 업데이트
	mux.HandleFunc("/hedge/update_mm", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
		defer r.Body.Close()

		var m mmUpdateMsg
		if err := json.NewDecoder(r.Body).Decode(&m); err != nil {
			http.Error(w, "bad json", http.StatusBadRequest)
			return
		}

		// 디더플
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

		if upd, ok := e.(HedgeMMUpdater); ok {
			upd.UpdateMainMarketPNL(m.MMUnrealizedPNLUS, m.Seq)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	})

	go func() {
		log.Printf("[HEDGE-HTTP] listening on http://%s (POST /hedge/target, /hedge/update_mm)", addr)
		if err := http.ListenAndServe(addr, mux); err != nil {
			log.Printf("[HEDGE-HTTP] server stopped: %v", err)
		}
	}()
}
