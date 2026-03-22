# go-workflow

DAG workflow engine. **Library, not a service** — no `main.go`, no `cmd/`. Consumers import it.

## Rules

- Two packages only: root `workflow` + `store`. Do not add more
- All deps injected via interfaces + `EngineOption` funcs
- Store updates: always use `store.Modify(id, func(w *Workflow))` — atomic load+apply+save. Never modify workflow and call Save separately

## Gotchas

- `StartAsync` detaches from caller context (uses `context.Background()`) — workflow outlives the HTTP request that started it
- `HandleApproval(id, bool)` only works when `State == StateWaitingApproval` — returns error otherwise. After approve, call `ResumeAsync` to continue
- `StepKind` has n8n aliases: `"if"` → condition, `"http_request"` → tool. Always use `NormalizeStepKind()` when comparing
- Template `Config` is `json.RawMessage` — variable `{{key}}` substitution happens on raw string before JSON unmarshal, not on parsed map
- ForEach/BranchAll create dynamic sub-steps at runtime — they won't appear in initial step list
- Step result goes to both `step.Result` AND `wf.Context[step.ID]` — downstream steps reference via `$steps.{id}.result`
- `MCPToolRunner` uses lazy connect + auto-discover — first call to unknown tool triggers connect to all servers

## Consumers

vaelor, krolik-agent, go-wp (planned)
