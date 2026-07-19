.PHONY: lint test preflight

# preflight — the merge gate: gofmt + vet + build + race-test. Postgres-backed
# store/dispatcher tests run for real in CI (GO_WORKFLOW_TEST_DSN +
# WORKFLOW_TEST_POSTGRES_DSN provisioned by .github/workflows/preflight.yml);
# they self-skip only when no DSN is set (a local run without a DB) — CI never
# skips-to-green.
preflight:
	@echo "==> gofmt -l"
	@dirty=$$(gofmt -l . | grep -v '^vendor/' || true); \
	  if [ -n "$$dirty" ]; then \
	    echo "FAIL: gofmt -- run gofmt -w on:"; echo "$$dirty"; exit 1; \
	  fi
	@echo "==> go vet ./..."
	go vet ./...
	@echo "==> go build ./..."
	go build ./...
	@echo "==> go test -race -count=1 ./..."
	go test -race -count=1 ./...

lint:
	golangci-lint run ./...

test:
	go test -v -race -count=1 ./...
