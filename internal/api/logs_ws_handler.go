package api

import (
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/pastorenue/kflow/internal/telemetry"
)

// LogSubscriber is a per-connection live log stream subscriber.
type LogSubscriber struct {
	filter telemetry.LogFilter
	ch     chan telemetry.LogRow
}

// LogHub manages live log subscriptions and fans out new log rows.
type LogHub struct {
	mu          sync.RWMutex
	subscribers map[*LogSubscriber]struct{}
}

// NewLogHub creates a ready-to-use LogHub.
func NewLogHub() *LogHub {
	return &LogHub{subscribers: make(map[*LogSubscriber]struct{})}
}

// Subscribe registers a new subscriber with the given filter.
func (h *LogHub) Subscribe(f telemetry.LogFilter) *LogSubscriber {
	s := &LogSubscriber{filter: f, ch: make(chan telemetry.LogRow, 64)}
	h.mu.Lock()
	h.subscribers[s] = struct{}{}
	h.mu.Unlock()
	return s
}

// Unsubscribe removes a subscriber and closes its channel.
func (h *LogHub) Unsubscribe(s *LogSubscriber) {
	h.mu.Lock()
	delete(h.subscribers, s)
	h.mu.Unlock()
	close(s.ch)
}

// Publish fans out a log row to all matching subscribers.
// Non-blocking: slow subscribers are silently dropped.
func (h *LogHub) Publish(row telemetry.LogRow) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	for sub := range h.subscribers {
		if matchesLogFilter(row, sub.filter) {
			select {
			case sub.ch <- row:
			default:
			}
		}
	}
}

// ServeLogsWSHandler returns an HTTP handler that streams logs over WebSocket.
// GET /api/v1/ws/logs?execution_id=&service_name=&state_name=&level=&since=&q=&offset=&limit=
func (h *LogHub) ServeLogsWSHandler(ch *telemetry.Client) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			log.Printf("logs_ws: upgrade: %v", err)
			return
		}
		defer conn.Close()

		q := r.URL.Query()
		filter := telemetry.LogFilter{
			ExecutionID: q.Get("execution_id"),
			ServiceName: q.Get("service_name"),
			StateName:   q.Get("state_name"),
			Level:       q.Get("level"),
			Query:       q.Get("q"),
			Limit:       parseIntParam(q.Get("limit"), 50),
			Offset:      parseIntParam(q.Get("offset"), 0),
		}
		if s := q.Get("since"); s != "" {
			if t, err := time.Parse(time.RFC3339, s); err == nil {
				filter.Since = &t
			}
		}

		// Stream historical logs in chronological order.
		if ch != nil {
			rows, _, err := ch.QueryLogs(r.Context(), filter)
			if err != nil {
				log.Printf("logs_ws: query historical: %v", err)
			}
			// QueryLogs returns DESC; reverse for chronological order.
			for i := len(rows) - 1; i >= 0; i-- {
				writeLogEntryWS(conn, rows[i])
			}
		}

		// Signal end of history replay.
		_ = conn.SetWriteDeadline(time.Now().Add(2 * time.Second))
		_ = conn.WriteJSON(WSEvent{Type: "logs_end", Timestamp: time.Now()})

		// Register as live subscriber.
		sub := h.Subscribe(filter)
		defer h.Unsubscribe(sub)

		// Read pump: detect disconnect.
		done := make(chan struct{})
		go func() {
			defer close(done)
			for {
				if _, _, err := conn.ReadMessage(); err != nil {
					return
				}
			}
		}()

		for {
			select {
			case <-done:
				return
			case row, ok := <-sub.ch:
				if !ok {
					return
				}
				writeLogEntryWS(conn, row)
			}
		}
	}
}

func writeLogEntryWS(conn *websocket.Conn, row telemetry.LogRow) {
	_ = conn.SetWriteDeadline(time.Now().Add(2 * time.Second))
	_ = conn.WriteJSON(WSEvent{
		Type:      "log_entry",
		Timestamp: row.OccurredAt,
		Payload: LogEntryPayload{
			LogID:       row.LogID,
			ExecutionID: row.ExecutionID,
			ServiceName: row.ServiceName,
			StateName:   row.StateName,
			Level:       row.Level,
			Message:     row.Message,
			OccurredAt:  row.OccurredAt,
		},
	})
}

func matchesLogFilter(row telemetry.LogRow, f telemetry.LogFilter) bool {
	if f.ExecutionID != "" && row.ExecutionID != f.ExecutionID {
		return false
	}
	if f.ServiceName != "" && row.ServiceName != f.ServiceName {
		return false
	}
	if f.StateName != "" && row.StateName != f.StateName {
		return false
	}
	if f.Level != "" && row.Level != f.Level {
		return false
	}
	if f.Query != "" && !strings.Contains(row.Message, f.Query) {
		return false
	}
	return true
}

