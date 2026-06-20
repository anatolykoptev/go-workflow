# Security

## Reporting

Report vulnerabilities via GitHub Security Advisories: https://github.com/anatolykoptev/go-workflow/security/advisories/new

For general questions, email the maintainer rather than filing public issues. Coordinate disclosure
windows for any vulnerability that could affect downstream consumers.

## Dependency policy

Application dependencies are kept current via `go list -m -u all` review at each milestone.
The current third-party set is pinned in `go.mod`/`go.sum`; transitive vulnerabilities are
checked with `govulncheck ./...` before each release tag.

## Residual risks (v0.13.x or later — Last checked: 2026-06)

`govulncheck ./...` reports 8 vulnerabilities in the Go standard library shipped with the
host toolchain (`go1.26`, fixed in `go1.26.1`/`go1.26.2`):

| Vuln ID         | Package        | Fixed in   | Reachable from                        |
|-----------------|----------------|------------|---------------------------------------|
| GO-2026-4947    | crypto/x509    | go1.26.2   | replay.go via bufio.Scanner.Scan      |
| GO-2026-4946    | crypto/x509    | go1.26.2   | replay.go via bufio.Scanner.Scan      |
| GO-2026-4870    | crypto/tls     | go1.26.2   | executor_mcp.go RoundTrip / replay.go |
| GO-2026-4866    | crypto/x509    | go1.26.2   | replay.go via bufio.Scanner.Scan      |
| GO-2026-4602    | os             | go1.26.1   | store/file.go loadAll                 |
| GO-2026-4601    | net/url        | go1.26.1   | executor_llm.go via llm.Client.Stream |
| GO-2026-4600    | crypto/x509    | go1.26.1   | replay.go via bufio.Scanner.Scan      |
| GO-2026-4599    | crypto/x509    | go1.26.1   | replay.go via bufio.Scanner.Scan      |

Mitigation: rebuild downstream binaries with go1.26.2 or newer once it lands in the host
toolchain. Library code in this repo does not pin a Go runtime; consumers control the
runtime they ship with.

No third-party module vulnerabilities are reachable from in-package symbols.

## Webhook trigger hardening

`WebhookTrigger` (added in v0.11.0) supports two auth modes for production use:

* `WebhookAuthBearer` — constant-time `Authorization: Bearer <secret>` comparison.
* `WebhookAuthHMAC` — `hmac.New(sha256.New, secret)` over the raw body, hex-encoded
  signature in a configurable header (e.g. `X-Hub-Signature-256`), constant-time compared.

`WebhookAuthNone` is intended only for development and explicitly logs that the endpoint
is unauthenticated. Request bodies are capped at 10 MiB to bound memory.
