package main

import (
	"OptionsHedger/internal/auth"
	"OptionsHedger/internal/fix"
	"OptionsHedger/internal/model"
	"OptionsHedger/internal/strategy"
	"OptionsHedger/internal/wsclient"

	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/joho/godotenv"
)

func main() {
	// ✅ .env 자동 로드
	if err := godotenv.Load(); err == nil {
		log.Println("[INFO] .env loaded successfully")
	}

	runtime.GOMAXPROCS(runtime.NumCPU())
	runtime.LockOSThread()

	// 1) Load credentials
	clientID := os.Getenv("DERIBIT_CLIENT_ID")
	clientSecret := os.Getenv("DERIBIT_CLIENT_SECRET")
	if clientID == "" || clientSecret == "" {
		log.Fatal("[AUTH] missing DERIBIT_CLIENT_ID or DERIBIT_CLIENT_SECRET")
	}
	log.Printf("[DEBUG] client_id: %s", clientID)

	// 2) REST를 이용해 JWT 발급 (API Key 유효성 테스트용)
	_ = auth.FetchJWTToken(clientID, clientSecret)

	// 3) Fetch BTC index price
	btcPrice := fetchBTCPrice()
	log.Printf("[INFO] BTC Price: %.2f", btcPrice)

	// 4) Fetch instruments & select ATM ±20 options
	instruments := fetchInstruments()
	nearestExpiry := findNearestExpiry(instruments)
	options := filterOptions(instruments, nearestExpiry, btcPrice)
	log.Printf("[INFO] Selected %d options from expiry %s", len(options), nearestExpiry)

	var topics []string
	for _, inst := range options {
		topics = append(topics, fmt.Sprintf("book.%s.raw", inst))
	}

	// 5) Initialize FIX engine
	if err := fix.InitFIXEngine("config/quickfix.cfg"); err != nil {
		log.Fatal("[FIX] Init failed:", err)
	}
	defer fix.StopFIXEngine()

	token := auth.FetchJWTToken(clientID, clientSecret)
	log.Printf("[DEBUG] Access Token: %s", token)
	// 6) Connect to Deribit WebSocket (API Key 기반 인증)
	wsclient.ConnectAndServe(
		"wss://www.deribit.com/ws/api/v2",
		topics,
		func(d *model.Depth) {
			d.Quantity = 1.0
			if strategy.IsBoxOpportunity(d) {
				fix.SendOrder(*d)
			}
		},
	)
}

func fetchBTCPrice() float64 {
	res, err := http.Get("https://www.deribit.com/api/v2/public/get_index_price?index_name=btc_usd")
	if err != nil {
		log.Fatal("[PRICE] fetch failed:", err)
	}
	defer res.Body.Close()
	var r struct {
		Result struct {
			IndexPrice float64 `json:"index_price"`
		}
	}
	_ = json.NewDecoder(res.Body).Decode(&r)
	return r.Result.IndexPrice
}

func fetchInstruments() []string {
	res, err := http.Get("https://www.deribit.com/api/v2/public/get_instruments?currency=BTC&kind=option")
	if err != nil {
		log.Fatal("[INSTR] fetch failed:", err)
	}
	defer res.Body.Close()
	var r struct {
		Result []struct {
			InstrumentName string `json:"instrument_name"`
			IsActive       bool   `json:"is_active"`
		}
	}
	_ = json.NewDecoder(res.Body).Decode(&r)
	names := make([]string, 0, len(r.Result))
	for _, inst := range r.Result {
		if inst.IsActive {
			names = append(names, inst.InstrumentName)
		}
	}
	return names
}

func findNearestExpiry(instruments []string) string {
	layout := "02Jan06"
	expMap := map[string]time.Time{}
	for _, name := range instruments {
		parts := strings.Split(name, "-")
		if len(parts) < 3 {
			continue
		}
		if _, ok := expMap[parts[1]]; !ok {
			t, err := time.Parse(layout, parts[1])
			if err == nil {
				expMap[parts[1]] = t
			}
		}
	}
	type p struct {
		label string
		t     time.Time
	}
	list := make([]p, 0, len(expMap))
	for lbl, tm := range expMap {
		list = append(list, p{lbl, tm})
	}
	sort.Slice(list, func(i, j int) bool { return list[i].t.Before(list[j].t) })
	if len(list) == 0 {
		log.Fatal("[EXPIRY] no expiries found")
	}
	return list[0].label
}

func filterOptions(instruments []string, expiry string, atmPrice float64) []string {
	type opt struct {
		name   string
		strike float64
	}
	var list []opt
	for _, name := range instruments {
		if strings.Contains(name, expiry) {
			parts := strings.Split(name, "-")
			if len(parts) >= 3 {
				var s float64
				fmt.Sscanf(parts[2], "%f", &s)
				list = append(list, opt{name, s})
			}
		}
	}
	sort.Slice(list, func(i, j int) bool {
		return abs(list[i].strike-atmPrice) < abs(list[j].strike-atmPrice)
	})
	limit := 40
	if len(list) < limit {
		limit = len(list)
	}
	out := make([]string, limit)
	for i := 0; i < limit; i++ {
		out[i] = list[i].name
	}
	return out
}

func abs(x float64) float64 {
	if x < 0 {
		return -x
	}
	return x
}
