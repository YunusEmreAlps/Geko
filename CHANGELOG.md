# Changelog

## Unreleased (v0.1.0)

First tagged release: everything in the [roadmap](docs/roadmap.md) phases 1 to 6,
and the first items of phase 7.

### Team use

- Triage tokens (`GEKO_TRIAGE_TOKENS`, or `triage:` in `GEKO_TOKENS_FILE`) read
  and resolve, mute or reopen error groups. Callers have the `viewer`,
  `triager` or `admin` role, the sidebar shows who is signed in, and pages hide
  actions the role can't use.
- Sign-in through an authenticating proxy such as oauth2-proxy:
  `GEKO_PROXY_USER_HEADER`, `GEKO_PROXY_TRUSTED_IPS`, `GEKO_PROXY_ROLES` and
  `GEKO_PROXY_DEFAULT_ROLE`.
- Error groups record who resolved or muted them, and `set_group_state` on
  `/mcp` is available to triagers.
- `GEKO_AUDIT_LOG` appends one JSON line per sign-in, sign-out and change.
- Error groups take a note and an issue link, shown in the group list and
  returned by `list_failure_groups` on `/mcp`. Triagers can edit them.
- **Copy as bug evidence** on trace pages and group summaries copies a Markdown
  description with links back to Geko, ready to paste into an issue.
- `trace_link_url` (`GEKO_TRACE_LINK_URL`) adds an **Open in Tempo** button to
  trace pages; `trace_link_name` changes the tool name.
- Error group titles show the classification instead of generic Go error types
  such as `*errors.errorString`.
- A container image for `linux/amd64` and `linux/arm64` at
  `ghcr.io/yunusemrealps/geko`; the Compose setup uses it.

### Privacy

- E-mail addresses, payment card numbers, IBANs and Turkish identity numbers
  are masked as `[email]`, `[card]`, `[iban]` and `[tckn]` before storage.
- Redaction defaults to `strict` once `GEKO_ACCESS_TOKEN` is set. Set
  `GEKO_REDACTION=best_effort` to keep exception messages and stack traces.

### Upgrading

- The database migrates to schema 11, which adds who changed a group's state
  and group notes.
- A token-protected instance without an explicit `redaction` setting switches
  to `strict` for new spans.
- Read token and triage token names must differ.
