package wsclient

import (
	"log"
	"time"

	"OptionsHedger/internal/model"

	"github.com/gorilla/websocket"
)

func ConnectAndServe(url string, topics []string, handler func(*model.Depth)) {
	for {
		ws, _, err := websocket.DefaultDialer.Dial(url, nil)
		if err != nil {
			log.Println("[WS] Dial error:", err)
			time.Sleep(time.Second)
			continue
		}
		log.Println("[WS] Connected")

		// ✅ API Key + Secret 기반 WS 인증
		if err := AuthenticateWebSocket(ws); err != nil {
			log.Println("[WS] Auth send failed:", err)
			ws.Close()
			time.Sleep(time.Second)
			continue
		}
		if err := WaitForAuthSuccess(ws); err != nil {
			log.Println("[WS] Auth failed:", err)
			ws.Close()
			time.Sleep(time.Second)
			continue
		}

		// 구독 후 Read Loop
		SubscribeMultiple(ws, topics)
		ReadLoop(ws, handler)

		ws.Close()
		log.Println("[WS] Disconnected; reconnecting…")
		time.Sleep(time.Second)
	}
}
