# CLAUDE.md

Notes for coding agents working in go-workflow: a DAG workflow engine library for Go (module
`github.com/anatolykoptev/go-workflow`, `package workflow` plus a `store` subpackage). Steps run in
parallel when dependencies allow; there are 15 step kinds, MCP tool routing, pluggable persistence,
a PostgreSQL step queue for distributed workers, approval gates and crash recovery. Every document
is indexed in [docs/INDEX.md](docs/INDEX.md); the README is the user guide.

## Layout

Root package, one file per concern. Core: `types.go`, `store.go` (`StoreBackend`, `WorkflowStore`),
`dag.go`, `engine*.go` (lifecycle, DAG walk, step runner, validation, recovery, watchdog,
self-heal, security checks, options). One executor per step kind in `executor_<kind>.go`, shared
resolution in `resolve.go` and `subst.go`. Reliability: `backoff_jitter.go`, `breaker_registry.go`,
`ratelimit_registry.go`, `cost_model.go`, `step_cache.go`, `reaper.go`. Distribution:
`dispatcher*.go`, `worker*.go`, `queue_types.go`. Triggers and scheduling: `cron.go`, `scheduler*.go`,
`triggers*.go`, `webhook_trigger.go`. Import: `n8n_*.go`. Observability: `metrics*.go`,
`tracing.go`, `eventlog.go`.

`store/` holds the concrete backends (file, SQLite, PostgreSQL, step queue) and the embedded SQL
migrations in `store/migrate/{postgres,sqlite}`. `docs/adr/` records the structural decisions;
`docs/template.schema.json` is the template JSON Schema referenced from the README.

## Gate

CI (`.github/workflows/preflight.yml`, job `preflight`, `ubuntu-24.04-arm`, Go `stable`) starts a
PostgreSQL 16 service and runs `make preflight`: `gofmt -l` (vendor excluded) must print nothing,
`go vet ./...`, `go build ./...`, `go test -race -count=1 ./...`. The Postgres-backed store and
dispatcher tests read `GO_WORKFLOW_TEST_DSN` and `WORKFLOW_TEST_POSTGRES_DSN`; CI sets both, and a
local run without a database skips them, so a green local run does not cover Postgres. `make lint`
runs golangci-lint (config in `.golangci.yml`) and is not in the gate. A docs-only PR (Markdown,
`docs/`, licence, issue and PR templates) skips the `preflight` job through the `docs-scope` job in
the same workflow; that block is canonical across the fleet and compared byte for byte, so do not
edit it here.

## Invariants that are easy to break

- **The root package stays free of backend drivers.** SQLite and PostgreSQL code lives in `store/`
  (ADR-0001) so importing `workflow` does not pull drivers in. New backends go in `store/`.
- **`WorkflowStore` clones on the way in and out; backends do not.** A backend must not hand out its
  own pointers, and `Modify` is the only atomic read-modify-write.
- **Metrics are injected** (`*Metrics` through options and constructors, ADR-0003); `GlobalMetrics`
  is a deprecated fallback. New code must not read it.
- **One executor per file, named `executor_<kind>.go`** (ADR-0004); a new step kind needs its
  executor, its schema entry in `docs/template.schema.json`, validation in `engine_validate.go` and
  a README row.
- **Approval status and approval handling must agree on the blocking gate.** `wf_status` and
  `HandleApproval` resolve the target through the same helper (issue 23); `step_id` addresses one
  gate explicitly. Tests `approval_ssot_test.go` and `approval_addressable_test.go` pin this.
- **Typed template parameters are preferred** to the deprecated `"@@int:KEY"` markers; the markers
  keep working and log a deprecation, so do not remove them without a major version.
- **Outbound calls are guarded:** per-endpoint circuit breaker, optional rate limit, retry with
  jittered backoff that honours `Retry-After`, and the cost budget (`ErrBudgetExceeded`). A new
  outbound executor must go through them.
- **Webhook triggers compare secrets in constant time** and cap the body at 10 MiB. Keep both.
- **SQL migrations are embedded, numbered and idempotent** (`CREATE ... IF NOT EXISTS`); add a new
  numbered file, never edit an applied one.
- **Versions come from release-please**, which reads Conventional Commits. Never tag or edit
  `CHANGELOG.md` or `.release-please-manifest.json` by hand.
- **This repository is public.** No hostnames, IP addresses, operator paths, account names, tokens
  or personal data in any tracked file, tests, fixtures and examples included.
