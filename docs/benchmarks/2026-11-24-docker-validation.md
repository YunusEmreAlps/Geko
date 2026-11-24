# Docker validation — 2026-11-24

These are local technical checks, not human usability observations or a hosted
CI result. Docker Desktop used Linux/amd64 on WSL2, kernel
`5.15.167.4-microsoft-standard-WSL2`, 12 logical CPUs and 16,698,753,024 bytes of
configured memory. This does not verify native macOS, Homebrew or Scoop.

## Study fixtures

Built the current working tree with `docker build -t geko:phase14-validation .`.
The resulting image ID was
`sha256:182447e673b586b5595dfc02e9e1db7dcbbc0ec711825822a628105ace6976e4`.

Both commands passed against Jaeger `2.23.0`:

```sh
python scripts/usability-study.py A --geko-image geko:phase14-validation --verify-only
python scripts/usability-study.py B --geko-image geko:phase14-validation --verify-only
```

Both tools retained the expected HTTP 200 entry span, child exception type and
parent/child relationship. Containers used disposable databases and published
ports only on loopback. The harness removed its containers after each check.
No participant times, navigation counts or success rates were collected.

## Published Linux release

Inside a disposable `golang:1.26.11-alpine` container:

- Downloaded `scripts/install.sh` from the published `v0.2.0` tag. Its SHA-256
  was `21e75ec3b9bffb0fd2df6b646cb10c3641084484205e0cf2811f7fd3861d7716`.
- Ran it with `GEKO_VERSION=v0.2.0` and `GEKO_INSTALL_DIR=/tmp/installed`.
  The installer verified the Linux/amd64 archive against the release's
  `checksums.txt` before installation.
- `geko version` reported `0.2.0`, Linux/amd64, Go 1.26.11, revision
  `cada0af1b37cf6ca903828227e3cd87e51e1bae0` and `vcs.modified=false`.
- From `/tmp/project`, created `geko.yaml` with `db_path: ./traces.db` and ran
  `geko mcp setup claude-code`. The output configuration pinned
  `GEKO_CONFIG=/tmp/project/geko.yaml` and
  `GEKO_DB_PATH=/tmp/project/traces.db`. The command remains `geko`, so the
  installation directory must be on the MCP client's PATH. This checks setup
  generation; it does not claim an interactive MCP-client investigation.
- From `/tmp/elsewhere`, started `geko demo --no-open --listen 127.0.0.1:4318`.
  Readiness and the dashboard passed. The log reported 2,400 synthetic traces,
  seven services and two applications. The process was stopped on completion.

The container base digest was
`golang@sha256:ecb67f987aa1a57794c91d19ade7ae0209f2f0f5496aaef9e6ae85ddf92d02ab`.
The first dashboard probe omitted redirect following and was corrected to
`curl -fsSL`; the completed rerun passed. The hosted Action using release
installation, package managers and other operating systems remain separate
checks.

## Large-database Linux migration

[Raw measurement and provenance](2026-11-24-linux-upgrade-schema15-21.json)
record the schema 15→21 upgrade of 400,000 traces / 800,000 spans. The existing
`TestUpgradeMeasurement` passed:

| Check | Result |
| --- | --- |
| Full upgrade | 32.44 s; trace/span counts unchanged; SQLite quick check passed |
| Peak main + WAL file sizes | 667,755,720 bytes, 1.53× the original 437,407,744-byte fixture |
| Interrupted upgrade | Cancelled after 16.23 s; schema 15 and all data retained |
| Retry | Schema 21 reached in 24.77 s |
| Concurrent reader | Upgrade succeeded; open reader prevented WAL truncation |
| After reader closed | Compaction succeeded; file became 409,374,720 bytes |

Peak main and WAL are sampled independently and summed, so this is a
conservative file-size estimate rather than an atomic measurement of disk
allocation. It excludes backups, the input fixture and the other disposable
test copies. It is an observed sample, not a maximum disk-growth guarantee.

The original Windows fixture was unavailable. The replacement has the same
trace/span scale and workload dimensions but different exact data. The target
schema also changed from 20 to 21. Keep the Windows result; do not infer a
Windows-versus-Linux speed difference from these two measurements.

To reproduce the fixture, extract commit `55d61fe` into a separate directory,
copy [the generator](upgrade-fixture_test.go.txt) to its
`internal/storage/sqlite/upgrade_fixture_local_test.go`, then run there:

```sh
GEKO_CREATE_UPGRADE_FIXTURE=/absolute/new/schema15.db \
  go test -run '^TestLocalUpgradeFixture$' -v -timeout 15m ./internal/storage/sqlite
```

The schema-15 snapshot's production sources in `internal/storage`,
`internal/telemetry`, `internal/privacy` and `internal/investigation`, and its
`go.mod`/`go.sum`, were verified against that commit. Generation rejects an
existing target path. It writes only synthetic data through the old
`Store.WriteSpans`, in batches of at most 512 traces.

From the current tree, on Linux with Go 1.26.11:

```sh
GEKO_UPGRADE_FIXTURE=/absolute/new/schema15.db \
GEKO_UPGRADE_OUT=/absolute/new/linux-upgrade.json \
  go test -run '^TestUpgradeMeasurement$' -v -count=1 -timeout 15m ./internal/storage/sqlite
```

For this run, the Linux test binary was cross-compiled and executed in the
container without networking. The fixture was copied from the bind mount into
the container's temporary Linux filesystem before each timed upgrade. No
benchmarks, builds or other tests ran concurrently with the measurement.
