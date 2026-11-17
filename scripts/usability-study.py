"""Start fresh local Geko and Jaeger instances for one usability study task."""

import argparse
from contextlib import contextmanager
import json
import secrets
import subprocess
import time
import urllib.error
import urllib.request


SCENARIOS = {
    "A": ("storefront", "/checkout", "inventory", "reserve stock", "InventoryTimeout"),
    "B": ("booking", "/reservations", "availability", "hold room", "AvailabilityTimeout"),
}


def attributes(values):
    return [
        {"key": key, "value": (
            {"intValue": str(value)} if isinstance(value, int) else {"stringValue": value}
        )}
        for key, value in values.items()
    ]


def dataset(scenario):
    gateway, route, downstream, operation, exception = SCENARIOS[scenario]
    now = time.time_ns()
    traces = [
        (route, 200, False),
        (route, 200, True),
        ("/health", 200, False),
        ("/preview", 503, False),
        (route, 200, False),
    ]
    resources = {gateway: [], downstream: []}
    target = None
    for index, (path, status, timed_out) in enumerate(traces):
        trace_id = secrets.token_hex(16)
        root_id, child_id = secrets.token_hex(8), secrets.token_hex(8)
        start = now - (len(traces) - index) * 1_000_000_000
        method = "GET" if path == "/health" else "POST"
        resources[gateway].append({
            "traceId": trace_id, "spanId": root_id, "name": f"{method} {path}", "kind": 2,
            "startTimeUnixNano": str(start), "endTimeUnixNano": str(start + 180_000_000),
            "attributes": attributes({"http.response.status_code": status, "http.route": path}),
            "status": {"code": 2 if status >= 500 else 1},
        })
        if path != route:
            continue
        child = {
            "traceId": trace_id, "spanId": child_id, "parentSpanId": root_id,
            "name": operation, "kind": 1, "startTimeUnixNano": str(start + 50_000_000),
            "endTimeUnixNano": str(start + (150_000_000 if timed_out else 85_000_000)),
            "status": {"code": 2 if timed_out else 1},
        }
        if timed_out:
            child["events"] = [{
                "timeUnixNano": str(start + 140_000_000), "name": "exception",
                "attributes": attributes({"exception.type": exception, "exception.message": f"{downstream} timed out"}),
            }]
            target = {
                "trace": trace_id, "root": root_id, "child": child_id,
                "operation": operation, "exception": exception,
            }
        resources[downstream].append(child)
    payload = {"resourceSpans": [
        {"resource": {"attributes": attributes({"service.name": service, "service.namespace": "geko-usability"})},
         "scopeSpans": [{"spans": spans}]}
        for service, spans in resources.items()
    ]}
    return json.dumps(payload).encode(), target


def docker(*args):
    return subprocess.check_output(["docker", *args], text=True).strip()


@contextmanager
def container(image, ports, options=()):
    bindings = [part for port in ports for part in ("-p", f"127.0.0.1::{port}")]
    name = docker("run", "-d", "--rm", *bindings, *options, image)
    try:
        addresses = {
            port: "http://" + docker("port", name, f"{port}/tcp").splitlines()[0]
            for port in ports
        }
        yield addresses
    except Exception:
        subprocess.run(["docker", "logs", name], check=False)
        raise
    finally:
        docker("rm", "-f", name)


def request(url, token="", payload=None, wait=False):
    headers = {"Authorization": f"Bearer {token}"} if token else {}
    if payload is not None:
        headers["Content-Type"] = "application/json"
    req = urllib.request.Request(url, data=payload, headers=headers)
    for attempt in range(40 if wait else 1):
        try:
            with urllib.request.urlopen(req, timeout=5) as response:
                return response.read()
        except (ConnectionError, TimeoutError, urllib.error.URLError) as error:
            if not wait or attempt == 39:
                raise
            if isinstance(error, urllib.error.HTTPError) and error.code not in (404, 503):
                raise
            time.sleep(0.25)


def verify(geko, jaeger, target, token):
    geko_trace = json.loads(request(f"{geko}/api/v1/traces/{target['trace']}", token))["trace"]
    if (geko_trace["entry_span"]["http_status"] != 200
            or geko_trace["evidence"]["span_id"] != target["child"]
            or geko_trace["evidence"]["exception_type"] != target["exception"]):
        raise RuntimeError("Geko did not retain the expected request result and child evidence")
    traces = json.loads(request(f"{jaeger}/api/traces/{target['trace']}", wait=True))["data"]
    spans = {span["spanID"]: span for span in traces[0]["spans"]}
    root_status = next(tag["value"] for tag in spans[target["root"]]["tags"]
                       if tag["key"] in ("http.response.status_code", "http.status_code"))
    child = spans[target["child"]]
    has_exception = any(
        field["key"] == "exception.type" and field["value"] == target["exception"]
        for log in child.get("logs", []) for field in log["fields"]
    )
    if (int(root_status) != 200 or child["operationName"] != target["operation"]
            or not any(ref["spanID"] == target["root"] for ref in child["references"])
            or not has_exception):
        raise RuntimeError("Jaeger did not retain the expected request result and child evidence")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("scenario", choices=SCENARIOS)
    parser.add_argument("--geko-image", default="geko:local-comparison")
    parser.add_argument("--jaeger-image", default="cr.jaegertracing.io/jaegertracing/jaeger:2.23.0")
    parser.add_argument("--verify-only", action="store_true", help="verify both tools then stop")
    args = parser.parse_args()
    token = secrets.token_hex(24)
    payload, target = dataset(args.scenario)
    options = (
        "-e", "GEKO_LISTEN_ADDR=0.0.0.0:4318", "-e", "GEKO_ALLOW_PLAINTEXT=true",
        "-e", f"GEKO_ACCESS_TOKEN={token}",
    )
    with container(args.geko_image, [4318], options) as geko, container(args.jaeger_image, [4318, 16686]) as jaeger:
        request(geko[4318] + "/healthz", wait=True)
        request(jaeger[16686], wait=True)
        for url, credential in ((geko[4318], token), (jaeger[4318], "")):
            result = json.loads(request(url + "/v1/traces", credential, payload) or b"{}")
            if result.get("partialSuccess", {}).get("rejectedSpans", 0) not in (0, "0"):
                raise RuntimeError(f"incomplete study dataset at {url}")
        verify(geko[4318], jaeger[16686], target, token)
        print(f"Scenario {args.scenario} verified in Geko and Jaeger.", flush=True)
        if not args.verify_only:
            print(f"Geko: {geko[4318]} (sign in with this temporary access token: {token})")
            print(f"Jaeger: {jaeger[16686]}/search")
            print(f"Facilitator answer: trace {target['trace']}, child span {target['child']}, HTTP 200")
            input("Press Enter after the attempt to stop both containers. ")


if __name__ == "__main__":
    main()
