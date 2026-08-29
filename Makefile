BINARY := mcpd
OUT_DIR := bin
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
DATE ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS := -s -w \
	-X github.com/kemalnw/mcpd/internal/version.Version=$(VERSION) \
	-X github.com/kemalnw/mcpd/internal/version.Commit=$(COMMIT) \
	-X github.com/kemalnw/mcpd/internal/version.Date=$(DATE)

.PHONY: build test test-race vet fmt fmt-check tidy check clean

build:
	@mkdir -p $(OUT_DIR)
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o $(OUT_DIR)/$(BINARY) ./cmd/mcpd

test:
	go test ./... -count=1

test-race:
	go test -race ./... -count=1

vet:
	go vet ./...

fmt:
	gofmt -w ./cmd ./internal

fmt-check:
	@test -z "$$(gofmt -l ./cmd ./internal)" || (echo "run 'make fmt'" && gofmt -d $$(gofmt -l ./cmd ./internal) && exit 1)

tidy:
	go mod tidy

check: fmt-check vet test
	@git diff --check

clean:
	rm -rf $(OUT_DIR) coverage.txt coverage.html
