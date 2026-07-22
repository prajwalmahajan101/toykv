.PHONY: build run cli tui fmt fmt-check vet lint test bench bench-prep compat compat-prep chaos chaos-smoke ci hooks clean help

GO          ?= go
GOFMT       ?= gofmt
PKGS        ?= ./...
TIMEOUT     ?= 5m
BIN         ?= bin
ADDR        ?= :6390
DIR         ?= ./data
BENCH_HOST  ?= 127.0.0.1
BENCH_PORT  ?= 6390
BENCH_N     ?= 100000
# v2 default exercises the typed keyspace (lists + hashes), not just strings.
BENCH_TESTS ?= set,get,lpush,rpush,hset
# §5 redis-client compat sweep runs redis-cli from this image via Docker,
# so no local redis/valkey install is needed. Same image family as bench.
COMPAT_IMAGE ?= valkey/valkey:8-alpine

help:
	@echo "Targets:"
	@echo "  build      - build all three binaries into $(BIN)/"
	@echo "  run        - run the server on $(ADDR) using $(DIR)"
	@echo "  cli        - run toykv-cli against $(ADDR)"
	@echo "  tui        - run toykv-tui against $(ADDR)"
	@echo "  fmt        - gofmt -w on all .go files"
	@echo "  fmt-check  - fail if gofmt would change anything"
	@echo "  vet        - go vet ./..."
	@echo "  lint       - golangci-lint run ./..."
	@echo "  test       - go test -race -timeout $(TIMEOUT) ./..."
	@echo "  bench-prep - print bench methodology and verify redis-benchmark"
	@echo "  bench      - redis-benchmark -h $(BENCH_HOST) -p $(BENCH_PORT) -t $(BENCH_TESTS) -n $(BENCH_N)"
	@echo "  compat-prep- verify Docker + pull $(COMPAT_IMAGE) for the redis-cli sweep"
	@echo "  compat     - run the redis-cli byte-compat sweep (§5) via Docker, no local install"
	@echo "  chaos      - full soak: go test -race -timeout 10m ./test/chaos/..."
	@echo "  chaos-smoke- short soak: go test -short -race -timeout 2m ./test/chaos/..."
	@echo "  ci         - fmt-check + vet + lint + test"
	@echo "  hooks      - install .githooks as the repo hooksPath"
	@echo "  clean      - remove $(BIN)/ and data/"

build:
	@mkdir -p $(BIN)
	$(GO) build -o $(BIN)/toykv     ./cmd/toykv
	$(GO) build -o $(BIN)/toykv-cli ./cmd/toykv-cli
	$(GO) build -o $(BIN)/toykv-tui ./cmd/toykv-tui

run: build
	./$(BIN)/toykv -addr $(ADDR) -dir $(DIR)

cli: build
	./$(BIN)/toykv-cli -addr $(ADDR)

tui: build
	./$(BIN)/toykv-tui -addr $(ADDR)

fmt:
	$(GOFMT) -w .

fmt-check:
	@out=$$($(GOFMT) -l .); \
	if [ -n "$$out" ]; then \
	  echo "gofmt would reformat:"; \
	  echo "$$out"; \
	  exit 1; \
	fi

vet:
	$(GO) vet $(PKGS)

lint:
	@if ! command -v golangci-lint >/dev/null 2>&1; then \
	  echo "golangci-lint not installed. Install:"; \
	  echo "  go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest"; \
	  exit 1; \
	fi
	golangci-lint run $(PKGS)

test:
	$(GO) test -race -timeout $(TIMEOUT) $(PKGS)

bench-prep:
	@if ! command -v redis-benchmark >/dev/null 2>&1; then \
	  echo "redis-benchmark not installed. Install redis-tools (apt) or redis (brew)."; \
	  exit 1; \
	fi
	@echo "Bench methodology — record one row per fsync policy in docs/BENCHMARKS.md:"
	@echo "  1. Start server:  ./$(BIN)/toykv -addr $(ADDR) -dir $(DIR) -appendfsync <always|everysec|no>"
	@echo "  2. Run:           make bench"
	@echo "  3. Vary knobs via BENCH_HOST / BENCH_PORT / BENCH_N / BENCH_TESTS."
	@echo "Current run target: $(BENCH_HOST):$(BENCH_PORT)  -t $(BENCH_TESTS)  -n $(BENCH_N)"

bench: bench-prep
	redis-benchmark -h $(BENCH_HOST) -p $(BENCH_PORT) -t $(BENCH_TESTS) -n $(BENCH_N)

compat-prep:
	@if ! command -v docker >/dev/null 2>&1; then \
	  echo "docker not installed — the §5 compat sweep runs redis-cli from $(COMPAT_IMAGE) via Docker."; \
	  echo "Install Docker, or install redis-tools/redis natively and run: go test -run TestRedisCLI_ByteCompat ./test/e2e"; \
	  exit 1; \
	fi
	@echo "Pulling $(COMPAT_IMAGE) (cached after first run)…"
	@docker pull -q $(COMPAT_IMAGE) >/dev/null
	@echo "Compat tooling ready: scripts/redis-cli -> docker run --network host $(COMPAT_IMAGE) redis-cli"

# Run the redis-client byte-compat sweep (§5) against a fresh subprocess
# server, sourcing redis-cli from Docker via scripts/redis-cli. The Go
# test execs a bare `redis-cli`; prepending scripts/ to PATH makes its
# LookPath resolve to the Docker shim, so no local install is needed.
compat: compat-prep
	TOYKV_COMPAT_IMAGE=$(COMPAT_IMAGE) PATH="$(CURDIR)/scripts:$$PATH" \
	  $(GO) test -race -count=1 -run TestRedisCLI_ByteCompat ./test/e2e -v

chaos:
	$(GO) test -tags=chaos -race -timeout 20m ./test/chaos/...

chaos-smoke:
	$(GO) test -tags=chaos -short -race -timeout 3m ./test/chaos/...

ci: fmt-check vet lint test

hooks:
	git config core.hooksPath .githooks
	@echo "Pre-commit hook installed (.githooks/pre-commit)"

clean:
	rm -rf $(BIN) data
