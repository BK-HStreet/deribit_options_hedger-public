// File: cmd/main.go - BoxSignal 구조체 맞춰서 수정
package main

import (
	"Options_Hedger/internal/auth"
	"Options_Hedger/internal/data"
	"Options_Hedger/internal/fix"
	"Options_Hedger/internal/strategy"
	"math"

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

type Instrument struct {
	Name     string `json:"instrument_name"`
	IsActive bool   `json:"is_active"`
	ExpireMs int64  `json:"expiration_timestamp"`
}

func main() {
	if err := godotenv.Load(); err == nil {
		log.Println("[INFO] .env loaded successfully")
	}

	// ✅ HFT 최적화: 단일 코어 사용 권장 (컨텍스트 스위칭 최소화)
	runtime.GOMAXPROCS(1) // HFT는 보통 단일 코어 최적화
	runtime.LockOSThread()

	clientID := os.Getenv("DERIBIT_CLIENT_ID")
	clientSecret := os.Getenv("DERIBIT_CLIENT_SECRET")
	if clientID == "" || clientSecret == "" {
		log.Fatal("[AUTH] missing DERIBIT_CLIENT_ID or DERIBIT_CLIENT_SECRET")
	}

	_ = auth.FetchJWTToken(clientID, clientSecret)

	log.Printf("[INFO] Shared memory base pointer: 0x%x", data.SharedMemoryPtr())

	btcPrice := fetchBTCPrice()
	log.Printf("[INFO] BTC Price: %.2f", btcPrice)
	data.SetIndexPrice(btcPrice) // ✅ HFT 버전 함수명 수정
	instruments := fetchInstruments()
	nearestLabel, expiryUTC := findNearestExpiryUTC(instruments)
	options := filterOptionsByTS(instruments, expiryUTC, btcPrice)
	if len(options) > data.MaxOptions {
		options = options[:data.MaxOptions]
	}
	log.Printf("[INFO] Selected %d options from expiry %s", len(options), nearestLabel)

	fix.SetOptionSymbols(options)

	// ✅ HFT 최적화: 큰 버퍼와 함께 Update 채널
	updateCh := make(chan data.Update, 2048)
	data.InitOrderBooks(options, updateCh)

	// ✅ HFT 박스 스프레드 엔진 초기화
	engine := strategy.NewBoxSpreadHFT(updateCh)
	engine.InitializeHFT(options)

	// ✅ 엔진 실행
	go engine.Run()

	// ✅ 신호 처리 - BoxSignal 구조체에 맞춰 수정
	go func() {
		for sig := range engine.Signals() {
			// HFT BoxSignal 구조체 사용
			lowCallSym := data.GetSymbolName(int32(sig.LowCallIdx))
			lowPutSym := data.GetSymbolName(int32(sig.LowPutIdx))
			highCallSym := data.GetSymbolName(int32(sig.HighCallIdx))
			highPutSym := data.GetSymbolName(int32(sig.HighPutIdx))

			log.Printf("[BOX_SIGNAL] Profit=$%.2f Strikes:[%.0f-%.0f] "+
				"BuyCall:%s SellPut:%s SellCall:%s BuyPut:%s LatencyNs:%d",
				sig.Profit, sig.LowStrike, sig.HighStrike,
				lowCallSym, lowPutSym, highCallSym, highPutSym,
				time.Now().UnixNano()-sig.UpdateTimeNs)
		}
	}()

	// ✅ 주기적으로 신호 마스크 리셋 (HFT 중복 방지)
	go func() {
		ticker := time.NewTicker(100 * time.Millisecond) // 100ms마다 리셋
		defer ticker.Stop()
		for range ticker.C {
			engine.ResetSignalMask()
		}
	}()

	// ✅ FIX 엔진 시작
	if err := fix.InitFIXEngine("config/quickfix.cfg"); err != nil {
		log.Printf("[FIX] Init failed: %v", err)
	}
	defer fix.StopFIXEngine()

	// 종료 신호 대기
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig

	log.Println("[MAIN] Shutting down...")
}

// Fetch BTC index price via Deribit REST
func fetchBTCPrice() float64 {
	res, err := http.Get("https://www.deribit.com/api/v2/public/get_index_price?index_name=btc_usd")
	if err != nil {
		log.Printf("[PRICE] fetch failed, using default: %v", err)
		return 65000.0 // 기본값
	}
	defer res.Body.Close()
	var r struct {
		Result struct {
			IndexPrice float64 `json:"index_price"`
		}
	}
	if err := json.NewDecoder(res.Body).Decode(&r); err != nil {
		log.Printf("[PRICE] decode failed, using default: %v", err)
		return 65000.0
	}
	return r.Result.IndexPrice
}

// Fetch all active BTC option instruments
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

// // "15AUG25" 같은 만기 라벨 → 그날의 08:00 UTC (= KST 17:00)
// func expiryUTCFromLabel(label string) (time.Time, error) {
// 	const layout = "02Jan06"
// 	d, err := time.Parse(layout, label) // UTC 기준으로 Y/M/D만 파싱
// 	if err != nil {
// 		return time.Time{}, err
// 	}
// 	// 그 날짜의 08:00 UTC가 곧 KST 17:00
// 	return time.Date(d.Year(), d.Month(), d.Day(), 8, 0, 0, 0, time.UTC), nil
// }

func findNearestExpiryUTC(instruments []Instrument) (label string, expiryUTC time.Time) {
	nowUTC := time.Now().UTC()
	var best time.Time
	seen := make(map[string]bool)

	for _, inst := range instruments {
		t := time.UnixMilli(inst.ExpireMs).UTC() // ← 서버가 주는 진짜 만기
		if !t.After(nowUTC) {                    // 지나간 건 제외(= 같으면 제외하고 싶으면 !t.Before(nowUTC)로)
			continue
		}
		// label은 심볼에서 빼거나 t로부터 만들기
		parts := strings.Split(inst.Name, "-") // BTC-10AUG25-115000-C
		if len(parts) < 3 {
			continue
		}
		lbl := parts[1]

		// 하루에 옵션이 여러 개라서 같은 만기가 반복되므로, 최초 한 번만 후보에 올리면 충분
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

	// 확인용 로그(원하면 KST 표시 제거 가능)
	kst := time.FixedZone("KST", 9*3600)
	log.Printf("[INFO] Nearest expiry: %s (UTC %s / KST %s)",
		label,
		best.Format(time.RFC3339),
		best.In(kst).Format("2006-01-02 15:04:05"),
	)
	return label, best
}

// // Find nearest expiry from the instrument list
// func findNearestExpiry(instruments []string) string {
// 	nowUTC := time.Now().UTC()

// 	// 라벨 -> 만기(UTC 08:00) 중 아직 안 지난 것만
// 	m := make(map[string]time.Time)
// 	for _, name := range instruments {
// 		parts := strings.Split(name, "-") // e.g. BTC-15AUG25-115000-C
// 		if len(parts) < 3 {
// 			continue
// 		}
// 		lbl := parts[1]
// 		if _, ok := m[lbl]; ok {
// 			continue
// 		}

// 		tUTC, err := expiryUTCFromLabel(lbl)
// 		if err == nil && tUTC.After(nowUTC) {
// 			m[lbl] = tUTC
// 		}
// 	}
// 	if len(m) == 0 {
// 		log.Fatal("[EXPIRY] no future expiries found")
// 	}

// 	type item struct {
// 		lbl string
// 		t   time.Time
// 	}
// 	list := make([]item, 0, len(m))
// 	for lbl, t := range m {
// 		list = append(list, item{lbl, t})
// 	}
// 	sort.Slice(list, func(i, j int) bool { return list[i].t.Before(list[j].t) })

//		kst := time.FixedZone("KST", 9*3600)
//		log.Printf("[INFO] Nearest expiry: %s (UTC %s / KST %s)",
//			list[0].lbl,
//			list[0].t.Format(time.RFC3339),
//			list[0].t.In(kst).Format("2006-01-02 15:04:05"),
//		)
//		return list[0].lbl
//	}
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
		if !t.Equal(expiryUTC) { // 정확히 그 만기만
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

// // ✅ ATM 필터링 최적화
// func filterOptions(instruments []string, expiry string, atmPrice float64) []string {
// 	type opt struct {
// 		name     string
// 		strike   float64
// 		distance float64
// 		isCall   bool
// 	}

// 	var list []opt
// 	for _, name := range instruments {
// 		if !strings.Contains(name, expiry) {
// 			continue
// 		}

// 		parts := strings.Split(name, "-")
// 		if len(parts) < 4 {
// 			continue
// 		}

// 		var s float64
// 		if _, err := fmt.Sscanf(parts[2], "%f", &s); err != nil {
// 			continue
// 		}

// 		// ✅ ATM 근처 옵션만 선택 (±20% 범위)
// 		distance := abs(s - atmPrice)
// 		if distance > atmPrice*0.2 { // 20% 범위 벗어나면 스킵
// 			continue
// 		}

// 		isCall := parts[3] == "C"
// 		list = append(list, opt{name, s, distance, isCall})
// 	}

// 	// ✅ ATM 거리순 정렬
// 	sort.Slice(list, func(i, j int) bool {
// 		return list[i].distance < list[j].distance
// 	})

// 	// ✅ Call/Put 균형 맞추기 (박스 스프레드를 위해)
// 	callCount := 0
// 	putCount := 0
// 	balanced := make([]opt, 0, len(list))

// 	for _, o := range list {
// 		if o.isCall && callCount < data.MaxOptions/2 {
// 			balanced = append(balanced, o)
// 			callCount++
// 		} else if !o.isCall && putCount < data.MaxOptions/2 {
// 			balanced = append(balanced, o)
// 			putCount++
// 		}

// 		if len(balanced) >= data.MaxOptions {
// 			break
// 		}
// 	}

// 	out := make([]string, len(balanced))
// 	for i, o := range balanced {
// 		out[i] = o.name
// 	}

// 	log.Printf("[INFO] Filtered to %d options (%d calls, %d puts) within %.0f%% of ATM",
// 		len(out), callCount, putCount, 20.0)

// 	return out
// }

func abs(x float64) float64 {
	if x < 0 {
		return -x
	}
	return x
}
