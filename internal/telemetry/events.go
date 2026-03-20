package telemetry

import (
	"context"
	"log"
	"time"
)

// EventRow is a row from the execution_events table.
type EventRow struct {
	EventID     string    `json:"event_id"`
	ExecutionID string    `json:"execution_id"`
	StateName   string    `json:"state_name"`
	FromStatus  string    `json:"from_status"`
	ToStatus    string    `json:"to_status"`
	Error       string    `json:"error"`
	OccurredAt  time.Time `json:"occurred_at"`
}

// EventWriter records state transition events to the execution_events table.
type EventWriter struct {
	ch *Client
}

// NewEventWriter creates an EventWriter backed by ch.
func NewEventWriter(ch *Client) *EventWriter {
	return &EventWriter{ch: ch}
}

// RecordStateTransition records a state lifecycle event as a fire-and-forget
// goroutine. Errors are logged at WARN level and never propagated.
func (w *EventWriter) RecordStateTransition(
	ctx context.Context,
	execID, stateName, fromStatus, toStatus, errMsg string,
) {
	if w == nil || w.ch == nil {
		return
	}
	go func() {
		writeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := w.ch.conn.Exec(writeCtx,
			`INSERT INTO execution_events (execution_id, state_name, from_status, to_status, error)
			 VALUES (?, ?, ?, ?, ?)`,
			execID, stateName, fromStatus, toStatus, errMsg,
		); err != nil {
			log.Printf("telemetry WARN: record state transition execID=%s state=%s: %v", execID, stateName, err)
		}
	}()
}

// QueryExecutionEvents returns events for execID ordered by occurred_at ascending.
// since is optional (nil = all time). limit ≤ 0 defaults to 100, capped at 1000.
func (c *Client) QueryExecutionEvents(ctx context.Context, execID string, since *time.Time, limit int) ([]EventRow, error) {
	limit = clampLimit(limit, 100, 1000)

	query := `SELECT toString(event_id), execution_id, state_name, from_status, to_status, error, occurred_at
	          FROM execution_events WHERE execution_id = ?`
	args := []any{execID}

	if since != nil {
		query += ` AND occurred_at >= ?`
		args = append(args, *since)
	}
	query += ` ORDER BY occurred_at ASC LIMIT ?`
	args = append(args, limit)

	rows, err := c.conn.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []EventRow
	for rows.Next() {
		var r EventRow
		if err := rows.Scan(&r.EventID, &r.ExecutionID, &r.StateName, &r.FromStatus, &r.ToStatus, &r.Error, &r.OccurredAt); err != nil {
			return nil, err
		}
		result = append(result, r)
	}
	return result, rows.Err()
}
