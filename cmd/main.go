package main

import (
	"Options_Hedger/internal/auth"
	"Options_Hedger/internal/data"
	"Options_Hedger/internal/fix"
	"Options_Hedger/internal/strategy"

	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"runtime"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/joho/godotenv"
)

func main() {
	if err := godotenv.Load(); err == nil {
		log.Println("[INFO] .env loaded successfully")
	}

	runtime.GOMAXPROCS(runtime.NumCPU())
	runtime.LockOSThread()

	clientID := os.Getenv("DERIBIT_CLIENT_ID")
	clientSecret := os.Getenv("DERIBIT_CLIENT_SECRET")
	if clientID == "" || clientSecret == "" {
		log.Fatal("[AUTH] missing DERIBIT_CLIENT_ID or DERIBIT_CLIENT_SECRET")
	}

	_ = auth.FetchJWTToken(clientID, clientSecret)

	// if err := data.InitSharedMemory(); err != nil {
	// 	log.Fatal("[SHM] init failed:", err)
	// }
	log.Printf("[INFO] Shared memory base pointer: 0x%x", data.SharedMemoryPtr())

	// // ✅ 자동 fork Index WS 프로세스
	// path, _ := os.Executable()
	// cmd := exec.Command(path, "--index-ws")
	// cmd.Stdout = os.Stdout
	// cmd.Stderr = os.Stderr
	// if err := cmd.Start(); err != nil {
	// 	log.Fatalf("[MAIN] Failed to fork index-ws process: %v", err)
	// }
	// log.Printf("[MAIN] Forked Index WS process (PID=%d)", cmd.Process.Pid)

	btcPrice := fetchBTCPrice()
	log.Printf("[INFO] BTC Price: %.2f", btcPrice)
	data.WriteIndexPrice(btcPrice)

	instruments := fetchInstruments()
	nearestExpiry := findNearestExpiry(instruments)
	options := filterOptions(instruments, nearestExpiry, btcPrice)
	if len(options) > data.MaxOptions {
		options = options[:data.MaxOptions]
	}

	log.Printf("[INFO] Selected %d options from expiry %s", len(options), nearestExpiry)
	fix.SetOptionSymbols(options)

	updateCh := make(chan data.DepthEntry, 1024)
	data.InitOrderBooks(options, updateCh)

	engine := strategy.NewBoxSpreadEngine(updateCh)
	fix.InitBoxEngine(engine)

	go engine.Run()
	go func() {
		for sig := range engine.Signals() {
			log.Printf("[ORDER] BoxSpread triggered: %s Bid=%.4f / %s Ask=%.4f",
				sig.CallSym, sig.CallBid, sig.PutSym, sig.PutAsk)
		}
	}()

	if err := fix.InitFIXEngine("config/quickfix.cfg"); err != nil {
		log.Printf("[FIX] Init failed:", err)
	}
	defer fix.StopFIXEngine()

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig

	log.Println("[MAIN] Shutting down...")
}

// Fetch BTC index price via Deribit REST
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

// Fetch all active BTC option instruments
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

// Find nearest expiry from the instrument list
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

	out := make([]string, len(list))
	for i := range list {
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
