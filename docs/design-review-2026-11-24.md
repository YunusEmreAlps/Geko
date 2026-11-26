# Design review — 2026-11-24

Three design agents independently reviewed visual identity, investigation
workflows and accessibility. The primary agent checked their findings against
the current source and rendered demo. This is an expert review, not the
five-developer study in phase 15, a usability score or WCAG certification.
No product UI code was changed during the initial review. The user subsequently
authorized the implementation recorded below.

## Finding

The neutral palette, small monochrome geko mark and table-based investigation
screens already suit the product. Overview and failure lists do not need a new
visual identity. The unfinished, generic feeling is concentrated in newer
tools: shared layouts used for unrelated forms, explanations competing with
data, and actions and statuses given the same visual weight.

The design direction is a quiet debugging workspace: make the selected
evidence and the next useful action clear. Keep existing system fonts,
native forms, disclosures and restrained color. Avoid introducing decorative
metrics cards, gradients, illustrations or another component framework.

## Evidence and limits

- Built the current tree as `bin/geko-design-review.exe`.
- The existing `scripts/browser-checks.mjs` passed with Chrome at 360/1280px,
  both themes. It checks selected interactions, theme application and outer
  page overflow. It is not a comprehensive keyboard or visual regression test.
- Captured the populated temporary demo at 390/1440px in both themes:
  Overview, Failures, trace detail, Connect, Settings, Applications and run
  comparison. Local captures are in `dist/design-review-2026-11-24/`.
- Budget and populated run-comparison captures are in `dist/browser-checks/`;
  the demo capture of run comparison shows the initial form.
- Screenshots are local generated artifacts, not committed reference images.
  Current captures do not cover Doctor, notification failures, authenticated
  role differences, 200% zoom or all empty/error states.
- Contrast figures below are computed from CSS tokens and their declared
  backgrounds. Validate the actual rendered SVG/text states during changes.

## Prioritized work

| Order | Problem and source | Proposed change | Acceptance |
| --- | --- | --- | --- |
| 1 | Mobile hides `.side-search` below 760px (`web/assets/dashboard.css`); placeholder token contrast is approximately 3.23:1 light / 3.52:1 dark. Graph edges use decorative border colors, approximately 1.49:1 / 1.62:1 on canvas. | Keep an accessible trace-ID route on narrow screens. Separate readable text and data-visualization tokens from decorative borders. Distinguish keyboard node focus from selection and hover. | Trace-ID search reachable by keyboard and touch; text target 4.5:1, meaningful data graphics 3:1 on their actual backgrounds; no loss of the HTML service list. |
| 2 | `.notice` uses the same accent treatment for success, invalid input, checkpoint failure and read-only information (`dashboard.css`, `budget.html`, `connect.html`, `settings.html`). | Add small semantic info/success/warning/error variants with explicit wording. Retain `role=status`/`alert` behavior. | Failed operations never look successful; states remain understandable without color. Verify rendered examples of each state in both themes. |
| 3 | Budget's volumes are buried between explanation paragraphs. A two-control time selector inherits the general multi-column `.filters` grid (`web/templates/budget.html`). Run baseline/candidate fields also inherit unequal columns (`runs.html`). | Give Budget a compact selected UTC window and active period controls, then span/byte totals and the service table. Use a content-sized hour form and equal run-ID fields. Move routine calculation details into one disclosure. | Selected period is visible and programmatic; checkpoint failure, partial/missing coverage and discarded history stay prominent. Do not label a window complete unless the report proves it. Current/previous/custom-hour navigation still works without JS. |
| 4 | On the populated 1440px trace, six evidence rows occupy about 406px; the map starts around y=603 and waterfall around y=1080 (`trace.html`). Toolbar actions have equal weight. | Prototype a compact selected-evidence summary with an explicit count and accessible route to every other evidence span. Separate investigation actions from pin/export utilities. Keep exception text more prominent than service metadata. | No evidence silently hidden or dropped; deep links, paging, selected-span context and no-JS behavior survive. Compare/copy remain discoverable. Validate the prototype with the phase-15 tasks before committing to interaction changes. |
| 5 | Connect verification follows the language setup material (`connect.html`). | Make Configure → Verify a short, explicit sequence; progressively disclose language guides. Keep observation status near the selected setup path. | Wrong endpoint/token, missing run ID, successful export and idle service remain distinct. Observation scope and global rejection caveats remain available. |
| 6 | Settings combines theme, receiver diagnostics, tool links and configuration reference into equally weighted long rows (`settings.html`). | Add lightweight section navigation and a clear tool-link area. Separate editable browser theme from effective server configuration. Show value/source consistently. | Sections remain accessible by anchors and keyboard; source/redaction/storage disclosures remain intact. Do not implement phase-16 runtime settings as part of this design work. |

For small graphs, evaluate a compact height based on observed node/edge count.
The populated multi-service map and the existing desktop sticky inspector
already work; preserve those rather than replacing them with a new graph or
split-pane framework.

## Delivery and validation

1. Implement the narrowly scoped accessibility and semantic-status corrections.
2. Fix Budget/run form proportions and Budget's data hierarchy.
3. Prototype trace/Connect/Settings hierarchy changes against the existing
   routes. Review screenshots and keyboard behavior before broader rollout.
4. Use the phase-15 participant tasks to choose interaction changes and repeat
   them afterwards. Agent opinions do not substitute for participant results.

For each implemented batch, run the relevant Go web tests and existing browser
checks. Add targeted assertions for the changed behavior rather than tests that
only repeat class names. Check 360/390, 768, 1280/1440px and 200% zoom in both
themes. Exercise skip link, trace search, navigation, disclosures, graph focus
and table scrolling. Include loaded, empty, partial, failure and read-only
states. Screenshot capture alone is not a pass/fail visual comparison.

Keep the product's distinctions intact: request outcome versus observed error,
retained data versus full coverage, best-effort redaction versus guaranteed
privacy, and generated setup versus observed telemetry. Simplifying layout is
not a reason to remove those facts.

## Implemented follow-up

Implemented after the review on 2026-11-24:

- Keep mobile trace-ID search and all navigation links visible using a wrapping
  layout, with the existing `/` shortcut. Improve text/graph contrast tokens
  and give keyboard-focused graph nodes a distinct dashed stroke.
- Separate informational, successful, warning and failed-operation notices.
- Prioritize Budget's selected UTC window and totals, preserve visible coverage
  warnings, and group routine accounting details under a native disclosure.
  Use compact hour controls and equally sized run-comparison inputs.
- Keep primary evidence, or explicitly selected evidence, above the workspace.
  Other evidence on the current page remains available in a counted disclosure;
  explicit pagination expands it. Pin/export/external-viewer actions move into
  a native More actions disclosure. Add direct waterfall/details links.
- Move Connect verification immediately after configuration snippets; language
  guidance and observation-scope details remain available on demand.
- Add Settings section links and distinguish browser preferences from effective
  server settings. Configuration sources are consistently shown below values.

The graph already has a 176px minimum drawing extent for small cases; no new
graph-sizing mechanism or rendering library was added. The populated
multi-service graph and desktop sticky inspector are preserved.

Validation completed:

- `go test ./... -count=1 -timeout=5m` and `go vet ./...` passed.
- Additional targeted web tests passed after the final primary-evidence
  selection correction: explicit selection beyond pagination/map limits,
  implicit HTTP-error roots versus child exceptions, evidence-page disclosure
  state and Budget current/previous/custom-hour selection.
- Updated browser checks passed at 360/768/1280/1440px in both themes, covering
  keyboard skip link/search, native disclosures, Settings section targets,
  equal-width run fields and page overflow. Evidence and download actions also
  passed with JavaScript disabled.
- Populated before/after demo captures at 390/1440px are in
  `dist/design-review-2026-11-24/` and `dist/design-after-2026-11-24/`.
  Visual inspection confirms that the selected evidence occupies one summary
  instead of six expanded rows, while the graph retains its full behavior.

These checks do not constitute a full accessibility audit. Actual browser
200% zoom and the participant task measurements remain separate validation;
phase 15 is not marked complete. Runtime server settings remain phase 16.

## Mobile design reconsideration

The user rejected the responsive experience after the overflow fixes. The
three design agents revisited the current 360/390/440px screenshots and
discussed alternatives with each other. The earlier lack of overflow or
overlap was a technical result, not evidence of a good mobile workflow.

Observed problems remain: the wrapped global navigation takes about 128px;
navigation, local controls and filters compete before the first result; the
mobile trace presents a desktop graph and wide waterfall before the detailed
exception/stack. Settings is a long document of equally weighted properties.

### Agreed direction — partially implemented

The mobile Menu/Search shell and overflow corrections were implemented on
2026-11-26, as recorded below. The broader trace ordering, Settings section
selection and Budget mobile presentation remain separate planned work.

| Option | Decision | Reason |
| --- | --- | --- |
| Keep all global links wrapped | Replace | Discoverable but spends multiple rows on navigation before investigation content. |
| Compact header with labelled Menu and Search | Preferred | Keeps global navigation available on demand and returns space to the active task. |
| Fixed bottom navigation plus More | Defer | Consumes persistent viewport space and introduces another navigation layer without evidence of frequent phone use. |

Use one compact global header, approximately 56px tall, with touch-friendly
Menu and Search controls. Page titles and operation context remain in the
content rather than being squeezed into the global header. Keep a single
navigation DOM and actual links; do not invent application-menu keyboard
semantics for ordinary site navigation. Aim for 44px primary touch targets;
the existing 28–32px desktop controls should not dictate mobile ergonomics.

The preferred initial interaction is a nonmodal, native disclosure panel
that expands into the document flow, with a bounded scroll area for short
screens. It does not imply a modal focus trap or an inert backdrop. Escape
should close it and return focus; `/` should reveal trace search and focus
the input. Preserve a usable native navigation/search fallback without JS.
Any later overlay design needs a complete dialog/focus model, not just a
backdrop. Prototype desktop/mobile transitions and no-JS behavior before
choosing the final markup.

For investigation content:

- **Trace:** selected evidence and its exception/stack first; technical
  properties on demand; waterfall and map secondary. Keep direct span links,
  all evidence, pagination, duration and relationship data. Do not reverse
  visual order with CSS while leaving keyboard/screen-reader order behind.
- **Settings:** select a section rather than present two navigation rows and
  the entire property document at once. Hash links such as `#receiver` must
  reveal their target. Without JS, all sections remain accessible as a normal
  document; no CSS-only hiding of configuration sections. Restore all sections
  when returning to desktop. Do not steal focus on initial load; move it when
  the user explicitly selects a section, and preserve history navigation.
- **Budget:** make service name, span usage/limit, byte usage/limit and overages
  readable within the viewport. Labelled stacked service rows are a candidate;
  cardinality, Other services and partial coverage must not disappear.
- **Failures:** keep search prominent, compact the remaining filters behind
  an explicit active-filter summary, and retain request outcome versus error
  evidence. Avoid converting every table into generic cards.

Implementation order: mobile shell and trace content order; Settings/Budget
adaptation; then filter density. Keep desktop investigation tools intact.
Validate at 320/390/440px, landscape and tablet boundaries, both themes and
200% browser zoom. Check touch targets, focus return, deep links, no-JS access,
long identifiers and large evidence lists. Evaluate time to visible evidence
and ability to read meaningful values, not only `scrollWidth <= innerWidth`.
Human task measurements remain outstanding; these decisions are expert
hypotheses informed by the user's feedback, not measured usability gains.

## Corrective implementation — 2026-11-26

The user explicitly requested applying the existing responsive direction and
reported that the Go checkout example did not work. Preserve the current
uncommitted Menu/search and first-run changes; no commits or workspace moves.

Execution plan:

- [x] Reproduce the Go checkout startup → request → OTLP export → stored trace
  path using temporary data. Fix only demonstrated gaps, with regression tests
  before product changes; update `examples/go` instructions to match.
- [x] Reproduce actual controls overflowing at 320/390/440px and the 760/761px
  sidebar boundary. Include valid long service/viewer names and open popovers.
- [x] Add failing browser assertions for the demonstrated cases in
  `scripts/browser-checks.mjs`, including menu keyboard/no-JS behavior.
- [x] Fix shared button sizing/wrapping and popup positioning in
  `web/assets/dashboard.css`, reusing existing markup and preserving data.
  Do not hide overflow to make tests pass.
- [x] Finish the approved mobile Menu/Search shell in existing layout/JS:
  one navigation/form DOM, labelled touch targets, keyboard opening/closing,
  and a usable fallback if JavaScript is absent or dashboard initialization fails.
- [x] Run the example, web and full Go tests; run the rebuilt browser checks
  in both themes, inspect populated screenshots and review the final diff.
- [x] Address independently reproduced review cases: Search submit focus on
  desktop resize, Escape outside an open disclosure, and reachable sticky
  header/sidebar actions in short landscape viewports.

Ruling: continue in this shared working tree because this repairs the existing
uncommitted implementation, including user edits; an isolated checkout would
omit the behavior being repaired. The existing mobile design is authorized;
no additional redesign or dependency upgrade is part of this fix.

Results:

- The actual Go SDK/export path worked after a scenario request: HTTP
  200/500/200, three stored traces after about five seconds, two with error
  evidence. Starting the server alone did not create traces; `/` returned 404.
  An exact, uninstrumented landing route now links to the three scenarios and
  startup output explains the first request. Unknown paths remain 404. Windows
  PowerShell 5.1's `curl -i` alias problem was reproduced; instructions use
  `curl.exe` explicitly.
- Red tests reproduced the landing 404 and an off-screen trace download.
  Additional tests reproduced lost submit-button focus on desktop resize,
  Escape stealing focus from a page filter, and unreachable controls in short
  viewports. The repaired code passed those regressions.
- Browser checks passed at 320/360/390/440/760/761/768/1280/1440px in both
  themes, with extra 761×260 and 768×200 checks, open panels, full identifiers,
  pagination, native forms and unavailable-script fallbacks. This is viewport
  testing, not browser UI zoom or participant research.
- The Connect action now says **View service traces**. The full service name
  remains in the form/status and exact destination query, instead of making
  a five-line button.
- `go test ./... -count=1 -timeout=5m`, `go vet ./...` and `go mod tidy -diff`
  passed. After the final copy change, `go test ./web ./examples/go -count=1`
  and pinned staticcheck for those packages passed. An independent final
  Chromium review found no remaining important issues in the scoped fixes.

Generated before/after images are under `dist/responsive-probe-before/`,
`dist/responsive-probe-after/` and `dist/browser-checks/`. The local runs do not
certify a fresh hosted CI run or other browser engines. No commits were made.

## Follow-up: local export and task order — 2026-11-26

The user authorized continuing the usability improvements and supplied a live
Go example failure. Review follows Apple's [layout](https://developer.apple.com/design/human-interface-guidelines/layout)
and [accessibility](https://developer.apple.com/design/human-interface-guidelines/accessibility)
guidance and Fluent's [layout](https://fluent2.microsoft.design/layout) and
[accessibility](https://fluent2.microsoft.design/accessibility) principles, adapted
to this web application. Agents are software reviewers, not former employees
of those companies. Delegated implementation was unavailable due to account
limits; the primary agent implemented and checked the changes.

Execution order:

1. Reproduce the missing trace using the running receiver, then test actual
   SDK export destinations before changing the example's default transport.
2. Put selected exceptions and stack traces before secondary properties,
   waterfall and map in document order; retain the desktop sticky inspector.
3. Enhance existing Settings anchors with a labelled mobile section selector.
   Preserve deep links, history, runtime saves, desktop access and no-script
   access. Keep one copy of each section and all existing authorization rules.
4. Adapt waterfall and Budget tables to available content width. Keep every
   duration, service, usage, limit, overage and cardinality value accessible.
   Correct the empty state for hours containing only unattributed volume.
5. Run Go regressions, the populated browser matrix and source checks. Inspect
   screenshots in both themes; record measured results, not certification.

The running receiver was healthy but had accepted no exports. The user's SDK
reported `HTTP response to HTTPS client`: its generic HTTPS default conflicted
with Geko's local HTTP listener. Earlier integration checks supplied an HTTP
endpoint explicitly and missed this no-environment path. A real SDK transport
test reproduced the failure; the sample now defaults to
`http://127.0.0.1:4318/v1/traces` only when no endpoint or TLS configuration is
supplied. Explicit HTTP/HTTPS endpoints and insecure=false settings retain SDK
precedence. A synthetic request exported through the corrected default was
retrieved from the user's running receiver. Existing example processes must
be restarted; previously rejected traces are not reconstructed.

Trace and Budget regressions failed before the fixes. The mobile Settings
browser test also failed before its enhancement. The complete document remains
usable without dashboard JavaScript. Budget cardinality scope is next to its
usage table; historical unavailable measurements remain explicitly unavailable.

Verification: `go test ./... -count=1 -timeout=5m`, `go vet ./...`,
`go mod tidy -diff` and pinned staticcheck for `./web ./examples/go` passed.
After the last panel markup adjustment, the web/example tests and the rebuilt
browser matrix passed again: 320/360/390/440/760/761/768/1280/1440px, both
themes, real nonzero budget limits/overages/cardinality, section deep links,
history, runtime saves and no-script fallback. Screenshots in
`dist/browser-checks/` were visually inspected at mobile and desktop sizes.
Browser UI zoom, other engines and human usability research remain unmeasured.
No commits were made.

## Approved span inspector simplification — 2026-11-26

The user approved the follow-up review of the expanded properties inspector.
Continue in this shared working tree, preserving the ongoing settings work.
Use existing Go templates, native details and CSS; no dependency or data-model
change is needed. The target is one bordered inspector with exception evidence
first, compact properties and sibling attribute disclosures. Empty attribute
groups show a zero count without an interactive disclosure. The operation and
service stay in the inspector heading rather than being repeated in properties.

Implementation plan (inline execution):

- [x] Add failing rendered/browser checks for direct attribute access, zero
  counts, compact property alignment, one frame and page-only scrolling.
- [x] Update `web/templates/trace.html`: one inspector surface, exception events,
  properties and counted attribute groups at the same level. Preserve source
  links, span links, escaping, selected span behavior and no-script access.
- [x] Scope styles in `web/assets/dashboard.css` to the inspector. Use its
  available width to select label/value columns; stack only on very narrow
  panels. Remove nested borders and the independent inspector scroll container.
- [x] Run web tests, the responsive browser matrix in both themes, vet and
  staticcheck; inspect populated screenshots, including long stacks and IDs.
  Record results here without adding another review document. Do not commit.

Results: the zero-count Go regression and one-step resource-attribute browser
regression both failed before implementation and passed afterwards. Full
`go test ./... -count=1 -timeout=5m`, web vet and pinned staticcheck passed.
The complete rebuilt-browser checks passed at nine widths from 320 to 1440px
in both themes, including native disclosures without JavaScript, 60-line stacks,
long status values and page-scroll access below the stack. Geometry assertions
allow one CSS pixel for rounded client dimensions; same-document navigation
preserves native disclosure state, so tests open it only when needed.

Expanded inspector screenshots for 320/390/1440px in both themes are under
`dist/browser-checks/span-inspector-*.png`; mobile and desktop captures were
visually inspected. No backend behavior, authorization or dependencies changed
in this follow-up. No commits were made.

## Approved daily investigation workflow — 2026-11-26

Goal: implement the user's approved field-review findings in the current tree,
preserving earlier changes and making no commits. Keep Go templates, native
forms/disclosures, existing SQLite read pools, redacted stored data and roles.
No schema migration or external infrastructure is needed.

Plan, executed inline with failing regressions before implementation:

- [x] Make Connect's repeated Check again perform a server request, including
  without JavaScript, while preserving the original observation window.
- [x] Carry validated local list return URLs through trace/evidence/map/parent,
  pagination, comparison and wide-reader links. Reject external/nested returns.
  Preserve browser-back behavior and avoid putting unsafe values into links.
- [x] Offer a full-width span reader with native navigation and preserve the
  selected span. Give missing traces receiver/Connect/storage checks and explain
  possible causes without claiming a cause that the database cannot establish.
- [x] Compare bounded per-operation span totals from both traces in the same
  SQLite snapshot as their summaries. Separate app/namespace/environment/kind,
  disclose limits and overlapping inclusive durations, and link to source spans.
- [x] Offer native datalist suggestions for observed versions, services and run
  IDs using bounded recent retained records; keep paste/manual input. Disclose
  omitted older records and sampling limits; do not add a full-table DISTINCT.
- [x] Verify focused regressions, full Go tests, vet/staticcheck and rebuilt
  desktop/mobile browser flows (including repeated checks and no-script use).
  Update existing user documentation and record results here.


Results: the browser regression reproduced repeated Check again not issuing a
request. A GET form alone still allowed fragment-only navigation in Chromium;
fresh read-only check values now force a request without changing the original
`since` or deadline. Repeated new-window actions are fresh too. Both paths were
verified with JavaScript enabled and Check again without JavaScript.

The regression suite also checks filtered-list return links, full-width reading,
selected spans, suggested versions/run IDs, missing-trace guidance and all
comparison columns on phones. SQLite tests cover suggestion time/service
filters, tied observation times, trace/value limits, query plans using existing
indexes without global span scans or temporary sorting, and truncated trace
comparisons. Unknown/overflowed timings stay unavailable; repeated calls and
separate environments/kinds are tested.

`go test ./... -count=1 -timeout=5m`, `go vet ./...`, pinned staticcheck on the
changed packages and `go mod tidy -diff` passed. Independent code review found
a wide-reader state loss in parent/waterfall/map/pagination links. A browser
regression reproduced it, the shared navigation mapper was fixed, and the
reviewer found no remaining important issue in the fix. Full web tests and the
complete rebuilt-browser matrix passed again afterwards, at nine widths from
320 to 1440px in both themes, including no-script controls. Captures are under
`dist/browser-checks/`; wide-reader and mobile comparison captures were inspected.
No release publication, commits or production performance claims were made.
