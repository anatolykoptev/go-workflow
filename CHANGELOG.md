# Changelog

All notable changes to this project will be documented in this file.

## v0.11.0 — M2: production readiness

### Added

- **OpenTelemetry tracing** (`tracing.go`).
  - `WithTracerProvider(tp)` opts the engine into OTel.
  - One `workflow.run` parent span per workflow; one `step.<kind>` child span per step.
  - Span attributes: `step.id`, `step.kind`, `step.duration_ms`, `step.cache_hit`,
    plus `step.input_tokens` / `step.output_tokens` / `step.usd_estimate` for
    cost-bearing executors.
  - Errors classified into `step.error.kind` (`budget_exceeded` vs
    `executor_error`) with `step.error.message`.
  - No-op via `go.opentelemetry.io/otel/trace/noop` when not configured —
    behaviour identical to v0.10.x.

- **HTTP webhook triggers** (`webhook_trigger.go`).
  - `Engine.RegisterWebhooks(mux, runtime, []WebhookTrigger{...})` attaches
    POST handlers that instantiate a template and start the workflow async.
  - Auth modes: `WebhookAuthNone` (dev), `WebhookAuthBearer`,
    `WebhookAuthHMAC` (constant-time compare; GitHub-style `sha256=` prefix
    accepted).
  - Body cap 10 MiB enforced before auth.
  - Custom `VarMapper` per trigger; default parses JSON body.
  - Responds 202 with `{workflow_id, template, status}`.

- **Content-addressed step caching** (`step_cache.go`).
  - `WithStepCache(cache)` plus `WithStepCacheKinds(...)` — defaults to
    `{StepTransform}` when no allowlist is set.
  - Cache key: SHA-256 of canonicalized JSON of (kind, config, depends_on).
  - Two backends: `InMemoryCache` (process-local) and `FileCache` (JSON files
    under a directory, atomic temp+rename writes, owner-only file modes).
  - Hits replay both Output AND Cost so `Workflow.Cost` totals correctly.
  - TTL per-entry; expired entries fall through to fresh execution.
  - New metrics: `StepCacheHits`, `StepCacheMisses`, `WebhooksReceived`,
    `WebhooksRejected`.

- **`SECURITY.md`** documenting residual stdlib vulnerabilities (8 vulns in
  Go 1.26.0, all fixed in 1.26.1 / 1.26.2 — host toolchain upgrade) and the
  webhook auth hardening contract.

### Changed

- Test count 407 → 478 (+71 across tracing, webhook, cache).
- `Engine` struct gains private fields `tracerProvider`, `tracer`,
  `stepCache`, `stepCacheKinds`. Constructor signature unchanged.

### Notes

- `go-kit` is at the latest tagged release (v0.24.1).
- All eight stdlib vulnerabilities reported by `govulncheck ./...` require
  a Go toolchain upgrade; no library code change addresses them.

## v0.10.0 — AI-native primitives + cost tracking

See git log: tag `v0.10.0`.
