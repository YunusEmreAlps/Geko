import { randomBytes } from "node:crypto";
import { readFile } from "node:fs/promises";

const endpoint = process.env.OTEL_EXPORTER_OTLP_TRACES_ENDPOINT || "http://127.0.0.1:4318/v1/traces";
const traceId = randomBytes(16).toString("hex");
const end = BigInt(Date.now()) * 1_000_000n;
const fixture = await readFile(new URL("trace.json", import.meta.url), "utf8");
const body = fixture.replace("@SERVICE@", "node-inventory").replace("@TRACE_ID@", traceId)
  .replace("@START@", end - 1_000_000n).replace("@END@", end);
const response = await fetch(endpoint, {
  method: "POST", headers: { "Content-Type": "application/json" }, body,
  signal: AbortSignal.timeout(5000),
});
const result = await response.json();
if (!response.ok || result.partialSuccess) {
  throw new Error(`Export failed (${response.status}): ${JSON.stringify(result)}`);
}
console.log(traceId);
