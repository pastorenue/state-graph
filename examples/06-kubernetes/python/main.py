import os
import sys

import kflow
from kflow import Workflow, step, run
from handlers import (
    ingest_data, transform_data, validate_data,
    export_data, notify_complete, handle_error,
)


def main():
    wf = Workflow("data-pipeline")

    @wf.task("IngestData")
    def _ingest(input):
        return ingest_data(input)

    @wf.task("TransformData")
    def _transform(input):
        return transform_data(input)

    @wf.task("ValidateData")
    def _validate(input):
        return validate_data(input)

    @wf.task("ExportData")
    def _export(input):
        return export_data(input)

    @wf.task("NotifyComplete")
    def _notify(input):
        return notify_complete(input)

    @wf.task("HandleError")
    def _handle(input):
        return handle_error(input)

    wf.flow(
        step("IngestData").next("TransformData"),
        step("TransformData").next("ValidateData").retry(max_attempts=3, backoff_seconds=2),
        step("ValidateData").next("ExportData").catch("HandleError"),
        step("ExportData").next("NotifyComplete"),
        step("NotifyComplete").end(),
        step("HandleError").end(),
    )

    run(wf, input={"source": "s3://input/data.csv"})


if __name__ == "__main__":
    main()
