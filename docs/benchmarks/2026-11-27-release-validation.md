# Published-release validation — 2026-11-27

[Machine-readable evidence](2026-11-27-release-validation.json) records local
checks of the published **v0.2.0** binaries. Both report revision
`cada0af1b37cf6ca903828227e3cd87e51e1bae0`, Go 1.26.11 and
`vcs.modified=false`. These results describe that release, independently of
features added on the current branch. No hosted workflow was dispatched, no
release was published, and no package manager was installed on this machine.

## Windows release installer

Ran the current checkout's `scripts/install.ps1` with `GEKO_VERSION=0.2.0`, a
temporary `GEKO_INSTALL_DIR`, and `GEKO_NO_MODIFY_PATH=1`. The installer
downloaded the public Windows/amd64 archive and checked it against the release's
`checksums.txt` before copying `geko.exe`. It did not add an installation to PATH.

`scripts/release-smoke.py` ran the absolute binary from a new temporary project:
version, `/readyz`, dashboard HTML, seeded failure evidence, and MCP setup's
absolute database **and config** paths passed. The generated MCP command is
still `geko` in v0.2.0, so an actual client needs that command on its PATH or
must replace it with the installed executable's absolute path.

The Windows/amd64 archive checksum was
`46356889cd9b9d39f3e246083fe7d7a8ea21f7d1934952964807964fbbd0e119`.

## Linux release installer in Docker

Used the existing `golang:1.26.11-alpine` Linux/amd64 image in a disposable
container, installing Python, curl and CA certificates there. The repository
was mounted read-only; installation went to `/tmp/installed` and the working
directory was `/tmp/project`. The installer verified the public release
archive's checksum. The same version, readiness, dashboard, seeded failures,
and MCP config/database-path smoke checks passed. Host PATH was unchanged.

The Linux/amd64 archive checksum was
`f0e23436ea1dcc7ac271e0ae59e6eb36c0aba0fcb8dba9005fef6a25ae7f316f`.

The first container attempt encountered the Windows checkout's CRLF shell
script. A temporary LF copy was used for the successful rerun. A Windows
smoke run inside the restricted sandbox also timed out on loopback; the
authorized run outside that network restriction passed. Neither failed
attempt is counted as a successful check.

## Released SDK run and actual MCP protocol

The Windows release ran the official Python SDK example through `geko run`
against a disposable database. The command exited successfully and its
`report.json` recorded `telemetry_status: available`, `failures: 1`,
`truncated: false`, `limit: 25` and run ID
`6ca35ff7db92e494f832d065924f63e6`.

From another temporary directory, the same released binary opened that
database read-only via an explicit `GEKO_DB_PATH`. Actual stdio MCP
`initialize` negotiated `2024-11-05` with server version `0.2.0`;
`tools/list` returned six tools and `tools/call` for `list_failures` returned
one item. This verifies protocol interaction locally, rather than claiming
an interactive Cursor, Claude Code or VS Code investigation. The reusable
demo smoke script itself checks setup generation, so its
`mcp_protocol_verified` field remains false; the separate protocol evidence
is recorded in the JSON report.

## Linux Homebrew public tap

A disposable Ubuntu 24.04 Linux/amd64 container ran as non-root `linuxbrew`,
with the checkout mounted read-only and limits of two CPUs, 2 GiB memory and
256 processes. It installed the official Linux prerequisites, tapped the public
repository, then ran `brew install yunusemrealps/geko/geko` and
`brew test yunusemrealps/geko/geko`. Both passed for v0.2.0. The actual installed
binary also passed the release smoke check: version/revision, demo readiness,
seeded failure evidence, dashboard and absolute MCP setup paths. The final
container exited zero. Host PATH, profiles and registry were unchanged.

Homebrew reported its own sandbox unavailable inside Docker; this is a bounded,
disposable container check, not a claim about Homebrew sandbox enforcement.
Two earlier environment attempts failed before verification: a root-owned prefix
`bin` directory, then missing developer tools. The final attempt fixed ownership
and installed all required tools. Neither earlier attempt is counted as passing.
This validates Linux Homebrew only; native macOS is still unexecuted.

## Prepared native and hosted gates

[The manual release-validation workflow](../../.github/workflows/release-validation.yml)
contains independently reported jobs for:

- Windows, Linux and macOS release installers, checksum verification, demo,
  dashboard and MCP setup. Installations use `runner.temp`; Windows asserts
  the installer leaves user PATH unchanged.
- A real Scoop installation from the advertised public bucket on Windows.
- Real Homebrew installations and formula tests from the advertised public
  tap on Linux and macOS.
- The current checkout's Action using its **release installer**, with no
  `geko-binary` override and `comment: false`. The official Python SDK example
  must yield available telemetry and a positive failure count.

The only trigger is `workflow_dispatch`; repository permissions are read-only.
The version defaults to `0.2.0`. Public package metadata must match the input
version, otherwise package-manager smoke checks fail. Artifact uploads occur
only when someone explicitly runs the workflow.

Native macOS (including Homebrew), native hosted Linux, actual Scoop installs,
ARM64 execution and the hosted Action remain **unexecuted**. Linux Homebrew
installation/formula/smoke passed locally in Docker as recorded above. Local SDK/run evidence
does not replace hosted Action execution. The workflow is preparation for
those gates, not a claim that they passed. No hosted job was requested here.

## Harness validation

The three smoke-validation unit tests and nine existing benchmark-comparison
tests passed. Disabling the config-path check made its regression test fail;
restoring it made the suite pass. Python compilation, YAML parsing and
`actionlint` 1.7.7 passed for the new workflow.
The JSON report includes all six published archive checksums; only the
Windows/amd64 and Linux/amd64 archives were installed and executed.
