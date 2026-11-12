import os
from pathlib import Path
import json
import time
from urllib.request import Request, urlopen
from uuid import uuid4

endpoint = os.getenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT") or "http://127.0.0.1:4318/v1/traces"
trace_id = uuid4().hex
end = time.time_ns()
fixture = Path(__file__).with_name("trace.json").read_text(encoding="utf-8")
body = (fixture.replace("@SERVICE@", "python-inventory").replace("@TRACE_ID@", trace_id)
        .replace("@START@", str(end - 1_000_000)).replace("@END@", str(end)))
request = Request(endpoint, data=body.encode(), headers={"Content-Type": "application/json"}, method="POST")
with urlopen(request, timeout=5) as response:
    result = json.load(response)
if result.get("partialSuccess"):
    raise RuntimeError(f"Partial export: {result}")
print(trace_id)
