package main

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"time"

	"github.com/gorilla/websocket"
	"github.com/spf13/cobra"
)

var logsCmd = &cobra.Command{
	Use:   "logs",
	Short: "Query or follow execution logs",
	RunE:  runLogs,
}

func init() {
	logsCmd.Flags().String("execution", "", "filter by execution ID")
	logsCmd.Flags().String("service", "", "filter by service name")
	logsCmd.Flags().String("workflow", "", "resolve most recent execution for this workflow")
	logsCmd.Flags().String("level", "", "filter by level (INFO|WARN|ERROR|DEBUG)")
	logsCmd.Flags().BoolP("follow", "f", false, "follow live log stream via WebSocket")
	logsCmd.Flags().Int("limit", 50, "number of log entries to return (non-follow mode)")
	logsCmd.Flags().String("since", "", "only show logs since this time (ISO 8601)")
}

func runLogs(cmd *cobra.Command, _ []string) error {
	execID, _ := cmd.Flags().GetString("execution")
	serviceName, _ := cmd.Flags().GetString("service")
	workflow, _ := cmd.Flags().GetString("workflow")
	level, _ := cmd.Flags().GetString("level")
	follow, _ := cmd.Flags().GetBool("follow")
	limit, _ := cmd.Flags().GetInt("limit")
	since, _ := cmd.Flags().GetString("since")

	if execID == "" && workflow != "" {
		id, err := latestExecutionID(workflow)
		if err != nil {
			return err
		}
		execID = id
	}

	if follow {
		return followLogs(execID, serviceName, level, since)
	}
	return queryLogsOnce(execID, serviceName, level, since, limit)
}

func queryLogsOnce(execID, serviceName, level, since string, limit int) error {
	params := url.Values{}
	if execID != "" {
		params.Set("execution_id", execID)
	}
	if serviceName != "" {
		params.Set("service_name", serviceName)
	}
	if level != "" {
		params.Set("level", level)
	}
	if since != "" {
		params.Set("since", since)
	}
	params.Set("limit", fmt.Sprintf("%d", limit))

	result, err := doJSON("GET", "/api/v1/logs?"+params.Encode(), nil)
	if err != nil {
		return err
	}

	logs, _ := result["logs"].([]any)
	for _, l := range logs {
		row, _ := l.(map[string]any)
		printLogLine(row)
	}
	return nil
}

func followLogs(execID, serviceName, level, since string) error {
	wsURL, err := buildWSURL("/api/v1/ws/logs")
	if err != nil {
		return err
	}

	params := url.Values{}
	if execID != "" {
		params.Set("execution_id", execID)
	}
	if serviceName != "" {
		params.Set("service_name", serviceName)
	}
	if level != "" {
		params.Set("level", level)
	}
	if since != "" {
		params.Set("since", since)
	}
	if tok := resolveBearer(); tok != "" {
		params.Set("token", tok)
	}
	if p := params.Encode(); p != "" {
		wsURL += "?" + p
	}

	for {
		conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
		if err != nil {
			fmt.Fprintf(os.Stderr, "ws connect: %v — retrying in 2s\n", err)
			time.Sleep(2 * time.Second)
			continue
		}
		if err := streamLogs(conn); err != nil {
			fmt.Fprintf(os.Stderr, "ws disconnected: %v — retrying in 2s\n", err)
			conn.Close()
			time.Sleep(2 * time.Second)
			continue
		}
		conn.Close()
		return nil
	}
}

func streamLogs(conn *websocket.Conn) error {
	for {
		_, data, err := conn.ReadMessage()
		if err != nil {
			return err
		}
		var evt struct {
			Type    string          `json:"type"`
			Payload json.RawMessage `json:"payload"`
		}
		if err := json.Unmarshal(data, &evt); err != nil {
			continue
		}
		switch evt.Type {
		case "log_entry":
			var row map[string]any
			if err := json.Unmarshal(evt.Payload, &row); err == nil {
				printLogLine(row)
			}
		case "logs_end":
			fmt.Fprintln(os.Stdout, "---")
		}
	}
}

func printLogLine(row map[string]any) {
	ts, _ := row["occurred_at"].(string)
	if t, err := time.Parse(time.RFC3339Nano, ts); err == nil {
		ts = t.UTC().Format(time.RFC3339)
	}
	level, _ := row["level"].(string)
	execID, _ := row["execution_id"].(string)
	if len(execID) > 8 {
		execID = execID[:8]
	}
	stateName, _ := row["state_name"].(string)
	source := execID
	if stateName != "" && execID != "" {
		source = execID + "/" + stateName
	} else if stateName != "" {
		source = stateName
	}
	msg, _ := row["message"].(string)
	fmt.Printf("%-20s  %-5s  %-28s  %s\n", ts, level, source, msg)
}

func buildWSURL(path string) (string, error) {
	u, err := url.Parse(serverFlag)
	if err != nil {
		return "", fmt.Errorf("parse server URL: %w", err)
	}
	switch u.Scheme {
	case "https":
		u.Scheme = "wss"
	default:
		u.Scheme = "ws"
	}
	u.Path = path
	u.RawQuery = ""
	return u.String(), nil
}

func latestExecutionID(workflow string) (string, error) {
	result, err := doJSON("GET", "/api/v1/executions?workflow="+url.QueryEscape(workflow)+"&limit=1", nil)
	if err != nil {
		return "", err
	}
	execs, _ := result["executions"].([]any)
	if len(execs) == 0 {
		return "", fmt.Errorf("no executions found for workflow %q", workflow)
	}
	first, _ := execs[0].(map[string]any)
	id, _ := first["id"].(string)
	if id == "" {
		return "", fmt.Errorf("execution record has no id field")
	}
	return id, nil
}
