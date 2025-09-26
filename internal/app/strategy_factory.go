package app

import (
	"Options_Hedger/internal/data"
	"Options_Hedger/internal/notify"
	"Options_Hedger/internal/strategy"
	"bufio"
	"context"
	"fmt"
	"log"
	"os"
	"strings"
	"time"
)

type Kind int

const (
	KindBox Kind = iota + 1
)

type Handle struct {
	Name string
	Stop func(ctx context.Context)
}

// Single-strategy build (Box): regardless of inputs, always select Box.

func ChooseStrategy() Kind {
	// 1) Taking .env value first
	if s := strings.ToLower(strings.TrimSpace(os.Getenv("STRATEGY"))); s != "" {
		switch s {
		case "1", "box_spread", "box", "boxspread":
			log.Printf("[STRATEGY] selected=%s (source=STRATEGY=%q)", kindName(KindBox), s)
			return KindBox
		default:
			log.Printf("[STRATEGY] unknown STRATEGY=%q → fallback", s)
		}
	}
	// 2) Taking number from .env
	if n := strings.TrimSpace(os.Getenv("STRATEGY_NUM")); n != "" {
		switch n {
		case "1":
			log.Printf("[STRATEGY] selected=%s (source=STRATEGY_NUM=%q)", kindName(KindBox), n)
			return KindBox
		default:
			log.Printf("[STRATEGY] unknown STRATEGY_NUM=%q → fallback", n)
		}
	}
	// 3) Interactive pull back
	if isInteractiveStdin() {
		reader := bufio.NewReader(os.Stdin)
		fmt.Println()
		fmt.Println("Select Strategy:")
		fmt.Println("  1) Box Spread (HFT)")
		fmt.Print("Enter number [Default=1]: ")
		line, _ := reader.ReadString('\n')
		switch strings.TrimSpace(line) {
		default:
			log.Printf("[STRATEGY] selected=%s (interactive default)", kindName(KindBox))
			return KindBox
		}
	}
	// 4) Default
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
	// Only Box is supported in this build.
	return "box_spread"
}

func StartEngine(kind Kind, updatesCh chan data.Update, symbols []string, ntf notify.Notifier) *Handle {
	// Only Box is supported: regardless of 'kind', run Box HFT.
	switch kind {
	default: // KindBox
		eng := strategy.NewBoxSpreadHFT(updatesCh)
		eng.InitializeHFT(symbols)
		go eng.Run()
		log.Printf("Box spread started..")

		// Single consumer: log + Telegram notifications.
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
