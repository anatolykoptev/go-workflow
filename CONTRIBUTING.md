# Contributing to go-workflow

go-workflow is a library other services build on, so open an issue before changing a public type, a
step kind, the template format or a default. Report vulnerabilities privately, see
[SECURITY.md](SECURITY.md).

## Development

You need the Go version in `go.mod` (CI uses the current stable release) and, for the Postgres
tests, a PostgreSQL server.

```sh
make preflight   # the CI gate: gofmt check, go vet, go build, go test -race -count=1 ./...
make lint        # golangci-lint run ./... (config in .golangci.yml; not part of the gate)
make test        # verbose race tests
```

CI runs `make preflight` on `ubuntu-24.04-arm` for every pull request (workflow `preflight`) with a
PostgreSQL 16 service. The store and dispatcher tests use it through `GO_WORKFLOW_TEST_DSN` and
`WORKFLOW_TEST_POSTGRES_DSN`; set both to a scratch database to run them locally, otherwise they are
skipped. A pull request that changes only documents skips the CI job. If preflight passes with
the database it passes there.

## Tests

A fix comes with a test that fails without it. Prefer the table-driven and `t.Parallel()` style the
existing tests use; pass a `*Metrics` instance rather than the global one. Tests must not need the
network or credentials.

## Commits

Conventional Commits: `feat:`, `fix:`, `perf:`, `refactor:`, `docs:`, `deps:`, `revert:`, and
`chore:`, `ci:`, `test:`, `build:`, `style:` (hidden from the changelog). release-please reads them:
`feat:` makes a minor release, `fix:` a patch, and `feat!:` or a `BREAKING CHANGE:` footer a
breaking one (before 1.0 it raises the minor version). A commit that does not parse is left out of
the release notes, so keep the subject clean. Never tag or edit `CHANGELOG.md` by hand.

## Pull requests

Describe what changed, why, and how it was tested. Add a step kind, a template field or a public
option together with its README text and, for a template field, `docs/template.schema.json`. Record a
structural decision as a new ADR under `docs/adr/`. Update [docs/INDEX.md](docs/INDEX.md) when you add
or rename a document. Keep the repository free of hostnames, IP addresses, operator paths and tokens.
By contributing you agree your work is licensed under Apache-2.0, see [LICENSE](LICENSE).
