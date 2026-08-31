GO ?= go
VERSION ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo dev)

.PHONY: build test fmt vet check run

build:
	$(GO) build -ldflags "-X github.com/neverbot/maestro/internal/version.Version=$(VERSION)" -o bin/maestro ./cmd/maestro

test:
	$(GO) test -race ./...

fmt:
	gofmt -w .

vet:
	$(GO) vet ./...

check:
	@test -z "$$(gofmt -l . | tee /dev/stderr)" || (echo "gofmt found unformatted files" && exit 1)
	$(MAKE) vet
	$(MAKE) test

run: build
	./bin/maestro
