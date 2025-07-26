package wsclient

import (
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"OptionsHedger/internal/model"

	"github.com/gorilla/websocket"
)

// WaitForAuthSuccess blocks until an auth response comes back
func WaitForAuthSuccess(ws *websocket.Conn) error {
	for {
		_, msg, err := ws.ReadMessage()
		if err != nil {
			return err
		}
		var resp map[string]interface{}
		if err := json.Unmarshal(msg, &resp); err != nil {
			log.Println("[WS] Malformed auth response:", string(msg))
			continue
		}
		if id, ok := resp["id"].(float64); ok && id == 9999 {
			if errObj, hasErr := resp["error"]; hasErr {
				return fmt.Errorf("[AUTH] Failed: %v", errObj)
			}
			log.Println("[AUTH] WebSocket authentication succeeded")
			return nil
		}
	}
}

var pool = sync.Pool{New: func() interface{} { return new(model.Depth) }}

// ReadLoop reads messages, keeps ping/pong alive, and dispatches Depth to handler
func ReadLoop(ws *websocket.Conn, handler func(*model.Depth)) {
	// set up ping/pong
	ws.SetReadDeadline(time.Now().Add(30 * time.Second))
	ws.SetPongHandler(func(_ string) error {
		ws.SetReadDeadline(time.Now().Add(30 * time.Second))
		return nil
	})
	go func() {
		ticker := time.NewTicker(10 * time.Second)
		defer ticker.Stop()
		for range ticker.C {
			if err := ws.WriteMessage(websocket.PingMessage, nil); err != nil {
				log.Println("[WS] Ping error:", err)
				return
			}
		}
	}()

	// main read loop
	for {
		_, raw, err := ws.ReadMessage()
		if err != nil {
			log.Println("[WS] Read error:", err)
			return
		}
		var msg map[string]interface{}
		if err := json.Unmarshal(raw, &msg); err != nil {
			log.Println("[WS] Malformed message:", string(raw))
			continue
		}
		// skip acks
		if method, ok := msg["method"].(string); ok && method == "subscription" {
			continue
		}
		// handle book messages
		if method, ok := msg["method"].(string); ok && strings.HasPrefix(method, "book") {
			d := pool.Get().(*model.Depth)
			parseDepth(raw, d)
			handler(d)
			pool.Put(d)
		}
	}
}

// parseDepth unmarshals raw JSON into Depth struct
func parseDepth(raw []byte, out *model.Depth) {
	var m struct {
		Params struct {
			Data struct {
				InstrumentName string      `json:"instrument_name"`
				Bids           [][]float64 `json:"bids"`
				Asks           [][]float64 `json:"asks"`
			} `json:"data"`
		} `json:"params"`
	}
	if err := json.Unmarshal(raw, &m); err != nil {
		log.Println("[WS] parseDepth error:", err)
		return
	}
	out.Instrument = m.Params.Data.InstrumentName
	if len(m.Params.Data.Bids) > 0 {
		out.Bid = m.Params.Data.Bids[0][0]
	}
	if len(m.Params.Data.Asks) > 0 {
		out.Ask = m.Params.Data.Asks[0][0]
	}
}
