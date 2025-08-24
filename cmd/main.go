// File: cmd/main.go
package main

import (
	"Options_Hedger/internal/auth"
	"Options_Hedger/internal/data"
	"Options_Hedger/internal/fix"
	"Options_Hedger/internal/notify"   // notifier package
	"Options_Hedger/internal/strategy" // strategies
	"bufio"

	"context"
	"encoding/json"
	"fmt"
	"log"
	"math"
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

type Instrument struct {
	Name     string `json:"instrument_name"`
	IsActive bool   `json:"is_active"`
	ExpireMs int64  `json:"expiration_timestamp"`
}

func main() {
	if err := godotenv.Load(); err == nil {
		log.Println("[INFO] .env loaded successfully")
	}

	// HFT: reduce thread migrations
	runtime.GOMAXPROCS(1)
	runtime.LockOSThread()

	clientID := os.Getenv("DERIBIT_CLIENT_ID")
	clientSecret := os.Getenv("DERIBIT_CLIENT_SECRET")
	if clientID == "" || clientSecret == "" {
		log.Fatal("[AUTH] missing DERIBIT_CLIENT_ID or DERIBIT_CLIENT_SECRET")
	}
	_ = auth.FetchJWTToken(clientID, clientSecret)

	log.Printf("[INFO] Shared memory base pointer: 0x%x", data.SharedMemoryPtr())

	// Initial index price
	btcPrice := fetchBTCPrice()
	log.Printf("[INFO] BTC Price: %.2f", btcPrice)
	data.SetIndexPrice(btcPrice)

	// Instruments → nearest expiry (UTC) → ATM ±20% symbols (balanced calls/puts)
	instruments := fetchInstruments()
	nearestLabel, expiryUTC := findNearestExpiryUTC(instruments)
	options := filterOptionsByTS(instruments, expiryUTC, btcPrice)
	if len(options) > data.MaxOptions {
		options = options[:data.MaxOptions]
	}
	log.Printf("[INFO] Selected %d options from expiry %s", len(options), nearestLabel)

	// Feed symbols to FIX
	fix.SetOptionSymbols(options)

	// Initialize order books and update channel
	updateCh := make(chan data.Update, 2048)
	data.InitOrderBooks(options, updateCh)

	// Notifier (optional)
	var notifier notify.Notifier
	if n, err := notify.NewTelegramFromEnv(); err != nil {
		log.Printf("[NOTIFY] Telegram disabled: %v", err)
	} else {
		notifier = n
	}

	// 번호 선택/환경변수 기반 전략 선택
	strategyName := chooseStrategy()
	switch strategyName {
	case "protective_collar", "collar":
		pc := strategy.NewProtectiveCollar(updateCh)
		pc.InitializeHFT(options)
		pc.SetNotifier(notifier)
		go pc.Run()
		log.Printf("Protective collar started..")

		// Signal consumer for Protective Collar
		go func() {
			for sig := range pc.Signals() {
				putSym := data.GetSymbolName(int32(sig.PutIdx))
				callSym := data.GetSymbolName(int32(sig.CallIdx))

				put := data.ReadDepthFast(int(sig.PutIdx))
				call := data.ReadDepthFast(int(sig.CallIdx))
				idx := data.GetIndexPrice()

				msg := fmt.Sprintf(
					"[PROTECTIVE-COLLAR]\n"+
						"expiry=%d  index=%.2f  qty=%.4f  netCostUSD=%.2f\n"+
						"buyPut : %s  ask@%.4f (qty=%.4f)\n"+
						"sellCall: %s  bid@%.4f (qty=%.4f)",
					sig.Expiry, idx, sig.Qty, sig.NetCostUSD,
					putSym, put.AskPrice, put.AskQty,
					callSym, call.BidPrice, call.BidQty,
				)

				log.Print(msg)
				if notifier != nil {
					ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
					if err := notifier.Send(ctx, msg); err != nil {
						log.Printf("[NOTIFY] telegram send failed: %v", err)
					}
					cancel()
				}
				fmt.Print("\a") // beep
			}
		}()

		// Periodic coarse-dedup reset
		go func() {
			ticker := time.NewTicker(100 * time.Millisecond)
			defer ticker.Stop()
			for range ticker.C {
				pc.ResetSignalMask()
			}
		}()

	default: // "box_spread"
		engine := strategy.NewBoxSpreadHFT(updateCh)
		engine.InitializeHFT(options)
		go engine.Run()
		log.Printf("Box spread started..")

		// Signal consumer for Box Spread
		go func() {
			for sig := range engine.Signals() {
				lowCallSym := data.GetSymbolName(int32(sig.LowCallIdx))
				lowPutSym := data.GetSymbolName(int32(sig.LowPutIdx))
				highCallSym := data.GetSymbolName(int32(sig.HighCallIdx))
				highPutSym := data.GetSymbolName(int32(sig.HighPutIdx))

				lowCall := data.ReadDepthFast(int(sig.LowCallIdx))
				lowPut := data.ReadDepthFast(int(sig.LowPutIdx))
				highCall := data.ReadDepthFast(int(sig.HighCallIdx))
				highPut := data.ReadDepthFast(int(sig.HighPutIdx))
				idx := data.GetIndexPrice()

				msg := fmt.Sprintf(
					"[BOX-SPREAD]\n"+
						"strikes=%.0f→%.0f  index=%.2f  profit=$%.2f\n"+
						"buyCallLo : %s  ask@%.4f (qty=%.4f)\n"+
						"sellCallHi: %s  bid@%.4f (qty=%.4f)\n"+
						"sellPutLo : %s  bid@%.4f (qty=%.4f)\n"+
						"buyPutHi  : %s  ask@%.4f (qty=%.4f)",
					sig.LowStrike, sig.HighStrike, idx, sig.Profit,
					lowCallSym, lowCall.AskPrice, lowCall.AskQty,
					highCallSym, highCall.BidPrice, highCall.BidQty,
					lowPutSym, lowPut.BidPrice, lowPut.BidQty,
					highPutSym, highPut.AskPrice, highPut.AskQty,
				)

				log.Print(msg)
				if notifier != nil {
					ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
					if err := notifier.Send(ctx, msg); err != nil {
						log.Printf("[NOTIFY] telegram send failed: %v", err)
					}
					cancel()
				}
				fmt.Print("\a") // beep
			}
		}()

		// Periodic coarse-dedup reset
		go func() {
			ticker := time.NewTicker(100 * time.Millisecond)
			defer ticker.Stop()
			for range ticker.C {
				engine.ResetSignalMask()
			}
		}()
	}

	// Start FIX
	if err := fix.InitFIXEngine("config/quickfix.cfg"); err != nil {
		log.Printf("[FIX] Init failed: %v", err)
	}
	defer fix.StopFIXEngine()

	// Wait for shutdown signal
	sigc := make(chan os.Signal, 1)
	signal.Notify(sigc, syscall.SIGINT, syscall.SIGTERM)
	<-sigc
	log.Println("[MAIN] Shutting down...")
}

// ───────────────────────── helpers ─────────────────────────

// 선택 메뉴 + 환경변수 지원
// 우선순위: STRATEGY(문자) > STRATEGY_NUM(숫자) > 콘솔 입력(번호)
func chooseStrategy() string {
	// 1) STRATEGY (text)
	if s := strings.ToLower(strings.TrimSpace(os.Getenv("STRATEGY"))); s != "" {
		switch s {
		case "1", "box_spread", "box", "boxspread":
			return "box_spread"
		case "2", "protective_collar", "collar", "protective":
			return "protective_collar"
		default:
			log.Printf("[STRATEGY] unknown STRATEGY=%q -> default to box_spread", s)
			return "box_spread"
		}
	}
	// 2) STRATEGY_NUM (number)
	if n := strings.TrimSpace(os.Getenv("STRATEGY_NUM")); n != "" {
		if n == "2" {
			return "protective_collar"
		}
		return "box_spread"
	}
	// 3) Interactive menu (번호 선택)
	reader := bufio.NewReader(os.Stdin)
	fmt.Println()
	fmt.Println("전략을 선택하세요:")
	fmt.Println("  1) Box Spread (HFT)")
	fmt.Println("  2) Protective Collar")
	fmt.Print("번호 입력 [기본=1]: ")
	line, _ := reader.ReadString('\n')
	line = strings.TrimSpace(line)
	switch line {
	case "2":
		return "protective_collar"
	default:
		return "box_spread"
	}
}

func fetchBTCPrice() float64 {
	res, err := http.Get("https://www.deribit.com/api/v2/public/get_index_price?index_name=btc_usd")
	if err != nil {
		log.Printf("[PRICE] fetch failed, using default: %v", err)
		return 65000.0
	}
	defer res.Body.Close()
	var r struct {
		Result struct {
			IndexPrice float64 `json:"index_price"`
		} `json:"result"`
	}
	if err := json.NewDecoder(res.Body).Decode(&r); err != nil {
		log.Printf("[PRICE] decode failed, using default: %v", err)
		return 65000.0
	}
	return r.Result.IndexPrice
}

func fetchInstruments() []Instrument {
	res, err := http.Get("https://www.deribit.com/api/v2/public/get_instruments?currency=BTC&kind=option")
	if err != nil {
		log.Fatal("[INSTR] fetch failed:", err)
	}
	defer res.Body.Close()

	var r struct {
		Result []Instrument `json:"result"`
	}
	if err := json.NewDecoder(res.Body).Decode(&r); err != nil {
		log.Fatal("[INSTR] decode failed:", err)
	}

	out := make([]Instrument, 0, len(r.Result))
	for _, inst := range r.Result {
		if inst.IsActive {
			out = append(out, inst)
		}
	}
	log.Printf("[INFO] Fetched %d active instruments", len(out))
	return out
}

// Choose nearest future expiry using expiration_timestamp (UTC)
func findNearestExpiryUTC(instruments []Instrument) (label string, expiryUTC time.Time) {
	nowUTC := time.Now().UTC()
	var best time.Time
	seen := make(map[string]bool)

	for _, inst := range instruments {
		t := time.UnixMilli(inst.ExpireMs).UTC()
		if !t.After(nowUTC) {
			continue
		}
		parts := strings.Split(inst.Name, "-") // BTC-10AUG25-115000-C
		if len(parts) < 3 {
			continue
		}
		lbl := parts[1]
		if seen[lbl] {
			continue
		}
		seen[lbl] = true
		if best.IsZero() || t.Before(best) {
			best, label = t, lbl
		}
	}

	if best.IsZero() {
		log.Fatal("[EXPIRY] no future expiries found (by timestamp)")
	}

	kst := time.FixedZone("KST", 9*3600)
	log.Printf("[INFO] Nearest expiry: %s (UTC %s / KST %s)",
		label,
		best.Format(time.RFC3339),
		best.In(kst).Format("2006-01-02 15:04:05"),
	)
	return label, best
}

// Exact expiry match by timestamp + select within ATM ±20%
func filterOptionsByTS(instruments []Instrument, expiryUTC time.Time, atmPrice float64) []string {
	type opt struct {
		name     string
		strike   float64
		distance float64
		isCall   bool
	}
	var list []opt

	for _, inst := range instruments {
		if !inst.IsActive {
			continue
		}
		t := time.UnixMilli(inst.ExpireMs).UTC()
		if !t.Equal(expiryUTC) {
			continue
		}
		parts := strings.Split(inst.Name, "-")
		if len(parts) < 4 {
			continue
		}
		var s float64
		if _, err := fmt.Sscanf(parts[2], "%f", &s); err != nil {
			continue
		}
		distance := math.Abs(s - atmPrice)
		if distance > atmPrice*0.2 {
			continue
		}
		isCall := parts[3] == "C"
		list = append(list, opt{inst.Name, s, distance, isCall})
	}

	sort.Slice(list, func(i, j int) bool { return list[i].distance < list[j].distance })

	callCap := data.MaxOptions / 2
	putCap := data.MaxOptions / 2
	callCount, putCount := 0, 0
	balanced := make([]opt, 0, len(list))
	for _, o := range list {
		if o.isCall && callCount < callCap {
			balanced = append(balanced, o)
			callCount++
		} else if !o.isCall && putCount < putCap {
			balanced = append(balanced, o)
			putCount++
		}
		if len(balanced) >= data.MaxOptions {
			break
		}
	}

	out := make([]string, len(balanced))
	for i, o := range balanced {
		out[i] = o.name
	}
	log.Printf("[INFO] Filtered to %d options (%d calls, %d puts) within %.0f%% of ATM",
		len(out), callCount, putCount, 20.0)
	return out
}
