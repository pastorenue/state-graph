package api

import (
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/pastorenue/kflow/internal/store"
	"github.com/pastorenue/kflow/internal/telemetry"
)

func (s *Server) handleListEvents(w http.ResponseWriter, r *http.Request) {
	if s.Telemetry == nil {
		writeJSON(w, http.StatusOK, map[string]any{"events": []any{}})
		return
	}

	id := r.PathValue("id")
	q := r.URL.Query()

	var since *time.Time
	if v := q.Get("since"); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid since timestamp", "bad_request")
			return
		}
		since = &t
	}
	limit := parseIntParam(q.Get("limit"), 100)

	events, err := s.Telemetry.QueryExecutionEvents(r.Context(), id, since, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to query events", "internal")
		return
	}
	if events == nil {
		events = []telemetry.EventRow{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"events": events})
}

func (s *Server) handleListMetrics(w http.ResponseWriter, r *http.Request) {
	if s.Telemetry == nil {
		writeJSON(w, http.StatusOK, map[string]any{"metrics": []any{}})
		return
	}

	name := r.PathValue("name")
	q := r.URL.Query()

	var since, until *time.Time
	if v := q.Get("since"); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid since timestamp", "bad_request")
			return
		}
		since = &t
	}
	if v := q.Get("until"); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid until timestamp", "bad_request")
			return
		}
		until = &t
	}
	limit := parseIntParam(q.Get("limit"), 100)

	metrics, err := s.Telemetry.QueryServiceMetrics(r.Context(), name, since, until, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to query metrics", "internal")
		return
	}
	if metrics == nil {
		metrics = []telemetry.MetricRow{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"metrics": metrics})
}

func (s *Server) handleListLogs(w http.ResponseWriter, r *http.Request) {
	if s.Telemetry == nil {
		// Fallback: synthesize log rows from MongoDB state records so the
		// Logs tab is never empty even when ClickHouse is not configured.
		execID := r.URL.Query().Get("execution_id")
		if execID == "" {
			writeJSON(w, http.StatusOK, map[string]any{"logs": []any{}, "total": 0})
			return
		}
		states, err := s.Store.ListStates(r.Context(), execID)
		if err != nil {
			writeJSON(w, http.StatusOK, map[string]any{"logs": []any{}, "total": 0})
			return
		}
		var rows []telemetry.LogRow
		for _, st := range states {
			rows = append(rows, telemetry.LogRow{
				LogID:       st.ExecutionID + "/" + st.StateName + "/start",
				ExecutionID: st.ExecutionID,
				StateName:   st.StateName,
				Level:       "INFO",
				Message:     fmt.Sprintf("[%s] started", st.StateName),
				OccurredAt:  st.CreatedAt,
			})
			switch st.Status {
			case store.StatusCompleted:
				rows = append(rows, telemetry.LogRow{
					LogID:       st.ExecutionID + "/" + st.StateName + "/complete",
					ExecutionID: st.ExecutionID,
					StateName:   st.StateName,
					Level:       "INFO",
					Message:     fmt.Sprintf("[%s] completed", st.StateName),
					OccurredAt:  st.UpdatedAt,
				})
			case store.StatusFailed:
				rows = append(rows, telemetry.LogRow{
					LogID:       st.ExecutionID + "/" + st.StateName + "/fail",
					ExecutionID: st.ExecutionID,
					StateName:   st.StateName,
					Level:       "ERROR",
					Message:     fmt.Sprintf("[%s] failed: %s", st.StateName, st.Error),
					OccurredAt:  st.UpdatedAt,
				})
			}
		}
		if rows == nil {
			rows = []telemetry.LogRow{}
		}
		writeJSON(w, http.StatusOK, map[string]any{"logs": rows, "total": len(rows)})
		return
	}

	q := r.URL.Query()
	filter := telemetry.LogFilter{
		ExecutionID: q.Get("execution_id"),
		ServiceName: q.Get("service_name"),
		StateName:   q.Get("state_name"),
		Level:       q.Get("level"),
		Query:       q.Get("q"),
		Limit:       parseIntParam(q.Get("limit"), 100),
		Offset:      parseIntParam(q.Get("offset"), 0),
	}
	if v := q.Get("since"); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid since timestamp", "bad_request")
			return
		}
		filter.Since = &t
	}
	if v := q.Get("until"); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid until timestamp", "bad_request")
			return
		}
		filter.Until = &t
	}

	logs, total, err := s.Telemetry.QueryLogs(r.Context(), filter)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to query logs", "internal")
		return
	}
	if logs == nil {
		logs = []telemetry.LogRow{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"logs": logs, "total": total})
}

// handleWSLogs upgrades to WebSocket, streams historical log entries matching
// the query params, sends a "logs_end" event, then forwards any future
// "log_entry" events broadcast to the hub that match the same filter.
func (s *Server) handleWSLogs(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("ws_logs: upgrade: %v", err)
		return
	}
	defer conn.Close()

	q := r.URL.Query()
	execID := q.Get("execution_id")
	serviceName := q.Get("service_name")
	stateName := q.Get("state_name")
	level := q.Get("level")
	limit := parseIntParam(q.Get("limit"), 100)
	offset := parseIntParam(q.Get("offset"), 0)

	send := func(ev WSEvent) error {
		_ = conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
		return conn.WriteJSON(ev)
	}

	// Stream historical logs.
	historical, err := s.logsForFilter(r, execID, serviceName, stateName, level, limit, offset)
	if err != nil {
		log.Printf("ws_logs: query: %v", err)
		return
	}
	for _, row := range historical {
		ev := WSEvent{
			Type: "log_entry",
			Payload: LogEntryPayload{
				LogID:       row.LogID,
				ExecutionID: row.ExecutionID,
				ServiceName: row.ServiceName,
				StateName:   row.StateName,
				Level:       row.Level,
				Message:     row.Message,
				OccurredAt:  row.OccurredAt,
			},
			Timestamp: time.Now(),
		}
		if err := send(ev); err != nil {
			return
		}
	}
	if err := send(WSEvent{Type: "logs_end", Timestamp: time.Now()}); err != nil {
		return
	}

	// Subscribe to future log_entry events from the hub.
	ch := s.Hub.subscribe()
	defer s.Hub.unsubscribe(ch)

	// Drain incoming messages in a separate goroutine so we detect client disconnect.
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
		case ev, ok := <-ch:
			if !ok {
				return
			}
			if ev.Type != "log_entry" {
				continue
			}
			p, ok := ev.Payload.(LogEntryPayload)
			if !ok {
				continue
			}
			if execID != "" && p.ExecutionID != execID {
				continue
			}
			if serviceName != "" && p.ServiceName != serviceName {
				continue
			}
			if stateName != "" && p.StateName != stateName {
				continue
			}
			if level != "" && p.Level != level {
				continue
			}
			if err := send(ev); err != nil {
				return
			}
		}
	}
}

func (s *Server) logsForFilter(r *http.Request, execID, serviceName, stateName, level string, limit, offset int) ([]telemetry.LogRow, error) {
	if s.Telemetry == nil {
		if execID == "" {
			return nil, nil
		}
		states, err := s.Store.ListStates(r.Context(), execID)
		if err != nil {
			return nil, err
		}
		var rows []telemetry.LogRow
		for _, st := range states {
			rows = append(rows, telemetry.LogRow{
				LogID:       st.ExecutionID + "/" + st.StateName + "/start",
				ExecutionID: st.ExecutionID,
				StateName:   st.StateName,
				Level:       "INFO",
				Message:     fmt.Sprintf("[%s] started", st.StateName),
				OccurredAt:  st.CreatedAt,
			})
			switch st.Status {
			case store.StatusCompleted:
				rows = append(rows, telemetry.LogRow{
					LogID:       st.ExecutionID + "/" + st.StateName + "/complete",
					ExecutionID: st.ExecutionID,
					StateName:   st.StateName,
					Level:       "INFO",
					Message:     fmt.Sprintf("[%s] completed", st.StateName),
					OccurredAt:  st.UpdatedAt,
				})
			case store.StatusFailed:
				rows = append(rows, telemetry.LogRow{
					LogID:       st.ExecutionID + "/" + st.StateName + "/fail",
					ExecutionID: st.ExecutionID,
					StateName:   st.StateName,
					Level:       "ERROR",
					Message:     fmt.Sprintf("[%s] failed: %s", st.StateName, st.Error),
					OccurredAt:  st.UpdatedAt,
				})
			}
		}
		return rows, nil
	}

	filter := telemetry.LogFilter{
		ExecutionID: execID,
		ServiceName: serviceName,
		StateName:   stateName,
		Level:       level,
		Limit:       limit,
		Offset:      offset,
	}
	logs, _, err := s.Telemetry.QueryLogs(r.Context(), filter)
	return logs, err
}
