GO ?= go

.PHONY: build test fmt vet check run

build:
	$(GO) build -ldflags "-X github.com/neverbot/maestro/internal/version.Version=$(shell git rev-parse --short HEAD)" -o bin/maestro ./cmd/maestro

test:
	$(GO) test ./...

fmt:
	gofmt -w .

vet:
	$(GO) vet ./...

check: vet test
	@test -z "$$(gofmt -l . | tee /dev/stderr)" || (echo "gofmt found unformatted files" && exit 1)

run: build
	./bin/maestro
