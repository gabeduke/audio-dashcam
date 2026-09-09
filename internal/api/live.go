package api

import (
	"log"
	"net/http"
	"time"

	"github.com/gorilla/websocket"
)

var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 8192,
	// Hindsight is a LAN appliance reached by hostname, IP and .local alias,
	// so origin pinning would only break access without adding protection.
	CheckOrigin: func(r *http.Request) bool { return true },
}

const (
	writeWait  = 5 * time.Second
	pongWait   = 60 * time.Second
	pingPeriod = 25 * time.Second
)

// handleLive streams real min/max peak bins plus per-channel dB to the UI.
// This replaces the old SSE endpoint, which sent a single RMS scalar every
// 50ms — a value from which no waveform can be reconstructed.
func (a *API) handleLive(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("[!] websocket upgrade: %v", err)
		return
	}
	defer conn.Close()

	frames, cancel := a.cap.Levels().Subscribe()
	defer cancel()

	// Reader pump: we expect no client messages, but reading is what surfaces
	// close frames and keeps the pong deadline fed.
	go func() {
		defer conn.Close()
		conn.SetReadLimit(512)
		_ = conn.SetReadDeadline(time.Now().Add(pongWait))
		conn.SetPongHandler(func(string) error {
			return conn.SetReadDeadline(time.Now().Add(pongWait))
		})
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	}()

	ping := time.NewTicker(pingPeriod)
	defer ping.Stop()

	for {
		select {
		case <-r.Context().Done():
			return

		case f, ok := <-frames:
			if !ok {
				return
			}
			if len(f.Bins) == 0 {
				continue
			}
			_ = conn.SetWriteDeadline(time.Now().Add(writeWait))
			if err := conn.WriteJSON(f); err != nil {
				return
			}

		case <-ping.C:
			_ = conn.SetWriteDeadline(time.Now().Add(writeWait))
			if err := conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}
