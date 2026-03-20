package telemetry

import (
	"context"
	"log"
	"time"
)

// MetricRow is a row from the service_metrics table.
type MetricRow struct {
	MetricID     string    `json:"metric_id"`
	ServiceName  string    `json:"service_name"`
	InvocationID string    `json:"invocation_id"`
	DurationMs   uint64    `json:"duration_ms"`
	StatusCode   uint16    `json:"status_code"`
	Error        string    `json:"error"`
	OccurredAt   time.Time `json:"occurred_at"`
}

// MetricsWriter records service invocation metrics to the service_metrics table.
type MetricsWriter struct {
	ch *Client
}

// NewMetricsWriter creates a MetricsWriter backed by ch.
func NewMetricsWriter(ch *Client) *MetricsWriter {
	return &MetricsWriter{ch: ch}
}

// RecordServiceInvocation records a service invocation metric as a fire-and-forget
// goroutine. Errors are logged at WARN level and never propagated.
//
// statusCode: HTTP status for Deployment mode; 200 (success) or 500 (failure) for Lambda.
func (w *MetricsWriter) RecordServiceInvocation(
	ctx context.Context,
	serviceName, invocationID string,
	durationMs uint64,
	statusCode uint16,
	errMsg string,
) {
	if w == nil || w.ch == nil {
		return
	}
	go func() {
		writeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := w.ch.conn.Exec(writeCtx,
			`INSERT INTO service_metrics (service_name, invocation_id, duration_ms, status_code, error)
			 VALUES (?, ?, ?, ?, ?)`,
			serviceName, invocationID, durationMs, statusCode, errMsg,
		); err != nil {
			log.Printf("telemetry WARN: record service invocation service=%s: %v", serviceName, err)
		}
	}()
}

// QueryServiceMetrics returns invocation metrics for serviceName.
// since and until are optional. limit ≤ 0 defaults to 100, capped at 1000.
func (c *Client) QueryServiceMetrics(ctx context.Context, serviceName string, since, until *time.Time, limit int) ([]MetricRow, error) {
	limit = clampLimit(limit, 100, 1000)

	query := `SELECT toString(metric_id), service_name, invocation_id, duration_ms, status_code, error, occurred_at
	          FROM service_metrics WHERE service_name = ?`
	args := []any{serviceName}

	if since != nil {
		query += ` AND occurred_at >= ?`
		args = append(args, *since)
	}
	if until != nil {
		query += ` AND occurred_at <= ?`
		args = append(args, *until)
	}
	query += ` ORDER BY occurred_at DESC LIMIT ?`
	args = append(args, limit)

	rows, err := c.conn.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []MetricRow
	for rows.Next() {
		var r MetricRow
		if err := rows.Scan(&r.MetricID, &r.ServiceName, &r.InvocationID, &r.DurationMs, &r.StatusCode, &r.Error, &r.OccurredAt); err != nil {
			return nil, err
		}
		result = append(result, r)
	}
	return result, rows.Err()
}
