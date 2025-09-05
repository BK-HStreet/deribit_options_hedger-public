// File: cmd/hedger/main.go
package main

import (
	"Options_Hedger/internal/app"
	"Options_Hedger/internal/auth"
	"Options_Hedger/internal/data"
	"Options_Hedger/internal/fix"
	"Options_Hedger/internal/notify"
	"context"
	"log"
	"os"
	"os/signal"
	"runtime"
	"syscall"
	"time"

	"github.com/joho/godotenv"
)

func main() {
	_ = godotenv.Load()

	// HFT: pin to a single OS thread for deterministic scheduling
	runtime.GOMAXPROCS(1)
	runtime.LockOSThread()

	// Auth
	clientID, clientSecret := os.Getenv("DERIBIT_CLIENT_ID"), os.Getenv("DERIBIT_CLIENT_SECRET")
	if clientID == "" || clientSecret == "" {
		log.Fatal("[AUTH] missing DERIBIT_CLIENT_ID or DERIBIT_CLIENT_SECRET")
	}
	_ = auth.FetchJWTToken(clientID, clientSecret)

	log.Printf("[INFO] Shared memory base pointer: 0x%x", data.SharedMemoryPtr())

	// Build option universe
	opts, nearLbl, farLbl := app.BuildUniverse()
	if nearLbl == farLbl {
		log.Printf("[INFO] Selected %d options from expiry %s", len(opts.Symbols), nearLbl)
	} else {
		log.Printf("[INFO] Selected %d options from expiries near=%s, far=%s", len(opts.Symbols), nearLbl, farLbl)
	}

	// Configure FIX subscriptions and initialize order books
	fix.SetOptionSymbols(opts.Symbols)
	updatesCh := make(chan data.Update, 2048)
	data.InitOrderBooks(opts.Symbols, updatesCh)

	// Optional notifier (Telegram)
	var ntf notify.Notifier
	if n, err := notify.NewTelegramFromEnv(); err == nil {
		ntf = n
	}

	// Select and start strategy
	handle := app.StartEngine(app.ChooseStrategy(), updatesCh, opts.Symbols, ntf)

	// Start FIX
	if err := fix.InitFIXEngine("config/quickfix.cfg"); err != nil {
		log.Printf("[FIX] Init failed: %v", err)
	}
	defer fix.StopFIXEngine()

	// Wait for termination signal
	sigc := make(chan os.Signal, 1)
	signal.Notify(sigc, syscall.SIGINT, syscall.SIGTERM)
	<-sigc

	// Graceful shutdown (if needed)
	if handle.Stop != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 750*time.Millisecond)
		defer cancel()
		handle.Stop(ctx)
	}
	log.Println("[MAIN] Shutting down...")
}
