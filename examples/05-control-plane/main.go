// Example 05: control-plane execution
// Submits a 3-state order-processing workflow to the orchestrator via HTTP and polls until done.
//
// Default mode: control-plane client (POST /api/v1/workflows → run → poll)
// --state=<name> mode: K8s Job worker (GetInput → handler → CompleteState/FailState)
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"time"

	kflowv1 "github.com/pastorenue/kflow/internal/gen/kflow/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/protobuf/types/known/structpb"
)

const workflowName = "cp-order"

func main() {
	stateName := flag.String("state", "", "state name to execute (K8s Job worker mode)")
	flag.Parse()

	if *stateName != "" {
		runWorker(*stateName)
		return
	}
	runClient()
}

// runClient submits the workflow and polls for completion.
func runClient() {
	endpoint := os.Getenv("KFLOW_API_ENDPOINT")
	if endpoint == "" {
		endpoint = "http://localhost:8080"
	}

	fmt.Printf("=== 05-control-plane: %s ===\n", workflowName)

	// 1. Register the workflow graph (proto JSON: {"graph": {...}}).
	regBody := map[string]any{
		"graph": map[string]any{
			"name": workflowName,
			"states": []map[string]any{
				{"name": "ValidateOrder", "kind": "task"},
				{"name": "CalculateTax", "kind": "task"},
				{"name": "ChargePayment", "kind": "task"},
			},
			"steps": []map[string]any{
				{"name": "ValidateOrder", "next": "CalculateTax"},
				{"name": "CalculateTax", "next": "ChargePayment"},
				{"name": "ChargePayment", "isEnd": true},
			},
		},
	}
	var regResp map[string]string
	if err := apiCall("POST", endpoint+"/api/v1/workflows", regBody, &regResp); err != nil {
		log.Fatalf("register workflow: %v", err)
	}
	fmt.Printf("  registered workflow %q\n", regResp["workflowName"])

	// 2. Trigger a run (name is path param; body carries input as Struct).
	runBody := map[string]any{
		"input": map[string]any{
			"order_id": "ORD-9001",
			"customer": "bob",
			"total":    299.99,
		},
	}
	var runResp map[string]string
	if err := apiCall("POST", endpoint+"/api/v1/workflows/"+workflowName+"/run", runBody, &runResp); err != nil {
		log.Fatalf("run workflow: %v", err)
	}
	execID := runResp["executionId"]
	fmt.Printf("  triggered execution %s\n", execID)

	// 3. Poll until completed or failed (grpc-gateway wraps record in "execution").
	fmt.Println("  polling for completion...")
	start := time.Now()
	deadline := time.Now().Add(30 * time.Second)
	var finalStatus string
	for time.Now().Before(deadline) {
		var resp map[string]any
		if err := apiCall("GET", endpoint+"/api/v1/executions/"+execID, nil, &resp); err != nil {
			log.Fatalf("get execution: %v", err)
		}
		exec, _ := resp["execution"].(map[string]any)
		status, _ := exec["status"].(string)
		if status == "STATUS_COMPLETED" || status == "STATUS_FAILED" {
			finalStatus = status
			elapsed := time.Since(start).Round(time.Millisecond)
			fmt.Printf("  execution %s in %s\n", status, elapsed)
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if finalStatus == "" {
		log.Fatal("timed out waiting for execution to complete")
	}

	// 4. Fetch and print per-state results (states at "states[].stateName / .status").
	var statesResp map[string]any
	if err := apiCall("GET", endpoint+"/api/v1/executions/"+execID+"/states", nil, &statesResp); err != nil {
		log.Fatalf("list states: %v", err)
	}
	fmt.Println("\n  state results:")
	states, _ := statesResp["states"].([]any)
	for _, s := range states {
		rec, _ := s.(map[string]any)
		fmt.Printf("    %-20s %s\n", rec["stateName"], rec["status"])
	}

	fmt.Println("\nNOTE: running without K8s — orchestrator used its in-process pass-through handler.")
	fmt.Println("To run with real K8s Jobs, build this binary as a container image and set KFLOW_IMAGE.")
}

// runWorker is invoked when the binary runs as a K8s Job container.
func runWorker(state string) {
	token := os.Getenv("KFLOW_STATE_TOKEN")
	grpcEndpoint := os.Getenv("KFLOW_GRPC_ENDPOINT")
	if grpcEndpoint == "" {
		grpcEndpoint = "kflow-cp.kflow.svc.cluster.local:9090"
	}

	ctx := context.Background()
	conn, err := grpc.DialContext(ctx, grpcEndpoint, grpc.WithTransportCredentials(insecure.NewCredentials())) //nolint:staticcheck
	if err != nil {
		log.Fatalf("dial runner: %v", err)
	}
	defer conn.Close()

	client := kflowv1.NewRunnerServiceClient(conn)

	resp, err := client.GetInput(ctx, &kflowv1.GetInputRequest{Token: token})
	if err != nil {
		log.Fatalf("GetInput: %v", err)
	}
	input := resp.GetPayload().AsMap()

	output, handlerErr := dispatch(state, input)

	if handlerErr != nil {
		if _, err := client.FailState(ctx, &kflowv1.FailStateRequest{Token: token, ErrorMessage: handlerErr.Error()}); err != nil {
			log.Fatalf("FailState: %v", err)
		}
		return
	}

	outStruct, err := structpb.NewStruct(output)
	if err != nil {
		log.Fatalf("structpb.NewStruct: %v", err)
	}
	if _, err := client.CompleteState(ctx, &kflowv1.CompleteStateRequest{Token: token, Output: outStruct}); err != nil {
		log.Fatalf("CompleteState: %v", err)
	}
}

func dispatch(state string, input map[string]any) (map[string]any, error) {
	switch state {
	case "ValidateOrder":
		return validateOrder(input)
	case "CalculateTax":
		return calculateTax(input)
	case "ChargePayment":
		return chargePayment(input)
	default:
		return nil, fmt.Errorf("unknown state: %s", state)
	}
}

func validateOrder(input map[string]any) (map[string]any, error) {
	total, _ := input["total"].(float64)
	if total <= 0 {
		return nil, fmt.Errorf("invalid order: total must be > 0")
	}
	return map[string]any{
		"order_id":  input["order_id"],
		"total":     total,
		"validated": true,
	}, nil
}

func calculateTax(input map[string]any) (map[string]any, error) {
	total, _ := input["total"].(float64)
	tax := total * 0.08
	return map[string]any{
		"order_id":    input["order_id"],
		"total":       total,
		"tax":         tax,
		"grand_total": total + tax,
	}, nil
}

func chargePayment(input map[string]any) (map[string]any, error) {
	grandTotal, _ := input["grand_total"].(float64)
	return map[string]any{
		"order_id":       input["order_id"],
		"grand_total":    grandTotal,
		"payment_status": "captured",
		"txn_id":         fmt.Sprintf("TXN-%d", time.Now().UnixMilli()),
	}, nil
}

// apiCall makes a JSON HTTP request. reqBody may be nil for GET requests.
func apiCall(method, url string, reqBody any, respPtr any) error {
	var body io.Reader
	if reqBody != nil {
		b, err := json.Marshal(reqBody)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}

	req, err := http.NewRequest(method, url, body)
	if err != nil {
		return err
	}
	if reqBody != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		raw, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(raw))
	}

	if respPtr != nil {
		return json.NewDecoder(resp.Body).Decode(respPtr)
	}
	return nil
}
