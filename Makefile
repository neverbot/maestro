GO ?= go
VERSION ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo dev)

# **One pin per tool, read here and by CI.** sqlc was `@latest` in this
# file and `@v1.31.1` in the workflow, which means the machine and the
# runner could disagree about the tool that decides whether the
# generated SQL is current: `make sqlc-check` would pass on one and fail
# on the other, over a diff neither had produced.
#
# golangci-lint must be a v2 release: v1 cannot read the export data of
# a current Go and reports that as typecheck errors across packages that
# compile and vet cleanly, which is a confusing enough failure to pin
# against rather than leave to a lucky day.
GOLANGCI_LINT_VERSION ?= v2.13.2
SQLC_VERSION          ?= v1.31.1

# Where `go install` drops binaries, inside CI and out.
GOBIN ?= $(shell $(GO) env GOPATH)/bin

.PHONY: build test fmt vet lint check run tools sqlc sqlc-check skill-check docs docs-check demo builder dev dev-down dev-logs dev-psql clean-test-dbs clean-docker

build:
	$(GO) build -ldflags "-X github.com/neverbot/maestro/internal/version.Version=$(VERSION)" -o bin/maestro ./cmd/maestro

test:
	$(GO) test -race ./...

fmt:
	gofmt -w .

vet:
	$(GO) vet ./...

# A missing linter is reported as a missing linter. Without this, the
# bare command fails with "command not found" and a 127 that reads as a
# broken repository rather than as a machine that has not run `make
# tools` — or, worse, as a lint failure. The check is on the binary, not
# on PATH, because the answer is the same either way.
lint:
	@command -v golangci-lint >/dev/null || { \
		echo "golangci-lint not found: run 'make tools', and put $$($(GO) env GOPATH)/bin on your PATH"; \
		exit 1; \
	}
	golangci-lint run

# docs-check is part of this gate now. The comment it replaces said it
# was deliberately out of it "until the generator exists, and whoever
# builds the generator adds docs-check to this list in the same change" —
# which is this change: cmd/maestro-docs builds the site and checks that
# every internal link in it resolves. The check that matters is exactly
# the defect that filed the task: the product's onboarding link pointed
# at a page of this site for the whole build and answered 404.
#
# The paragraph below is kept for its argument, which is still right
# about what a check with no generator behind it would have been:
# Adding docs-check ahead of that generator existing would either be
# a no-op with a misleading name or block every commit on a tool that
# doesn't ship anything.
check:
	@test -z "$$(gofmt -l . | tee /dev/stderr)" || (echo "gofmt found unformatted files" && exit 1)
	$(MAKE) vet
	$(MAKE) lint
	$(MAKE) sqlc-check
	$(MAKE) skill-check
	$(MAKE) docs-check
	$(MAKE) test

# The documentation site: built from the readme, the skill bundle and the
# generated design system, into a directory nothing commits. `docs-check`
# builds it and fails on an internal link that points at a page the site
# does not have — the defect that filed this task, which lived for a
# whole build because nothing joined a link to a page.
docs:
	$(GO) run ./cmd/maestro-docs -o site

docs-check:
	@$(GO) run ./cmd/maestro-docs -o $${TMPDIR:-/tmp}/maestro-site-check -check

run: build
	./bin/maestro

# tools installs everything `check` shells out to. Both land in
# $(go env GOPATH)/bin, so that directory has to be on PATH — `make lint`
# and `make sqlc-check` call the binaries by bare name, the way a
# developer would.
#
# golangci-lint is pinned to a v2 line because .golangci.yml declares
# `version: "2"` and a v1 binary cannot read it. A v1 binary also cannot
# read the export data of a current Go standard library, and what it
# reports then is not "your config is old" but typecheck errors in files
# nobody has edited — which is a confusing enough failure to be worth
# pinning against rather than leaving to @latest and a lucky day.
# Installs the pinned tools, and **reinstalls when what is on disk is
# not the pin**. Checking only that a binary exists makes the pin
# decorative: any golangci-lint already in $(GOBIN) — left by another
# project, or by somebody's `@latest` — would win forever, and a
# contributor would lint with a different tool than CI without ever
# being told.
#
# No GOTOOLCHAIN exception here any more. It was needed while this
# project was on Go 1.25.7 and both tools wanted 1.26; the project is on
# 1.27 now, which is newer than either asks for.
tools:
	@$(GOBIN)/golangci-lint version 2>/dev/null | grep -qF ' $(GOLANGCI_LINT_VERSION:v%=%) ' \
		|| $(GO) install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION)
	@$(GOBIN)/sqlc version 2>/dev/null | grep -qxF '$(SQLC_VERSION)' \
		|| $(GO) install github.com/sqlc-dev/sqlc/cmd/sqlc@$(SQLC_VERSION)

sqlc:
	sqlc generate

sqlc-check:
	sqlc diff

# The skill bundle's guards: the generated tool index against the
# registered tools, the anti-restatement scan, the vocabulary fences, the
# page budgets, the routing table and the genre transcripts.
#
# It is the same shape of gate as sqlc-check above and exists for the same
# reason: a generated artefact is committed, and a test fails when the
# source moved and the artefact did not.
#
# **It is a subset of `make test`, not a substitute.** It exists for a
# fast local loop while writing prose. The transcripts need a database,
# so a run of this with no TEST_DATABASE_URL exported skips them and says
# ok; `make test` is what runs everything.
skill-check:
	$(GO) test ./internal/skill/ ./internal/web/ -run 'Skill|Bundle|Genre|ToolReference|Vocab|RoutingTable|Transcript'

# A local instance in Docker: the binary and its own Postgres, separate
# from the container the Go tests use. `dev` rebuilds and waits for
# health, so it is also how you pick up a code change. The stack keeps
# its data in a named volume across restarts; `dev-down` stops it and
# leaves that volume alone, which is deliberate — a game you seeded to
# try something out survives until you remove the volume yourself.
# The demo game, written into the running dev instance: a prerequisite
# cycle, an entity reachable only behind it, one connected to nothing, a
# type declaring more fields than a catalogue draws, three hundred rows
# to page, a document with two versions and an uploaded image.
#
# It exists because three defects in this product were found the first
# time a screen was ever rendered with data in it, each on a game that
# had been seeded by hand and was in nobody's repository. `make dev` then
# `make demo` is the shortest path to a Maestro that can be looked at.
#
# It writes through the domain rather than over the wire, so a demo this
# product would refuse cannot be written; cmd/maestro-demo's own test
# runs the same function against a throwaway database.
demo:
	$(GO) run ./cmd/maestro-demo \
	  -database-url "postgres://maestro:maestro@localhost:$${MAESTRO_DB_HOST_PORT:-5433}/maestro?sslmode=disable" \
	  -slug $${DEMO_SLUG:-demo}

# **This project builds on its own builder, so its cache is its own.**
# Docker's default builder holds one cache for every project on the
# machine, and `docker builder prune` empties all of it: `clean-docker`
# used to do exactly that, which made a target in this repository
# reach into everybody else's work. A named builder is the only way
# Docker offers to draw that line, so this creates one on first use and
# every build and every prune below names it.
BUILDER ?= maestro

# builder creates it if it is not there, and says nothing when it is.
.PHONY: builder
builder:
	@docker buildx inspect $(BUILDER) >/dev/null 2>&1 || \
		docker buildx create --name $(BUILDER) --bootstrap >/dev/null

dev: builder
	BUILDX_BUILDER=$(BUILDER) docker compose up --build -d
	@# The rebuild leaves the image it replaced untagged, and an untagged
	@# image keeps every layer it had. Fifteen rebuilds in an afternoon
	@# was 4.4GB of build cache and 800MB of images that nothing could
	@# name. Pruned by the label the Dockerfile sets, so this touches
	@# Maestro's leftovers and no other project's.
	@docker image prune -f --filter label=org.opencontainers.image.title=maestro >/dev/null 2>&1 || true
	@echo "Maestro on http://localhost:$${MAESTRO_HOST_PORT:-8090} (admin@example.test / change-me-please)"

dev-down:
	docker compose down

dev-logs:
	docker compose logs -f maestro

# The disk this project can leak, in one place.
#
# Test databases: every integration test creates one and drops it, and a
# run that is interrupted never reaches its cleanup. internal/testutil
# sweeps anything over an hour old at the start of the next run, so this
# target is for the impatient and for a machine that is about to run out.
#
# Build cache: scoped to this project's own builder (see BUILDER above),
# so this target empties what Maestro's own rebuilds left and nothing
# else. The default builder's cache is shared by every project on the
# machine and is not this repository's to empty.
clean-test-dbs:
	@docker exec maestro-test-pg psql -U postgres -tAc \
		"select datname from pg_database where datname like 'maestro_test\_%'" 2>/dev/null \
		| while read db; do \
			[ -n "$$db" ] && docker exec maestro-test-pg psql -U postgres -q \
				-c "DROP DATABASE IF EXISTS \"$$db\" WITH (FORCE)" >/dev/null 2>&1; \
		done; \
		echo "test databases left: $$(docker exec maestro-test-pg psql -U postgres -tAc \
			"select count(*) from pg_database where datname like 'maestro_test\_%'" 2>/dev/null)"

clean-docker: clean-test-dbs
	docker image prune -f --filter label=org.opencontainers.image.title=maestro
	@docker buildx inspect $(BUILDER) >/dev/null 2>&1 \
		&& docker builder prune -f --builder $(BUILDER) \
		|| echo "no $(BUILDER) builder yet: nothing of this project's to prune"
	@echo "this project's build cache:"
	@docker buildx du --builder $(BUILDER) 2>/dev/null | tail -1 || true

dev-psql:
	docker compose exec db psql -U maestro -d maestro
