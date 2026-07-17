# Changelog

All notable changes to this project will be documented in this file.

## [0.17.2](https://github.com/anatolykoptev/go-workflow/compare/v0.17.1...v0.17.2) (2026-07-17)


### Bug Fixes

* **approval:** route pending_approval + HandleApproval through BlockingStep SSOT ([#28](https://github.com/anatolykoptev/go-workflow/issues/28)) ([d05abb7](https://github.com/anatolykoptev/go-workflow/commit/d05abb79360e473465c6834036fd08b9ce9715a5))

## [0.17.1](https://github.com/anatolykoptev/go-workflow/compare/v0.17.0...v0.17.1) (2026-07-16)


### Bug Fixes

* **reopen:** select step via CurrentStep instead of pending-approval count ([#20](https://github.com/anatolykoptev/go-workflow/issues/20)) ([1bd6c5c](https://github.com/anatolykoptev/go-workflow/commit/1bd6c5c2d681352f872ed2abd1dadb711bccac86))

## [0.17.0](https://github.com/anatolykoptev/go-workflow/compare/v0.16.1...v0.17.0) (2026-07-16)


### Features

* add Engine.Reopen + wf_reopen MCP tool to resume cancelled workflows ([#16](https://github.com/anatolykoptev/go-workflow/issues/16)) ([#17](https://github.com/anatolykoptev/go-workflow/issues/17)) ([1a9af8e](https://github.com/anatolykoptev/go-workflow/commit/1a9af8e6501825b56585b1e8b1a6f4ea6d7d9e2e))

## [0.16.1](https://github.com/anatolykoptev/go-workflow/compare/v0.16.0...v0.16.1) (2026-06-21)


### Bug Fixes

* **breaker:** wrap LLM tool-loop calls in the shared circuit breaker ([#14](https://github.com/anatolykoptev/go-workflow/issues/14)) ([e2623db](https://github.com/anatolykoptev/go-workflow/commit/e2623dbb150e1835dd8ad103d636e8b8809c1d1d))

## [0.16.0](https://github.com/anatolykoptev/go-workflow/compare/v0.15.0...v0.16.0) (2026-06-21)


### Features

* adopt go-kit resilience primitives (circuit breaker, retry jitter + Retry-After, QPS rate limit) ([8cd2d17](https://github.com/anatolykoptev/go-workflow/commit/8cd2d17128e418163aa4ac66935a8a654120c985))


### Bug Fixes

* **testdb:** add test-DB isolation guard ([#11](https://github.com/anatolykoptev/go-workflow/issues/11)) ([c49834c](https://github.com/anatolykoptev/go-workflow/commit/c49834c46390d907faee077e2fadc0f6adb0b22c))


### Documentation

* document circuit breaker, retry jitter/Retry-After, and rate limiting in features ([7a7ab89](https://github.com/anatolykoptev/go-workflow/commit/7a7ab8938fe3528cbbd20eb0c029c1a2fa9deacc))
* drop stale hardcoded version from README header; clean up step-types line ([2366086](https://github.com/anatolykoptev/go-workflow/commit/2366086ba8955ad3dd931f862b8fc4abf421a81e))
* use canonical Apache-2.0 license text so GitHub detects the license ([6183628](https://github.com/anatolykoptev/go-workflow/commit/6183628a77b902464142a3b5e7c1a368243e316e))

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
