# Changelog

All notable changes to this project will be documented in this file.

## v0.13.1 — typed markers wired into instantiateStep

### Fixed

- **`templates.go::instantiateStep`** previously had its own `strings.ReplaceAll` substitution loop that bypassed `ResolveRefs(Err)` entirely. Templates using the typed markers introduced in v0.13.0 leaked the literal `"@@int:NAME"` text into Step config, causing downstream JSON-schema validators to reject the call with `type "string", want "integer"`.
- The same path is now `ResolveRefsErr(string(ts.Config), &Workflow{Context: merged})`, the canonical entry point. Coercion errors propagate as `step <id>: typed-marker substitution failed: <reason>`. The Retry block uses the same path. Three new tests in `template_typed_markers_test.go` cover `@@int`, `@@bool`, and classic `{{x}}` preservation.

## v0.13.0 — typed template markers

### Added

- **`@@int:NAME` / `@@bool:NAME` / `@@float:NAME`** — typed substitution markers wrapped in JSON quotes inside the template (so the file remains valid JSON pre-substitution). After substitution, the surrounding quotes are stripped and the value is emitted as a bare typed JSON literal. Required when the downstream consumer (e.g. an MCP tool's JSON-schema validator) demands a non-string type.
- **`ResolveRefsErr(s, wf) (string, error)`** — explicit-error variant of `ResolveRefs`. Returns a typed coercion error when a marker references a missing key or a value that cannot be coerced (e.g. `@@int:` against `"abc"`). The legacy `ResolveRefs` keeps its signature; on coercion error it logs via `slog.Warn` and continues — backward-compatible with all existing callers.
- **`coerceTyped(kind, v)`** — internal helper accepts `int`, `int64`, `float64`, `bool`, or numeric/boolean strings; rejects anything else with a wrapped error.

### Why

`Template.Config` is `json.RawMessage` and substitution runs on the raw bytes before unmarshal. The classic `{{key}}` syntax preserves the surrounding JSON quotes from the template literal (so `"x": "{{n}}"` with `n=5` becomes `"x": "5"`), erasing types. Templates that needed non-string params had to either hardcode the value at template-author time (defeating per-call parametrization) or rely on the consumer to coerce strings — fragile and tool-specific. Typed markers solve this without breaking JSON validity at template load time.

## v0.12.0 — alias loader + ValidateTemplate + engine.go split

### Added

- Template alias loader and `ValidateTemplate` for explicit pre-flight checks.
- Internal `engine.go` split for readability — no external API change.

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
