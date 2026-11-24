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

### Agreed direction — not yet implemented

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
