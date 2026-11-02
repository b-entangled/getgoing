GO ?= go
PKGS := ./...
PORT ?= 8000
SITE_DIR := site
SITE_PID := $(SITE_DIR)/.server.pid
SITE_LOG := $(SITE_DIR)/.server.log

.PHONY: all build test race vet fmt fmt-check tidy clean cover site serve run stop restart

all: check ## Run every check

build: ## Compile all packages
	$(GO) build $(PKGS)

test: ## Run the test suite
	$(GO) test $(PKGS)

race: ## Run the test suite under the race detector (concurrency pattern packages rely on this)
	$(GO) test -race $(PKGS)

vet: ## Run static analysis
	$(GO) vet $(PKGS)

fmt: ## Format the source tree in place
	gofmt -w .

fmt-check: ## Fail if any file is not gofmt-clean
	@out=$$(gofmt -l .); \
	if [ -n "$$out" ]; then echo "not gofmt-clean:"; echo "$$out"; exit 1; fi

tidy: ## Tidy and verify the module graph
	$(GO) mod tidy
	$(GO) mod verify

cover: ## Test with coverage and print the per-package summary
	$(GO) test -cover -coverprofile=coverage.out $(PKGS)
	$(GO) tool cover -func=coverage.out | tail -n 1

check: fmt-check vet race ## gofmt + vet + race tests

site: ## Build the interactive documentation site into site/index.html
	$(GO) run ./tools/buildsite -out $(SITE_DIR)/index.html

serve: site ## Build the site and serve it in the foreground on PORT (Ctrl-C stops)
	python3 -m http.server $(PORT) --directory $(SITE_DIR)

run: site ## Build the site and serve it in the background on PORT
	@if curl -sf -o /dev/null --max-time 1 http://localhost:$(PORT)/; then \
		echo "already running: http://localhost:$(PORT)"; \
	else \
		nohup python3 -m http.server $(PORT) --directory $(SITE_DIR) \
			>$(SITE_LOG) 2>&1 & echo $$! > $(SITE_PID); \
		sleep 0.4; \
		if curl -sf -o /dev/null --max-time 2 http://localhost:$(PORT)/; then \
			echo "serving http://localhost:$(PORT) (pid $$(cat $(SITE_PID)), log $(SITE_LOG))"; \
		else \
			echo "failed to start — see $(SITE_LOG)"; rm -f $(SITE_PID); exit 1; \
		fi; \
	fi

stop: ## Stop the background site server
	@if [ -f $(SITE_PID) ] && kill -0 $$(cat $(SITE_PID)) 2>/dev/null; then \
		kill $$(cat $(SITE_PID)) && echo "stopped (pid $$(cat $(SITE_PID)))"; \
		rm -f $(SITE_PID); \
	elif pgrep -f "http[.]server $(PORT)" >/dev/null; then \
		pkill -f "http[.]server $(PORT)" && echo "stopped"; \
	else \
		echo "not running"; \
	fi

restart: stop run ## Restart the background site server (rebuilds first)

clean: ## Remove build and coverage artifacts
	rm -f coverage.out $(SITE_PID) $(SITE_LOG)
