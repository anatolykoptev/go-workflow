# Documentation index

| Document | Description |
|---|---|
| [README](../README.md) | User guide: features, quick start, MCP integration, templates, approval flow, storage, step types, cost, retry, interfaces, distributed execution |
| [CHANGELOG](../CHANGELOG.md) | Release history, written by release-please |
| [SECURITY](../SECURITY.md) | How to report a vulnerability, dependency policy and residual risks |
| [CODE_OF_CONDUCT](../CODE_OF_CONDUCT.md) | Contributor Covenant 3.0 |
| [LICENSE](../LICENSE) | Apache License 2.0 |
| [COMPETITORS](COMPETITORS.md) | How the engine compares with other workflow engines and where it is positioned |
| [PERSISTENCE](PERSISTENCE.md) | The StoreBackend interface and the file, SQLite and PostgreSQL backends |
| [ROADMAP](ROADMAP.md) | Milestones from v0.1 on, done and planned |
| [adr/0001-flat-package-with-subpackages](adr/0001-flat-package-with-subpackages.md) | Decision: core in the root package, storage backends in store/ |
| [adr/0002-pluggable-storage-backends](adr/0002-pluggable-storage-backends.md) | Decision: persistence behind the StoreBackend interface |
| [adr/0003-metrics-dependency-injection](adr/0003-metrics-dependency-injection.md) | Decision: inject Metrics instead of a global singleton |
| [adr/0004-executor-per-file-organization](adr/0004-executor-per-file-organization.md) | Decision: one executor_<kind>.go file per step kind |
| [CONTRIBUTING](../CONTRIBUTING.md) | Build, gate with the Postgres service, tests, commit style and pull-request rules |
| [CLAUDE](../CLAUDE.md) | Notes for coding agents: layout, the exact CI gate with its Postgres service, invariants (store split, injected metrics, approval gate, guarded outbound calls) |
