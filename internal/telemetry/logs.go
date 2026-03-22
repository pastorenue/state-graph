package telemetry

import (
	"bufio"
	"context"
	"log"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// LogRow is a row from the logs table.
type LogRow struct {
	LogID       string    `json:"log_id"`
	ExecutionID string    `json:"execution_id"`
	ServiceName string    `json:"service_name"`
	StateName   string    `json:"state_name"`
	Level       string    `json:"level"`
	Message     string    `json:"message"`
	OccurredAt  time.Time `json:"occurred_at"`
}

// LogFilter controls which rows are returned by QueryLogs.
type LogFilter struct {
	ExecutionID string
	ServiceName string
	StateName   string
	Level       string
	Since       *time.Time
	Until       *time.Time
	Query       string // full-text search against message
	Limit       int    // 0 = default 100, max 1000
	Offset      int
}

// LogWriter writes captured log lines to the logs table.
type LogWriter struct {
	ch *Client
}

// NewLogWriter creates a LogWriter backed by ch.
func NewLogWriter(ch *Client) *LogWriter {
	return &LogWriter{ch: ch}
}

// Write records a single log line as a fire-and-forget goroutine.
// level should be one of: INFO, WARN, ERROR, DEBUG.
func (w *LogWriter) Write(
	ctx context.Context,
	execID, serviceName, stateName, level, message string,
) {
	if w == nil || w.ch == nil {
		return
	}
	go func() {
		writeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := w.ch.conn.Exec(writeCtx,
			`INSERT INTO logs (execution_id, service_name, state_name, level, message)
			 VALUES (?, ?, ?, ?, ?)`,
			execID, serviceName, stateName, level, message,
		); err != nil {
			log.Printf("telemetry WARN: write log execID=%s state=%s: %v", execID, stateName, err)
		}
	}()
}

// StreamJobLogs reads container stdout/stderr for a completed K8s Job and writes
// each line to ClickHouse via lw. Best-effort: failures are logged and ignored.
// lw may be nil, in which case the function is a no-op.
func StreamJobLogs(
	ctx context.Context,
	clientset *kubernetes.Clientset,
	namespace, jobName, execID, stateName string,
	lw *LogWriter,
) {
	pods, err := clientset.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{
		LabelSelector: "job-name=" + jobName,
	})
	if err != nil {
		log.Printf("telemetry WARN: StreamJobLogs list pods for job %q: %v", jobName, err)
		return
	}

	for _, pod := range pods.Items {
		req := clientset.CoreV1().Pods(namespace).GetLogs(pod.Name, &corev1.PodLogOptions{})
		stream, err := req.Stream(ctx)
		if err != nil {
			log.Printf("telemetry WARN: StreamJobLogs get logs for pod %q: %v", pod.Name, err)
			continue
		}

		scanner := bufio.NewScanner(stream)
		for scanner.Scan() {
			line := scanner.Text()
			log.Printf("job-log: [%s/%s] %s", execID, stateName, line)
			if lw != nil {
				lw.Write(ctx, execID, "", stateName, "INFO", line)
			}
		}
		if err := scanner.Err(); err != nil {
			log.Printf("telemetry WARN: StreamJobLogs scan pod %q: %v", pod.Name, err)
		}
		stream.Close()
	}
}

// QueryLogs returns log rows matching filter. Returns rows and the total count.
// limit ≤ 0 defaults to 100, capped at 1000.
func (c *Client) QueryLogs(ctx context.Context, filter LogFilter) ([]LogRow, int, error) {
	limit := clampLimit(filter.Limit, 100, 1000)

	where := " WHERE 1=1"
	args := []any{}

	if filter.ExecutionID != "" {
		where += " AND execution_id = ?"
		args = append(args, filter.ExecutionID)
	}
	if filter.ServiceName != "" {
		where += " AND service_name = ?"
		args = append(args, filter.ServiceName)
	}
	if filter.StateName != "" {
		where += " AND state_name = ?"
		args = append(args, filter.StateName)
	}
	if filter.Level != "" {
		where += " AND level = ?"
		args = append(args, filter.Level)
	}
	if filter.Since != nil {
		where += " AND occurred_at >= ?"
		args = append(args, *filter.Since)
	}
	if filter.Until != nil {
		where += " AND occurred_at <= ?"
		args = append(args, *filter.Until)
	}
	if filter.Query != "" {
		where += " AND message LIKE ?"
		args = append(args, "%"+filter.Query+"%")
	}

	// Count total matching rows.
	var total uint64
	countArgs := make([]any, len(args))
	copy(countArgs, args)
	countRow := c.conn.QueryRow(ctx, "SELECT count() FROM logs"+where, countArgs...)
	if err := countRow.Scan(&total); err != nil {
		return nil, 0, err
	}

	query := `SELECT toString(log_id), execution_id, service_name, state_name, level, message, occurred_at
	          FROM logs` + where + ` ORDER BY occurred_at DESC LIMIT ? OFFSET ?`
	args = append(args, limit, filter.Offset)

	rows, err := c.conn.Query(ctx, query, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	var result []LogRow
	for rows.Next() {
		var r LogRow
		if err := rows.Scan(&r.LogID, &r.ExecutionID, &r.ServiceName, &r.StateName, &r.Level, &r.Message, &r.OccurredAt); err != nil {
			return nil, 0, err
		}
		result = append(result, r)
	}
	return result, int(total), rows.Err()
}
