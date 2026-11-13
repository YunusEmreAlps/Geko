# Design review — 2026-11-11

Three independent code reviews covered visual hierarchy, accessibility and
trace investigation workflows. This is a source review, not a user study or
a claim of WCAG certification.

## Prioritized plan and changes

1. Fix mobile brand rules overridden by later CSS and allow navigation wrapping.
2. Improve light-theme navigation label, waterfall bar and node indicator contrast.
3. Add zoom percentage and actual-size controls. Fit must accommodate the entire
   graph, including graphs requiring less than 25% scale. Small text at Fit scale
   is supplemented by a readable HTML service list with full service names.
4. Expose selected services with aria-current, not only stroke color.
5. Preserve waterfall disclosure state per trace in session storage, with a
   server-rendered open fallback on paginated views. Explain that service nodes
   open one representative span, not all spans belonging to that service.
6. Compact trace summary spacing and size the graph relative to viewport height.

## Verification

Run `go test ./web`, `node --check web/assets/dashboard.js` and inspect the
rendered trace at desktop and narrow widths. Check both themes, Fit/100%,
service selection and waterfall reopening after navigation. Source-level
contrast findings must not be presented as full browser accessibility audits.

## Visual system — 2026-11-13

A second review (visual system, layout and copy) found that the
UI looked generated rather than designed. Problems: green-tinted neutrals mixed with blue-grey
component colors, the lime accent used for links, navigation, labels and buttons alike,
stacked override blocks, a large mascot with a tagline, and an explanatory
subtitle under every heading. Changes:

1. `dashboard.css` is one ordered file. All colors are tokens in the dark and light
   blocks. Neutrals have no hue.
2. The accent is limited to the primary action, the focus ring and the selected span or node.
   Active navigation uses a neutral fill.
3. A compact sidebar holds a monochrome mark, a trace ID search (`/`), Overview, Traces and
   Regressions, with Applications and Settings at the bottom. A sticky top bar replaces the
   page banners and back links.
4. Failures uses a segmented view control and a single filter row. Secondary filters sit
   behind "More filters". Live status shows as a dot.
5. Service map nodes keep their counts inside the card. Counts are pluralized correctly, and
   the duplicate zoom readout is gone. A graph that is slightly wider than the canvas (it fits at
   70% or more) is scaled to fit on load. Larger graphs still scroll.
6. The README screenshots in `docs/images` show the new design.

## Follow-up work

- Add visual regression coverage for both themes.
- Preserve graph scroll and zoom per trace, including late-arriving graph changes.- Consider a service-specific span picker after validating the readable list.
