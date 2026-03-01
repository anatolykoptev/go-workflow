# go-workflow Roadmap

## v0.1.0 — Extraction (current)
- [x] Extract from Vaelor pkg/workflow
- [x] Replace Vaelor deps with local interfaces (MessagePublisher, LLMProvider)
- [x] Functional options for Engine construction
- [x] slog for logging (injected)
- [x] All tests passing
- [x] Vaelor migrated to go-workflow

## v0.2.0 — Cron & Triggers
- [ ] Extract cron/scheduler from Vaelor pkg/cron
- [ ] Time-based triggers (at, every, cron expression)
- [ ] Event-based triggers (hook-driven)
- [ ] Trigger→workflow auto-start

## v0.3.0 — Persistence backends
- [ ] Interface for WorkflowStore (currently JSON file only)
- [ ] PostgreSQL backend
- [ ] Redis backend (for distributed setups)

## v0.4.0 — Observability
- [ ] OpenTelemetry tracing (span per step)
- [ ] Prometheus metrics exporter
- [ ] Structured event log (append-only)

## v0.5.0 — Advanced DAG
- [ ] Parallel step groups (fan-out / fan-in)
- [ ] Dynamic step generation (loop/map)
- [ ] Conditional branches (if/else as first-class)
- [ ] Step timeout (per-step, not just global)

## Future
- [ ] Visual DAG editor (web UI)
- [ ] Distributed execution (worker nodes)
- [ ] Workflow versioning & migration
