# User agent evaluation

The sections below record the original evaluation. Follow-up work added
distinct empty states, receiver diagnostics, separate request and evidence
service filters, paged evidence search, automatic retention, and capacity
counters. The dashboard now opens successful traces by full ID and reads
each trace page from one SQLite snapshot. These changes have automated
coverage and have not been evaluated with human participants.

Three profiles were assigned: a first-time Go developer, an experienced trace
investigator, and a developer responsible for local operation and privacy.
Two agents completed their reviews. The investigator agent identified a search
problem before reaching its usage limit; the primary agent completed the source
and automated-test checks for that profile. These were simulated tasks and
technical checks. No human task times or click counts were collected.

| Task | Current behavior | Acceptance for the next version |
| --- | --- | --- |
| Know whether an exporter works | An empty database and a database with only a successful trace show the same unfiltered empty state. The filtered empty state is already distinct. | Distinguish no stored telemetry, stored telemetry with no evidence, and a filter with no results. Show the effective endpoint and a runnable example. Report the last accepted export and rejection reasons without payloads. |
| Find the service named in the evidence | The Service filter uses the entry service when one exists, even when a different service recorded the displayed exception. | The UI must name its filter scope. A filter intended for evidence services must find a trace with an exception in payments when the entry span belongs to checkout. Preserve or explicitly migrate the documented API semantics. |
| Interpret a successful request with child errors | The Go retry fixture produces HTTP 200 with child exception evidence. The UI has separate columns for result and evidence, but the Failures label can still suggest request failure. | Explicitly describe the result and observed error. Do not infer retry recovery unless instrumentation supports it. A clean HTTP 200 must not appear as failure evidence. |
| Find a second independent error | The summary and free-text search use one ranked evidence span. A lower-ranked exception may be visible in the waterfall but is not covered by the summary search. | List all evidence with bounded paging and direct links. Search must find a second exception by its own message; opening the result should take the user to the matching span. Include late-arrival and tie cases. |
| Understand an incomplete graph | The map reports missing parents and its 500-span limit. It only draws links for observed parent-child pairs in its scope. Direct span lookup already works beyond the first waterfall page. | Test an evidence span after the 500th span, and distinguish graph truncation from a parent absent from storage. Do not imply that the graph is a complete topology. |
| Bound disk usage | Settings reports manual retention. There is no configured expiry or storage bound. | Test seven-day default retention, configured limits, database and WAL usage, and a restart. Document cleanup and rejection behavior under a sustained write load. |
| Know what is safe to share | Attribute filtering and best-effort text scrubbing occur before persistence, but the UI does not explain the policy. Doctor currently checks listener availability as a preflight check. | Describe the policy and residual risk in Settings. Show only aggregate filtering diagnostics. Distinguish a live receiver check from doctor preflight. |

Delivery order: (1) receiver diagnostics and actionable empty states, (2) evidence
search and multi-evidence navigation, (3) bounded retention as a release gate.
The retention work can proceed independently. Grouping comes after users can
reliably find and interpret the underlying evidence.

The web, API, configuration, OTLP receiver, SQLite and Go example tests passed.
They cover a successful HTTP 200 excluded from Failures, a retry fixture with
HTTP 200 plus exception, out-of-order ingestion, and direct focus beyond the
first waterfall page. The source review of the 500-span limit does not count as
a 501-span end-to-end test.

A separate running Geko instance with a temporary database reproduced three
results using real HTTP requests and OTLP exports:

- Before the first span and after storing a clean HTTP 200 trace, Failures
  displayed the same empty-state message.
- A retry trace displayed HTTP 200 and a child exception together.
- A synthetic multi-service trace displayed demo-payments as evidence, yet
  filtering by demo-payments returned no matching traces.
