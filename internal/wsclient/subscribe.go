package wsclient

import (
	"encoding/json"
	"log"

	"github.com/gorilla/websocket"
)

type subscribeRequest struct {
	JSONRPC string `json:"jsonrpc"`
	ID      int    `json:"id"`
	Method  string `json:"method"`
	Params  struct {
		Channels []string `json:"channels"`
	} `json:"params"`
}

func SubscribeMultiple(ws *websocket.Conn, topics []string) {
	req := subscribeRequest{
		JSONRPC: "2.0",
		ID:      42,
		Method:  "public/subscribe",
	}
	req.Params.Channels = topics

	// ✅ 채널명 로깅 (raw 여부 확인)
	for i, ch := range topics {
		if len(ch) > 4 && ch[len(ch)-4:] == ".raw" {
			log.Printf("[SUBSCRIBE MULTI] Channel[%d]: %s (✅ raw suffix detected)", i, ch)
		} else {
			log.Printf("[SUBSCRIBE MULTI] Channel[%d]: %s (⚠️ no raw suffix)", i, ch)
		}
	}

	if err := ws.WriteJSON(req); err != nil {
		log.Println("[SUBSCRIBE MULTI FAIL]", err)
	} else {
		log.Printf("[SUBSCRIBE MULTI] Sent %d channels", len(topics))
	}

	// ✅ 구독 응답 읽고 로깅
	_, msg, err := ws.ReadMessage()
	if err != nil {
		log.Println("[SUBSCRIBE MULTI] Error reading response:", err)
		return
	}
	log.Printf("[SUBSCRIBE MULTI] Response Raw: %s", string(msg))

	var resp map[string]any
	if err := json.Unmarshal(msg, &resp); err == nil {
		if errObj, ok := resp["error"]; ok {
			log.Printf("[SUBSCRIBE MULTI] Subscribe Error: %+v", errObj)
		} else {
			log.Println("[SUBSCRIBE MULTI] Subscribe succeeded")
		}
	}
}
