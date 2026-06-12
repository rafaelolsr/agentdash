# AgentDash — build automation.
#
# `make run` always rebuilds first, so you can never accidentally run a stale
# binary. The build stamps version/commit/date into the binary; see them with
# `agentdash version` or in the dashboard header.

BINARY      := agentdash
PKG         := github.com/datageek/agentdash/internal/version

# Version from the latest git tag (falls back to "dev"); commit short SHA;
# UTC build timestamp. Guarded so it works even outside a git repo.
VERSION     := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT      := $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
DATE        := $(shell date -u +%Y-%m-%dT%H:%M:%SZ)

LDFLAGS := -X $(PKG).Version=$(VERSION) \
           -X $(PKG).Commit=$(COMMIT) \
           -X $(PKG).Date=$(DATE)

.PHONY: all build run install test vet fmt clean tidy

all: build

## build: compile the binary with version stamping
build:
	go build -ldflags "$(LDFLAGS)" -o $(BINARY) .
	@echo "built $(BINARY) — $(VERSION) ($(COMMIT)) at $(DATE)"

## run: rebuild then launch the dashboard (never runs a stale binary)
run: build
	./$(BINARY)

## install: install to GOPATH/bin with version stamping
install:
	go install -ldflags "$(LDFLAGS)" .

## test: run the full unit-test suite
test:
	go test ./...

## vet: run go vet
vet:
	go vet ./...

## fmt: format all Go source
fmt:
	gofmt -w .

## tidy: tidy module dependencies
tidy:
	go mod tidy

## clean: remove the built binary
clean:
	rm -f $(BINARY)
