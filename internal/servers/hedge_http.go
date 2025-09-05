package servers

import (
	"encoding/json"
	"log"
	"net/http"
	"os"
	"strings"
	"sync/atomic"
)

// ─────────────────────────────────────────────────────────────────────────────
// Public interface: any strategy engine can be woken up via HTTP
// Keep it minimal so it works even if only box_spread exists.
// ─────────────────────────────────────────────────────────────────────────────

type HedgeHTTPEngine interface {
	Wake()
}

// Optional interface: engines that can accept main-market PnL updates
// (If the engine implements this, we'll call it; otherwise we ignore.)
type mmUpdatable interface {
	UpdateMainMarketPNL(pnlUSD float64, seq uint64)
}

// ─────────────────────────────────────────────────────────────────────────────
// Message formats
// ─────────────────────────────────────────────────────────────────────────────

type hedgeHTTPMsg struct {
	Seq      uint64  `json:"seq"`
	Type     string  `json:"type"` // "SNAPSHOT" | "CLOSE_ALL"
	Side     string  `json:"side"` // "LONG" | "SHORT" | "FLAT"
	QtyBTC   float64 `json:"qty_btc"`
	BaseUSD  float64 `json:"base_usd"`
	IndexUSD float64 `json:"index_usd,omitempty"` // optional manual index override
	TsMs     int64   `json:"ts_ms"`
}

// Main-market unrealized PnL update payload
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

	var lastSeq uint64   // de-dupe for /hedge/target
	var lastMMSeq uint64 // de-dupe for /hedge/update_mm

	mux := http.NewServeMux()

	// 1) Hedge target from the main system (SNAPSHOT/CLOSE_ALL).
	//    In box-only setups, just wake the engine; do not persist or route targets.
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

		// seq de-dupe
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

		// Box engine does not track external targets; just wake it.
		e.Wake()

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	})

	// 2) Main-market unrealized PnL updates.
	//    If the engine supports it, forward; otherwise safely ignore.
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

		log.Printf("[HEDGE-HTTP] /hedge/update_mm seq=%d PNL=%f TsMs=%d",
			m.Seq, m.MainPNLUSD, m.TsMs)

		// seq de-dupe
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

		// Forward if supported
		if up, ok := e.(mmUpdatable); ok {
			up.UpdateMainMarketPNL(m.MainPNLUSD, m.Seq)
		}

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
