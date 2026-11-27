# A real agent investigation of a synthetic defect

This is a deliberately broken Go fixture investigated by a fresh-context coding
agent using actual Geko MCP evidence, then repaired and rerun. It is not a
production incident, a human usability study, or an agent success-rate benchmark.
The checked-in [example](../examples/agent-investigation/quote.go) is correct;
the harness introduces the defect only in a separate scratch copy.

## Reproduce the failing run

Use Go from `go.mod`, Python 3, and a Geko binary built from this checkout.
The script uses only the Python standard library and the repository's existing
official OpenTelemetry Go SDK dependencies. It does not install dependencies or
invoke a hosted model. Let Go download missing module dependencies beforehand
if your integration environment cannot access the network.

```sh
go build -o bin/geko-walkthrough ./cmd/geko
python scripts/agent-walkthrough.py --binary bin/geko-walkthrough --work-dir bin/quote-investigation --phase baseline
```

On Windows use `bin/geko-walkthrough.exe`. The work directory must be new.
The harness copies `go.mod`, `go.sum`, and the fixture into it, removes only the
quantity whitespace normalization, and runs:

```sh
geko run --report <work-dir>/baseline -- go test -count=1 -v ./source
```

The original test workload checks both `"2"` and `" 2 "` at unit price 500;
both must produce 1000. The baseline deliberately fails the second assertion.
The Go SDK emits a real `*strconv.NumError` exception with a stack trace, sets
error status, inherits the run ID supplied by `geko run`, and synchronously
shuts down its batch exporter before the test process exits. The harness
requires two tagged spans/traces and one failing trace, and records the
unchanged child exit code 1 in `baseline/command.json`.

A separate temporary Geko process uses a random loopback port and a database
inside the work directory. It is terminated in a `finally` block before MCP
opens the database read-only. The harness clears inherited `GEKO_*` and
`OTEL_*` settings so a personal collector, token, or database is not selected.
It needs permission to open local sockets, including loopback client requests.

## Investigate before changing the code

The script initializes the real `geko mcp` stdio endpoint using JSON-RPC and
calls `investigate_group` with the group ID from `baseline/run.json`.
It saves the actual request/response transcript, decoded investigation, test
output, portable run snapshot and Geko failure report in `baseline/`.

Give a coding agent only these observed files and the scratch sources:

- `baseline/investigation.json` and `baseline/mcp-transcript.json`;
- `baseline/run.json`, `baseline/command.stdout.txt`, `baseline/command.stderr.txt`;
- `source/quote.go` and `source/quote_test.go`.

Ask it to explain the failure using source and trace/span citations, propose
the smallest fix, and specify verification. Do this before revealing the
repair below. The harness does not generate an agent response or pretend that
rerunning the script performs a new coding-agent investigation.

The relevant evidence is an exception parsing the whitespace-padded input.
The stack points to `Quote` and its test call. The contract and test establish
that surrounding whitespace is allowed. The minimal repair is to import
`strings` and parse `strings.TrimSpace(quantity)` before multiplying by the
unit price. Apply that change to `<work-dir>/source/quote.go`.

## Verify the same workload

```sh
python scripts/agent-walkthrough.py --binary bin/geko-walkthrough --work-dir bin/quote-investigation --phase verify
```

Verification does not edit the scratch source or test. It executes the same
test command with `geko run --baseline baseline/run.json --report verify`,
requires child exit 0, two tagged traces and zero failing traces, and calls the
actual MCP `compare_runs` tool with the two explicit run IDs. It requires an
comparison with available snapshots, `partial` coverage, and the original
group classified as `not_observed`.
Both complete snapshots and the comparison remain on disk.

The passing assertion is positive evidence for this input contract. An absent
exception in a captured candidate alone does not prove a general fix or full
export coverage. Counts describe retained evidence. This tiny fixture makes no
latency, throughput, production reliability, or agent-performance claims.

## Recorded investigation on 2026-11-27

See the [compact observation](benchmarks/2026-11-27-agent-walkthrough.json) for
the actual run IDs, source locations, exception evidence, agent finding and
before/after verification. A fresh-context investigator received the baseline
evidence and broken source before the repair was applied. No conversation was
simulated. The baseline's `successful_baseline` field was null; this non-HTTP
fixture did not provide a Geko successful-request comparison. That limitation
was preserved rather than inferred away from its passing control subtest.
