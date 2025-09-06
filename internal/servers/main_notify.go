// File: internal/servers/main_notify.go
package servers

import (
	"bytes"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"time"
)

type CloseNotify struct {
	Type           string  `json:"type"`     // "CLOSE_ALL"
	Strategy       string  `json:"strategy"` // e.g., "box_spread"
	QtyBTC         float64 `json:"qty_btc"`
	NearExpiry     uint16  `json:"near_expiry"`
	FarExpiry      uint16  `json:"far_expiry"`
	IndexUSD       float64 `json:"index_usd"`
	DeribitPNLUSD  float64 `json:"deribit_pnl_usd"`
	CombinedPNLUSD float64 `json:"combined_pnl_usd"`
	Note           string  `json:"note,omitempty"`
	TsMs           int64   `json:"ts_ms"`
}

func NotifyMainClose(ev CloseNotify) {
	url := os.Getenv("MAIN_MARKET_NOTIFY_URL")
	if url == "" {
		// 설정 안 되었으면 알림 생략
		return
	}
	body, _ := json.Marshal(ev)
	req, _ := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		log.Printf("[MAIN-NOTIFY] send error: %v", err)
		return
	}
	_ = resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		log.Printf("[MAIN-NOTIFY] non-2xx: %s", resp.Status)
	}
}
