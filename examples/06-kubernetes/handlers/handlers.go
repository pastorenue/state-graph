package handlers

import (
	"context"
	"fmt"

	"github.com/pastorenue/kflow/pkg/kflow"
)

func IngestData(_ context.Context, input kflow.Input) (kflow.Output, error) {
	source := input["source"]
	fmt.Printf("[IngestData] reading from %v\n", source)
	return kflow.Output{
		"source":     source,
		"rows":       1000,
		"ingested":   true,
	}, nil
}

func TransformData(_ context.Context, input kflow.Input) (kflow.Output, error) {
	rows := input["rows"]
	fmt.Printf("[TransformData] transforming %v rows\n", rows)
	return kflow.Output{
		"source":      input["source"],
		"rows":        rows,
		"transformed": true,
	}, nil
}

func ValidateData(_ context.Context, input kflow.Input) (kflow.Output, error) {
	fmt.Printf("[ValidateData] validating %v rows\n", input["rows"])
	return kflow.Output{
		"source":    input["source"],
		"rows":      input["rows"],
		"validated": true,
	}, nil
}

func ExportData(_ context.Context, input kflow.Input) (kflow.Output, error) {
	fmt.Printf("[ExportData] exporting %v rows\n", input["rows"])
	return kflow.Output{
		"exported": true,
		"rows":     input["rows"],
	}, nil
}

func NotifyComplete(_ context.Context, input kflow.Input) (kflow.Output, error) {
	fmt.Printf("[NotifyComplete] pipeline complete — %v rows processed\n", input["rows"])
	return kflow.Output{"status": "complete"}, nil
}

func HandleError(_ context.Context, input kflow.Input) (kflow.Output, error) {
	fmt.Printf("[HandleError] caught error: %v\n", input["_error"])
	return kflow.Output{"status": "error_handled"}, nil
}
