// Package telemetry is the Anti-Corruption Layer (ACL) that translates domain
// state transitions into ClickHouse's append-only schema. It must NOT import
// internal/engine, internal/api, or internal/runner.
package telemetry

import (
	"context"
	"fmt"

	ch "github.com/ClickHouse/clickhouse-go/v2"
	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"
)

// Client wraps the ClickHouse native driver connection.
type Client struct {
	conn driver.Conn
}

// NewClient connects to ClickHouse using the provided DSN.
// DSN format: "clickhouse://<host>:<port>?database=<db>&username=<u>&password=<p>"
func NewClient(ctx context.Context, dsn string) (*Client, error) {
	opts, err := ch.ParseDSN(dsn)
	if err != nil {
		return nil, fmt.Errorf("telemetry: parse ClickHouse DSN: %w", err)
	}

	conn, err := ch.Open(opts)
	if err != nil {
		return nil, fmt.Errorf("telemetry: open ClickHouse connection: %w", err)
	}

	if err := conn.Ping(ctx); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("telemetry: ping ClickHouse: %w", err)
	}

	return &Client{conn: conn}, nil
}

// InitSchema creates all telemetry tables if they do not already exist.
// Safe to call on every startup — all DDL uses CREATE TABLE IF NOT EXISTS.
func (c *Client) InitSchema(ctx context.Context) error {
	ddls := []string{
		`CREATE TABLE IF NOT EXISTS execution_events (
			event_id     UUID    DEFAULT generateUUIDv4(),
			execution_id String,
			state_name   String,
			from_status  String,
			to_status    String,
			error        String,
			occurred_at  DateTime64(3) DEFAULT now64()
		) ENGINE = MergeTree()
		ORDER BY (execution_id, occurred_at)
		TTL toDateTime(occurred_at) + INTERVAL 90 DAY
		SETTINGS index_granularity = 8192`,

		`CREATE TABLE IF NOT EXISTS service_metrics (
			metric_id     UUID    DEFAULT generateUUIDv4(),
			service_name  String,
			invocation_id String,
			duration_ms   UInt64,
			status_code   UInt16,
			error         String,
			occurred_at   DateTime64(3) DEFAULT now64()
		) ENGINE = MergeTree()
		ORDER BY (service_name, occurred_at)
		TTL toDateTime(occurred_at) + INTERVAL 90 DAY
		SETTINGS index_granularity = 8192`,

		`CREATE TABLE IF NOT EXISTS logs (
			log_id       UUID    DEFAULT generateUUIDv4(),
			execution_id String,
			service_name String,
			state_name   String,
			level        String,
			message      String,
			occurred_at  DateTime64(3) DEFAULT now64()
		) ENGINE = MergeTree()
		ORDER BY (execution_id, occurred_at)
		TTL toDateTime(occurred_at) + INTERVAL 30 DAY
		SETTINGS index_granularity = 8192`,
	}

	for _, ddl := range ddls {
		if err := c.conn.Exec(ctx, ddl); err != nil {
			return fmt.Errorf("telemetry: init schema: %w", err)
		}
	}
	return nil
}

// Close closes the ClickHouse connection.
func (c *Client) Close() error {
	return c.conn.Close()
}
