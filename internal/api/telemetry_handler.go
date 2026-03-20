package api

import (
	"net/http"
	"time"

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
		writeJSON(w, http.StatusOK, map[string]any{"logs": []any{}, "total": 0})
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
