"""Pure handler functions for the data-pipeline workflow (Python)."""


def ingest_data(input: dict) -> dict:
    source = input.get("source")
    print(f"[IngestData] reading from {source}")
    return {"source": source, "rows": 1000, "ingested": True}


def transform_data(input: dict) -> dict:
    rows = input.get("rows")
    print(f"[TransformData] transforming {rows} rows")
    return {"source": input.get("source"), "rows": rows, "transformed": True}


def validate_data(input: dict) -> dict:
    print(f"[ValidateData] validating {input.get('rows')} rows")
    return {"source": input.get("source"), "rows": input.get("rows"), "validated": True}


def export_data(input: dict) -> dict:
    print(f"[ExportData] exporting {input.get('rows')} rows")
    return {"exported": True, "rows": input.get("rows")}


def notify_complete(input: dict) -> dict:
    print(f"[NotifyComplete] pipeline complete — {input.get('rows')} rows processed")
    return {"status": "complete"}


def handle_error(input: dict) -> dict:
    print(f"[HandleError] caught error: {input.get('_error')}")
    return {"status": "error_handled"}
