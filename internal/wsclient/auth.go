package wsclient

import (
	"encoding/json"
	"log"
	"os"

	"github.com/gorilla/websocket"
)

func AuthenticateWebSocket(ws *websocket.Conn) error {
	clientID := os.Getenv("DERIBIT_CLIENT_ID")
	clientSecret := os.Getenv("DERIBIT_CLIENT_SECRET")

	if clientID == "" || clientSecret == "" {
		log.Println("[WS] Missing DERIBIT_CLIENT_ID or DERIBIT_CLIENT_SECRET")
	}

	req := map[string]any{
		"jsonrpc": "2.0",
		"id":      9999,
		"method":  "public/auth",
		"params": map[string]string{
			"grant_type":    "client_credentials",
			"client_id":     clientID,
			"client_secret": clientSecret,
		},
	}

	log.Printf("[WS] Sending WS Auth request (client_id=%s)", clientID)
	if err := ws.WriteJSON(req); err != nil {
		log.Println("[WS] Auth request send failed:", err)
		return err
	}

	// ✅ 인증 응답을 즉시 읽고 로깅
	_, msg, err := ws.ReadMessage()
	if err != nil {
		log.Println("[WS] Error reading Auth response:", err)
		return err
	}
	log.Printf("[WS] Auth Response Raw: %s", string(msg))

	var resp map[string]any
	if err := json.Unmarshal(msg, &resp); err == nil {
		if errObj, ok := resp["error"]; ok {
			log.Printf("[WS] Auth Error: %+v", errObj)
		} else {
			log.Println("[WS] WebSocket authentication succeeded")
		}
	}

	return nil
}
