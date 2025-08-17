// File: cmd/main.go
package main

import (
	"Options_Hedger/internal/auth"
	"Options_Hedger/internal/data"
	"Options_Hedger/internal/fix"
	"Options_Hedger/internal/notify" // ← 권장 구조: 별도 패키지
	"Options_Hedger/internal/strategy"

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

	// HFT: 컨텍스트 스위칭 최소화
	runtime.GOMAXPROCS(1)
	runtime.LockOSThread()

	clientID := os.Getenv("DERIBIT_CLIENT_ID")
	clientSecret := os.Getenv("DERIBIT_CLIENT_SECRET")
	if clientID == "" || clientSecret == "" {
		log.Fatal("[AUTH] missing DERIBIT_CLIENT_ID or DERIBIT_CLIENT_SECRET")
	}
	_ = auth.FetchJWTToken(clientID, clientSecret)

	log.Printf("[INFO] Shared memory base pointer: 0x%x", data.SharedMemoryPtr())

	// 초기 인덱스 가격
	btcPrice := fetchBTCPrice()
	log.Printf("[INFO] BTC Price: %.2f", btcPrice)
	data.SetIndexPrice(btcPrice)

	// 인스트루먼트 조회 → 가장 가까운 만기(UTC 08:00) 선택 → 해당 만기에서 ATM±20% 필터
	instruments := fetchInstruments()
	nearestLabel, expiryUTC := findNearestExpiryUTC(instruments)
	options := filterOptionsByTS(instruments, expiryUTC, btcPrice)
	if len(options) > data.MaxOptions {
		options = options[:data.MaxOptions]
	}
	log.Printf("[INFO] Selected %d options from expiry %s", len(options), nearestLabel)

	// FIX 구독 심볼 세팅
	fix.SetOptionSymbols(options)

	// 업데이트 채널 초기화 및 오더북 세팅
	updateCh := make(chan data.Update, 2048)
	data.InitOrderBooks(options, updateCh)

	// 박스 스프레드 HFT 엔진
	engine := strategy.NewBoxSpreadHFT(updateCh)
	engine.InitializeHFT(options)

	// 텔레그램 노티파이어 (환경변수 없으면 비활성)
	var notifier notify.Notifier
	if n, err := notify.NewTelegramFromEnv(); err != nil {
		log.Printf("[NOTIFY] Telegram disabled: %v", err)
	} else {
		notifier = n
	}

	// 엔진 실행
	go engine.Run()

	// 신호 수신 → 텔레그램 전송 → 삑 → FIX 정리 → 종료
	go func() {
		for sig := range engine.Signals() {
			lowCallSym := data.GetSymbolName(int32(sig.LowCallIdx))
			lowPutSym := data.GetSymbolName(int32(sig.LowPutIdx))
			highCallSym := data.GetSymbolName(int32(sig.HighCallIdx))
			highPutSym := data.GetSymbolName(int32(sig.HighPutIdx))

			// 최신 호가 & 인덱스 읽어 메시지 풍부화
			lowCall := data.ReadDepthFast(int(sig.LowCallIdx))
			lowPut := data.ReadDepthFast(int(sig.LowPutIdx))
			highCall := data.ReadDepthFast(int(sig.HighCallIdx))
			highPut := data.ReadDepthFast(int(sig.HighPutIdx))
			idx := data.GetIndexPrice()

			msg := fmt.Sprintf(
				"[BOX-SPREAD]\n"+
					"strikes=%.0f→%.0f  index=%.2f  profit=$%.2f\n"+
					"buyCallLo: %s  ask@%.4f (qty=%.4f)\n"+
					"sellCallHi: %s  bid@%.4f (qty=%.4f)\n"+
					"sellPutLo: %s  bid@%.4f (qty=%.4f)\n"+
					"buyPutHi: %s  ask@%.4f (qty=%.4f)",
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

			// 삑
			fmt.Print("\a")

			// // FIX 종료 후 프로세스 종료 (defer는 os.Exit로 실행되지 않으니 직접 정리)
			// fix.StopFIXEngine()
			// time.Sleep(50 * time.Millisecond) // 소켓/로그 플러시 여유
			// os.Exit(0)
		}
	}()

	// 중복 신호 비트마스크 주기적 리셋
	go func() {
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()
		for range ticker.C {
			engine.ResetSignalMask()
		}
	}()

	// FIX 시작
	if err := fix.InitFIXEngine("config/quickfix.cfg"); err != nil {
		log.Printf("[FIX] Init failed: %v", err)
	}
	defer fix.StopFIXEngine()

	// 외부 종료 시그널 대기
	sigc := make(chan os.Signal, 1)
	signal.Notify(sigc, syscall.SIGINT, syscall.SIGTERM)
	<-sigc
	log.Println("[MAIN] Shutting down...")
}

// ───────────────────────── helpers ─────────────────────────

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

// UTC 기준 가장 가까운 미래 만기 선택 (서버 제공 expiration_timestamp 사용)
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

	// 참고 로그
	kst := time.FixedZone("KST", 9*3600)
	log.Printf("[INFO] Nearest expiry: %s (UTC %s / KST %s)",
		label,
		best.Format(time.RFC3339),
		best.In(kst).Format("2006-01-02 15:04:05"),
	)
	return label, best
}

// expiration_timestamp(UTC)로 정확하게 필터링 + ATM±20%
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
