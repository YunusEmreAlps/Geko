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

## Follow-up work

- Consolidate the accumulated theme overrides into semantic surface and graph
  tokens in a separate change, with visual regression coverage.
- Preserve graph scroll and zoom per trace, including late-arriving graph changes.
- Consider a service-specific span picker after validating the readable list.
