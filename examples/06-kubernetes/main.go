package main

import (
	"log"

	"github.com/pastorenue/kflow/examples/06-kubernetes/handlers"
	"github.com/pastorenue/kflow/pkg/kflow"
)

func main() {
	wf := kflow.New("data-pipeline")

	wf.Task("IngestData", handlers.IngestData)
	wf.Task("TransformData", handlers.TransformData).
		Retry(kflow.RetryPolicy{MaxAttempts: 3, BackoffSeconds: 2})
	wf.Task("ValidateData", handlers.ValidateData).Catch("HandleError")
	wf.Task("ExportData", handlers.ExportData)
	wf.Task("NotifyComplete", handlers.NotifyComplete)
	wf.Task("HandleError", handlers.HandleError)

	wf.Flow(
		kflow.Step("IngestData").Next("TransformData"),
		kflow.Step("TransformData").Next("ValidateData"),
		kflow.Step("ValidateData").Next("ExportData").Catch("HandleError"),
		kflow.Step("ExportData").Next("NotifyComplete"),
		kflow.Step("NotifyComplete").End(),
		kflow.Step("HandleError").End(),
	)

	if err := kflow.Dispatch(wf, kflow.Input{"source": "s3://input/data.csv"}); err != nil {
		log.Fatal(err)
	}
}
