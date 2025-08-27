// File: internal/app/strategy_factory.go
package app

import (
	"Options_Hedger/internal/data"
	"Options_Hedger/internal/notify"
	"Options_Hedger/internal/servers"
	"Options_Hedger/internal/strategy"
	"bufio"
	"context"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
	"time"
)

type Kind int

const (
	KindBox Kind = iota + 1
	KindProtective
	KindBudgeted
)

type Handle struct {
	Name string
	Stop func(ctx context.Context)
}

func ChooseStrategy() Kind {
	// 1) 환경변수 우선
	if s := strings.ToLower(strings.TrimSpace(os.Getenv("STRATEGY"))); s != "" {
		switch s {
		case "1", "box_spread", "box", "boxspread":
			log.Printf("[STRATEGY] selected=%s (source=STRATEGY=%q)", kindName(KindBox), s)
			return KindBox
		case "2", "protective_collar", "collar", "protective":
			log.Printf("[STRATEGY] selected=%s (source=STRATEGY=%q)", kindName(KindProtective), s)
			return KindProtective
		case "3", "budgeted_collar", "budget_collar", "budgeted":
			log.Printf("[STRATEGY] selected=%s (source=STRATEGY=%q)", kindName(KindBudgeted), s)
			return KindBudgeted
		default:
			log.Printf("[STRATEGY] unknown STRATEGY=%q → fallback", s)
		}
	}
	if n := strings.TrimSpace(os.Getenv("STRATEGY_NUM")); n != "" {
		switch n {
		case "2":
			log.Printf("[STRATEGY] selected=%s (source=STRATEGY_NUM=%q)", kindName(KindProtective), n)
			return KindProtective
		case "3":
			log.Printf("[STRATEGY] selected=%s (source=STRATEGY_NUM=%q)", kindName(KindBudgeted), n)
			return KindBudgeted
		case "1":
			log.Printf("[STRATEGY] selected=%s (source=STRATEGY_NUM=%q)", kindName(KindBox), n)
			return KindBox
		default:
			log.Printf("[STRATEGY] unknown STRATEGY_NUM=%q → fallback", n)
		}
	}
	// 2) 인터랙티브 폴백(터미널에서 실행 중일 때만)
	if isInteractiveStdin() {
		reader := bufio.NewReader(os.Stdin)
		fmt.Println()
		fmt.Println("전략을 선택하세요:")
		fmt.Println("  1) Box Spread (HFT)")
		fmt.Println("  2) Protective Collar")
		fmt.Println("  3) Budgeted Protective Collar")
		fmt.Print("번호 입력 [기본=1]: ")
		line, _ := reader.ReadString('\n')
		switch strings.TrimSpace(line) {
		case "2":
			log.Printf("[STRATEGY] selected=%s (interactive)", kindName(KindProtective))
			return KindProtective
		case "3":
			log.Printf("[STRATEGY] selected=%s (interactive)", kindName(KindBudgeted))
			return KindBudgeted
		default:
			log.Printf("[STRATEGY] selected=%s (interactive default)", kindName(KindBox))
			return KindBox
		}
	}
	// 3) 완전 무인 실행 시 기본값
	log.Printf("[STRATEGY] selected=%s (default)", kindName(KindBox))
	return KindBox
}

func isInteractiveStdin() bool {
	fi, err := os.Stdin.Stat()
	if err != nil {
		return false
	}
	return (fi.Mode() & os.ModeCharDevice) != 0
}

func kindName(k Kind) string {
	switch k {
	case KindProtective:
		return "protective_collar"
	case KindBudgeted:
		return "budgeted_collar"
	default:
		return "box_spread"
	}
}

func StartEngine(kind Kind, updatesCh chan data.Update, symbols []string, ntf notify.Notifier) *Handle {
	switch kind {
	case KindProtective:
		pc := strategy.NewProtectiveCollar(updatesCh)
		pc.InitializeHFT(symbols)
		pc.SetNotifier(ntf)
		go pc.Run()
		log.Printf("Protective collar started..")

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
				if ntf != nil {
					ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
					_ = ntf.Send(ctx, msg)
					cancel()
				}
				fmt.Print("\a")
			}
		}()

		return &Handle{Name: "protective_collar", Stop: nil}

	case KindBudgeted:
		bc := strategy.NewBudgetedProtectiveCollar(updatesCh)
		bc.InitializeHFT(symbols)
		bc.SetIndexSource(parseIndexSource())
		// (옵션) 테스트 타깃 ENV → 운용프로그램 POST 없이 즉시 동작
		if t, ok := parseTestTarget(os.Getenv("HEDGE_TEST_TARGET")); ok {
			bc.SetTarget(t)
			log.Printf("[TEST] HEDGE_TEST_TARGET applied: side=%d qty=%.8f base=%.2f", t.Side, t.QtyBTC, t.BaseUSD)
		}
		go bc.Run()
		log.Printf("Budgeted protective collar started.. (index_src=%s)", os.Getenv("HEDGE_INDEX_SRC"))

		// 신호 소비/로그 (한 곳에서만 구독; 중복 구독 금지)
		go func() {
			for s := range bc.Signals() {
				if s.CloseAll {
					log.Printf("[BUDGETED-COLLAR] CLOSE_ALL exp=%d S=%.2f", s.Expiry, s.IndexPrice)
					continue
				}
				sell := s.SellLeg
				sellType := "PUT"
				if sell.IsCall {
					sellType = "CALL"
				}
				log.Printf("[BUDGETED-COLLAR] SIDE=%s EXP=%d S=%.2f BASE=%.2f | SELL %s K=%.0f Q=%.6f",
					map[int8]string{+1: "LONG_HEDGE", -1: "SHORT_HEDGE"}[s.Side],
					s.Expiry, s.IndexPrice, s.BaseUSD, sellType, sell.Strike, sell.Qty,
				)
				for i := 0; i < s.BuyLegN; i++ {
					bl := s.BuyLegs[i]
					blType := "PUT"
					if bl.IsCall {
						blType = "CALL"
					}
					log.Printf("  BUY %s K=%.0f Q=%.6f", blType, bl.Strike, bl.Qty)
				}
			}
		}()

		// 외부 타깃 수신 HTTP 서버
		servers.ServeHedgeHTTP(bc)

		return &Handle{Name: "budgeted_collar", Stop: nil}

	default: // KindBox
		eng := strategy.NewBoxSpreadHFT(updatesCh)
		eng.InitializeHFT(symbols)
		go eng.Run()
		log.Printf("Box spread started..")

		go func() {
			for sig := range eng.Signals() {
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
				if ntf != nil {
					ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
					_ = ntf.Send(ctx, msg)
					cancel()
				}
				fmt.Print("\a")
			}
		}()

		return &Handle{Name: "box_spread", Stop: nil}
	}
}

func tern(b bool, x, y string) string {
	if b {
		return x
	}
	return y
}

// HEDGE_INDEX_SRC: "", "update"(기본), "shared", "target"
func parseIndexSource() strategy.IndexSource {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("HEDGE_INDEX_SRC"))) {
	case "shared":
		return strategy.IndexFromShared
	case "target":
		return strategy.IndexFromTarget
	default:
		return strategy.IndexFromUpdate
	}
}

// HEDGE_TEST_TARGET 예: "LONG,0.2511,102580" 또는 "SHORT,0.15,95100"
func parseTestTarget(s string) (strategy.HedgeTarget, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return strategy.HedgeTarget{}, false
	}
	parts := strings.Split(s, ",")
	if len(parts) != 3 {
		return strategy.HedgeTarget{}, false
	}
	sideStr := strings.ToUpper(strings.TrimSpace(parts[0]))
	var side int8
	switch sideStr {
	case "LONG":
		side = 1
	case "SHORT":
		side = -1
	default:
		return strategy.HedgeTarget{}, false
	}
	qty, err1 := strconv.ParseFloat(strings.TrimSpace(parts[1]), 64)
	base, err2 := strconv.ParseFloat(strings.TrimSpace(parts[2]), 64)
	if err1 != nil || err2 != nil || qty <= 0 || base <= 0 {
		return strategy.HedgeTarget{}, false
	}
	return strategy.HedgeTarget{Side: side, QtyBTC: qty, BaseUSD: base, Seq: 1}, true
}
