package api

import (
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// WSEvent is broadcast to all connected WebSocket clients.
type WSEvent struct {
	Type      string    `json:"type"`    // "state_transition" | "service_update" | "log_entry" | "logs_end"
	Payload   any       `json:"payload"` // typed by Type; nil for "logs_end"
	Timestamp time.Time `json:"timestamp"`
}

// StateTransitionPayload is the Payload for "state_transition" events.
type StateTransitionPayload struct {
	ExecutionID string `json:"execution_id"`
	StateName   string `json:"state_name"`
	FromStatus  string `json:"from_status"`
	ToStatus    string `json:"to_status"`
	Error       string `json:"error,omitempty"`
}

// ServiceUpdatePayload is the Payload for "service_update" events.
type ServiceUpdatePayload struct {
	ServiceName string `json:"service_name"`
	Status      string `json:"status"`
}

// LogEntryPayload is the Payload for "log_entry" events.
type LogEntryPayload struct {
	LogID       string    `json:"log_id"`
	ExecutionID string    `json:"execution_id"`
	ServiceName string    `json:"service_name"`
	StateName   string    `json:"state_name"`
	Level       string    `json:"level"`
	Message     string    `json:"message"`
	OccurredAt  time.Time `json:"occurred_at"`
}

var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	CheckOrigin: func(r *http.Request) bool {
		// Origin validation is enforced by auth middleware (Phase 11).
		// In dev mode (no API key) allow all origins.
		return true
	},
}

// WSHub manages all active WebSocket connections and broadcasts events.
// Broadcasts are best-effort: slow or closed clients are dropped.
type WSHub struct {
	clients map[*websocket.Conn]struct{}
	subs    map[chan WSEvent]struct{}
	mu      sync.RWMutex
}

// NewWSHub creates a ready-to-use WSHub.
func NewWSHub() *WSHub {
	return &WSHub{
		clients: make(map[*websocket.Conn]struct{}),
		subs:    make(map[chan WSEvent]struct{}),
	}
}

// subscribe returns a channel that receives a copy of every Broadcast event.
func (h *WSHub) subscribe() chan WSEvent {
	ch := make(chan WSEvent, 64)
	h.mu.Lock()
	h.subs[ch] = struct{}{}
	h.mu.Unlock()
	return ch
}

// unsubscribe removes and closes a subscriber channel.
func (h *WSHub) unsubscribe(ch chan WSEvent) {
	h.mu.Lock()
	delete(h.subs, ch)
	h.mu.Unlock()
	close(ch)
}

// Register adds a WebSocket connection to the hub.
func (h *WSHub) Register(conn *websocket.Conn) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.clients[conn] = struct{}{}
}

// Unregister removes a WebSocket connection from the hub.
func (h *WSHub) Unregister(conn *websocket.Conn) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.clients, conn)
}

// Broadcast sends event as JSON to all registered clients. Clients that fail
// to receive the message within the write deadline are silently dropped.
func (h *WSHub) Broadcast(event WSEvent) {
	h.mu.RLock()
	conns := make([]*websocket.Conn, 0, len(h.clients))
	for c := range h.clients {
		conns = append(conns, c)
	}
	h.mu.RUnlock()

	deadline := time.Now().Add(2 * time.Second)
	var failed []*websocket.Conn
	for _, c := range conns {
		if err := c.SetWriteDeadline(deadline); err != nil {
			failed = append(failed, c)
			continue
		}
		if err := c.WriteJSON(event); err != nil {
			failed = append(failed, c)
		}
	}

	if len(failed) > 0 {
		h.mu.Lock()
		for _, c := range failed {
			delete(h.clients, c)
			if err := c.Close(); err != nil {
				log.Printf("ws_hub: close failed client: %v", err)
			}
		}
		h.mu.Unlock()
	}

	// Fan out to event subscribers (non-blocking; slow subscribers drop events).
	h.mu.RLock()
	for ch := range h.subs {
		select {
		case ch <- event:
		default:
		}
	}
	h.mu.RUnlock()
}

// ServeWS upgrades the HTTP request to a WebSocket and registers the connection.
func (h *WSHub) ServeWS(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("ws_hub: upgrade: %v", err)
		return
	}
	h.Register(conn)

	// Drain incoming messages and unregister on close.
	go func() {
		defer h.Unregister(conn)
		defer conn.Close()
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	}()
}
