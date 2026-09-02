GO ?= go
VERSION ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo dev)

.PHONY: build test fmt vet lint check run tools sqlc sqlc-check dev dev-down dev-logs dev-psql

build:
	$(GO) build -ldflags "-X github.com/neverbot/maestro/internal/version.Version=$(VERSION)" -o bin/maestro ./cmd/maestro

test:
	$(GO) test -race ./...

fmt:
	gofmt -w .

vet:
	$(GO) vet ./...

lint:
	golangci-lint run

# docs-check is deliberately not part of this gate: it would lint the
# documentation site this project's own docs live in, and no such
# generator exists yet in this repository (see docs/superpowers/plans,
# which is plain Markdown consumed by nothing but a human reader today).
# Adding docs-check here ahead of that generator existing would either be
# a no-op with a misleading name or block every commit on a tool that
# doesn't ship anything. Whoever builds the generator adds docs-check to
# this list in the same change.
check:
	@test -z "$$(gofmt -l . | tee /dev/stderr)" || (echo "gofmt found unformatted files" && exit 1)
	$(MAKE) vet
	$(MAKE) lint
	$(MAKE) sqlc-check
	$(MAKE) test

run: build
	./bin/maestro

tools:
	$(GO) install github.com/sqlc-dev/sqlc/cmd/sqlc@latest

sqlc:
	sqlc generate

sqlc-check:
	sqlc diff

# A local instance in Docker: the binary and its own Postgres, separate
# from the container the Go tests use. `dev` rebuilds and waits for
# health, so it is also how you pick up a code change. The stack keeps
# its data in a named volume across restarts; `dev-down` stops it and
# leaves that volume alone, which is deliberate — a game you seeded to
# try something out survives until you remove the volume yourself.
dev:
	docker compose up --build -d
	@echo "Maestro on http://localhost:$${MAESTRO_HOST_PORT:-8090} (admin@example.com / change-me-please)"

dev-down:
	docker compose down

dev-logs:
	docker compose logs -f maestro

dev-psql:
	docker compose exec db psql -U maestro -d maestro
