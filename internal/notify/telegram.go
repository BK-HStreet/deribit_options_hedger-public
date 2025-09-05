package notify

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/joho/godotenv"
)

type Notifier interface {
	Send(ctx context.Context, text string) error
}

type Telegram struct {
	token   string
	chatID  int64
	client  *http.Client
	apiBase string
}

func NewTelegramFromEnv() (Notifier, error) {
	tok := os.Getenv("TELEGRAM_BOT_TOKEN")
	cid := os.Getenv("TELEGRAM_CHAT_ID")

	// Re-call .env if empty for testing purpose
	if tok == "" || cid == "" {
		_ = godotenv.Load() // no overwriting; load if exists
		if tok == "" {
			tok = os.Getenv("TELEGRAM_BOT_TOKEN")
		}
		if cid == "" {
			cid = os.Getenv("TELEGRAM_CHAT_ID")
		}
	}

	if tok == "" || cid == "" {
		return nil, fmt.Errorf("missing TELEGRAM_BOT_TOKEN or TELEGRAM_CHAT_ID")
	}

	chatID, err := strconv.ParseInt(cid, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("invalid TELEGRAM_CHAT_ID: %w", err)
	}

	return &Telegram{
		token:   tok,
		chatID:  chatID,
		client:  &http.Client{Timeout: 3 * time.Second},
		apiBase: "https://api.telegram.org",
	}, nil
}

func (t *Telegram) Send(ctx context.Context, text string) error {
	form := url.Values{}
	form.Set("chat_id", strconv.FormatInt(t.chatID, 10))
	form.Set("text", text)
	form.Set("parse_mode", "HTML")

	req, err := http.NewRequestWithContext(
		ctx, "POST",
		fmt.Sprintf("%s/bot%s/sendMessage", t.apiBase, t.token),
		strings.NewReader(form.Encode()),
	)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := t.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode/100 != 2 {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("telegram sendMessage failed: %d %s", resp.StatusCode, string(b))
	}
	return nil
}
