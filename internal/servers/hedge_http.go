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

type hedgeHTTPMsg struct {
	Seq     uint64  `json:"seq"`
	Type    string  `json:"type"` // "SNAPSHOT" | "CLOSE_ALL"
	Side    string  `json:"side"` // "LONG" | "SHORT" | "FLAT"
	QtyBTC  float64 `json:"qty_btc"`
	BaseUSD float64 `json:"base_usd"`
	TsMs    int64   `json:"ts_ms"`
}

func ServeHedgeHTTP(bc *strategy.BudgetedProtectiveCollar) {
	addr := strings.TrimSpace(os.Getenv("HEDGE_HTTP_ADDR"))
	if addr == "" {
		addr = "127.0.0.1:7071"
	}
	var lastSeq uint64

	mux := http.NewServeMux()
	mux.HandleFunc("/hedge/target", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		defer r.Body.Close()

		var m hedgeHTTPMsg
		if err := json.NewDecoder(r.Body).Decode(&m); err != nil {
			http.Error(w, "bad json", http.StatusBadRequest)
			return
		}

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
			bc.SetTarget(strategy.HedgeTarget{Side: 0, QtyBTC: 0, BaseUSD: 0, Seq: m.Seq})
		default:
			bc.SetTarget(strategy.HedgeTarget{
				Side:    side,
				QtyBTC:  m.QtyBTC,
				BaseUSD: m.BaseUSD,
				Seq:     m.Seq,
			})
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	})

	go func() {
		log.Printf("[HEDGE-HTTP] listening on http://%s (POST /hedge/target)", addr)
		if err := http.ListenAndServe(addr, mux); err != nil {
			log.Printf("[HEDGE-HTTP] server stopped: %v", err)
		}
	}()
}
