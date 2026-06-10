.PHONY: build run cli tui fmt fmt-check vet lint test bench ci hooks clean help

GO        ?= go
GOFMT     ?= gofmt
PKGS      ?= ./...
TIMEOUT   ?= 5m
BIN       ?= bin
ADDR      ?= :6390
DIR       ?= ./data

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
	@echo "  bench      - redis-benchmark -p 6390 -t set,get -n 100000"
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

bench:
	@if ! command -v redis-benchmark >/dev/null 2>&1; then \
	  echo "redis-benchmark not installed. Install redis-tools (apt) or redis (brew)."; \
	  exit 1; \
	fi
	redis-benchmark -p 6390 -t set,get -n 100000

ci: fmt-check vet lint test

hooks:
	git config core.hooksPath .githooks
	@echo "Pre-commit hook installed (.githooks/pre-commit)"

clean:
	rm -rf $(BIN) data
