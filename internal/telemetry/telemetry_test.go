package telemetry

import (
	"context"
	"os"
	"testing"
	"time"
)

// liveClient returns a *Client connected to a test ClickHouse instance,
// or nil when KFLOW_TEST_CLICKHOUSE_DSN is not set (skips live tests).
func liveClient(t *testing.T) *Client {
	t.Helper()
	dsn := os.Getenv("KFLOW_TEST_CLICKHOUSE_DSN")
	if dsn == "" {
		return nil
	}
	c, err := NewClient(context.Background(), dsn)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

// --- Nil-safety tests (no live ClickHouse required) ---

func TestEventWriter_NilSafe(t *testing.T) {
	var w *EventWriter
	// Must not panic.
	w.RecordStateTransition(context.Background(), "e1", "StateA", "Pending", "Running", "")

	w2 := &EventWriter{ch: nil}
	w2.RecordStateTransition(context.Background(), "e1", "StateA", "Pending", "Running", "")
}

func TestMetricsWriter_NilSafe(t *testing.T) {
	var w *MetricsWriter
	w.RecordServiceInvocation(context.Background(), "svc", "e1:StateA", 42, 200, "")

	w2 := &MetricsWriter{ch: nil}
	w2.RecordServiceInvocation(context.Background(), "svc", "e1:StateA", 42, 200, "")
}

func TestLogWriter_NilSafe(t *testing.T) {
	var w *LogWriter
	w.Write(context.Background(), "e1", "", "StateA", "INFO", "hello")

	w2 := &LogWriter{ch: nil}
	w2.Write(context.Background(), "e1", "", "StateA", "INFO", "hello")
}

func TestStreamJobLogs_NilWriter(t *testing.T) {
	// Must not panic when lw is nil.
	StreamJobLogs(context.Background(), nil, "ns", "job", "e1", "StateA", nil)
}

func TestClampLimit(t *testing.T) {
	tests := []struct{ v, def, max, want int }{
		{0, 100, 1000, 100},
		{-1, 100, 1000, 100},
		{50, 100, 1000, 50},
		{1500, 100, 1000, 1000},
	}
	for _, tc := range tests {
		if got := clampLimit(tc.v, tc.def, tc.max); got != tc.want {
			t.Errorf("clampLimit(%d,%d,%d) = %d, want %d", tc.v, tc.def, tc.max, got, tc.want)
		}
	}
}

// --- Integration tests (require KFLOW_TEST_CLICKHOUSE_DSN) ---

func TestInitSchema_Idempotent(t *testing.T) {
	c := liveClient(t)
	if c == nil {
		t.Skip("KFLOW_TEST_CLICKHOUSE_DSN not set")
	}
	ctx := context.Background()
	if err := c.InitSchema(ctx); err != nil {
		t.Fatalf("first InitSchema: %v", err)
	}
	if err := c.InitSchema(ctx); err != nil {
		t.Fatalf("second InitSchema (idempotency): %v", err)
	}
}

func TestEventWriter_WriteAndQuery(t *testing.T) {
	c := liveClient(t)
	if c == nil {
		t.Skip("KFLOW_TEST_CLICKHOUSE_DSN not set")
	}
	ctx := context.Background()
	_ = c.InitSchema(ctx)

	execID := "test-exec-" + time.Now().Format("20060102150405")
	w := NewEventWriter(c)
	w.RecordStateTransition(ctx, execID, "StateA", "Pending", "Running", "")
	time.Sleep(200 * time.Millisecond) // let goroutine flush

	events, err := c.QueryExecutionEvents(ctx, execID, nil, 10)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	if events[0].StateName != "StateA" || events[0].ToStatus != "Running" {
		t.Fatalf("unexpected event: %+v", events[0])
	}
}

func TestMetricsWriter_WriteAndQuery(t *testing.T) {
	c := liveClient(t)
	if c == nil {
		t.Skip("KFLOW_TEST_CLICKHOUSE_DSN not set")
	}
	ctx := context.Background()
	_ = c.InitSchema(ctx)

	svcName := "test-svc-" + time.Now().Format("150405")
	w := NewMetricsWriter(c)
	w.RecordServiceInvocation(ctx, svcName, "exec1:StateA", 123, 200, "")
	time.Sleep(200 * time.Millisecond)

	metrics, err := c.QueryServiceMetrics(ctx, svcName, nil, nil, 10)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(metrics) != 1 {
		t.Fatalf("expected 1 metric, got %d", len(metrics))
	}
	if metrics[0].DurationMs != 123 || metrics[0].StatusCode != 200 {
		t.Fatalf("unexpected metric: %+v", metrics[0])
	}
}

func TestLogWriter_WriteAndQuery(t *testing.T) {
	c := liveClient(t)
	if c == nil {
		t.Skip("KFLOW_TEST_CLICKHOUSE_DSN not set")
	}
	ctx := context.Background()
	_ = c.InitSchema(ctx)

	execID := "log-exec-" + time.Now().Format("150405")
	w := NewLogWriter(c)
	w.Write(ctx, execID, "", "StateA", "INFO", "Payment processed successfully")
	time.Sleep(200 * time.Millisecond)

	logs, total, err := c.QueryLogs(ctx, LogFilter{ExecutionID: execID})
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if total != 1 || len(logs) != 1 {
		t.Fatalf("expected 1 log, got total=%d len=%d", total, len(logs))
	}
	if logs[0].Message != "Payment processed successfully" {
		t.Fatalf("unexpected message: %q", logs[0].Message)
	}
}

func TestLogWriter_QueryByMessage(t *testing.T) {
	c := liveClient(t)
	if c == nil {
		t.Skip("KFLOW_TEST_CLICKHOUSE_DSN not set")
	}
	ctx := context.Background()
	_ = c.InitSchema(ctx)

	execID := "log-q-" + time.Now().Format("150405")
	w := NewLogWriter(c)
	w.Write(ctx, execID, "", "ChargePayment", "INFO", "Payment processed")
	w.Write(ctx, execID, "", "ValidateOrder", "INFO", "Order validated")
	time.Sleep(200 * time.Millisecond)

	logs, _, err := c.QueryLogs(ctx, LogFilter{ExecutionID: execID, Query: "Payment"})
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(logs) != 1 {
		t.Fatalf("expected 1 matching log, got %d", len(logs))
	}
}
