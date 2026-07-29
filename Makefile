# kpf Makefile — convenience wrappers for the most common local workflows.
#
# All targets are thin shims around the canonical Go / shell scripts.
# The release target delegates to scripts/build-release.sh, which is
# exactly the script CI runs — keeping local + CI artifacts consistent.
#
# Override VERSION on the command line: `make release VERSION=0.1.2`.
# Override BIN to relocate the local binary: `make build BIN=$PWD/bin/kpf`.

GO      ?= go
BIN     ?= bin/kpf
PKG     ?= ./...
VERSION ?= $(shell grep -oE 'const version = "[^"]+"' cmd/kpf/main.go | head -1 | sed 's/.*"\(.*\)".*/\1/')

# Default: print help so first-time users don't get a confusing "nothing to do".
.DEFAULT_GOAL := help

.PHONY: help
help: ## show this help
	@awk 'BEGIN {FS = ":.*?## "} /^[a-zA-Z_-]+:.*?## / {printf "  \033[36m%-15s\033[0m %s\n", $$1, $$2}' $(MAKEFILE_LIST)

.PHONY: build
build: ## build the kpf binary to $(BIN)
	@mkdir -p $(dir $(BIN))
	$(GO) build -trimpath -o $(BIN) ./cmd/kpf
	@echo "built $(BIN)"

.PHONY: run
run: build ## run the local build
	./$(BIN)

.PHONY: test
test: ## run go test -race
	$(GO) test -race -count=1 $(PKG)

.PHONY: vet
vet: ## run go vet
	$(GO) vet $(PKG)

.PHONY: fmt
fmt: ## gofmt the whole tree
	$(GO) fmt $(PKG)

.PHONY: tidy
tidy: ## go mod tidy
	$(GO) mod tidy

.PHONY: clean
clean: ## remove ./bin and ./dist
	rm -rf bin dist

.PHONY: install
install: build ## install kpf to /usr/local/bin (needs sudo)
	install -m 0755 $(BIN) /usr/local/bin/kpf

# Convenience wrappers for the daemon commands — same as `kpf ...` but
# always point at the just-built binary.
.PHONY: daemon-start daemon-stop ping
daemon-start: build ## start the daemon using the local build
	./$(BIN) daemon start

daemon-stop: build ## stop the daemon
	./$(BIN) daemon stop

ping: build ## ping the daemon
	./$(BIN) ping

.PHONY: release
release: ## build cross-platform tarballs into dist/ (delegates to scripts/build-release.sh)
	./scripts/build-release.sh $(VERSION)

.PHONY: tag
tag: ## create an annotated tag for $(VERSION); use VERSION=0.1.2 to override (push with `git push origin v$(VERSION)`)
	git tag -a v$(VERSION) -m "kpf v$(VERSION)"
	@echo "tagged v$(VERSION)"