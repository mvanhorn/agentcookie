# agentcookie Makefile
#
# Targets:
#   make            - build and sign bin/agentcookie (default; not notarized)
#   make build      - go build ./cmd/agentcookie -> bin/agentcookie (native arch)
#   make build-universal
#                   - build a Universal 2 (arm64 + x86_64) bin/agentcookie
#                     via lipo, so one signed binary runs on both Apple
#                     Silicon and Intel Macs
#   make install    - go install ./cmd/agentcookie, then sign $(GOBIN)/agentcookie
#   make sign       - sign bin/agentcookie with the Developer ID identity
#   make notarize   - submit bin/agentcookie to Apple's notary service
#                     (5-30 min; required before deploying to a Mac other
#                     than the one this build ran on)
#   make release    - build-universal + sign + notarize in one shot (a
#                     fully-portable Universal 2 binary that launches on any
#                     Intel or Apple Silicon Mac without prompts)
#   make verify     - print the designated requirement of bin/agentcookie
#   make test       - go test -race ./...
#   make vet        - go vet ./...
#   make clean      - remove bin/
#
# Build alone does not require an Apple Developer ID. Signing is split into
# `make sign` so contributors can `make build` and `make test` without a
# cert. CI release builds run `make` (build + sign) on a signing-enabled
# macOS runner.
#
# Override the signing identity by exporting AGENTCOOKIE_SIGN_IDENTITY. See
# docs/runbook-v0.12-codesign.md for how to install / renew the cert.

SHELL := /bin/bash
BIN_DIR := bin
BINARY := $(BIN_DIR)/agentcookie
PKG := ./cmd/agentcookie

# Inject the version at link time so `make build` / `make install` -- and the
# CI release build, which runs `make` (see the comment above) -- report the
# real tag instead of the "0.0.1-dev" default baked into internal/cli.Version.
# Mirrors the -X ldflag in .goreleaser.yaml. `git describe` yields e.g. 0.17.1
# on a tagged build or 0.17.1-2-gfe6f405 between tags; the leading v is stripped
# to match goreleaser's {{ .Version }}. Falls back to the in-source default when
# git is unavailable (e.g. building from a release tarball).
VERSION := $(shell git describe --tags --always --dirty 2>/dev/null | sed 's/^v//')
ifneq ($(VERSION),)
LDFLAGS := -X github.com/mvanhorn/agentcookie/internal/cli.Version=$(VERSION)
endif

GOBIN := $(shell go env GOBIN)
ifeq ($(GOBIN),)
GOBIN := $(shell go env GOPATH)/bin
endif

.PHONY: all build build-universal install sign notarize release verify test vet clean

all: build sign

release: build-universal sign notarize

build:
	@mkdir -p $(BIN_DIR)
	go build -ldflags "$(LDFLAGS)" -o $(BINARY) $(PKG)

# build-universal - Produce a Universal 2 (arm64 + x86_64) binary at
# bin/agentcookie via lipo so a single signed + notarized release runs
# natively on both Apple Silicon and Intel Macs. CGO must stay enabled
# (the codebase links C for SQLite + the macOS Keychain), so each slice is
# built separately with the target arch pinned through the C compiler
# (CC="clang -arch ..."). Pinning CC per slice makes the build host
# irrelevant: it works whether run on an arm64 or an x86_64 Mac.
build-universal:
	@mkdir -p $(BIN_DIR)
	CGO_ENABLED=1 GOOS=darwin GOARCH=arm64 CC="clang -arch arm64" \
	  go build -ldflags "$(LDFLAGS)" -o $(BIN_DIR)/agentcookie-arm64 $(PKG)
	CGO_ENABLED=1 GOOS=darwin GOARCH=amd64 CC="clang -arch x86_64" \
	  go build -ldflags "$(LDFLAGS)" -o $(BIN_DIR)/agentcookie-amd64 $(PKG)
	lipo -create $(BIN_DIR)/agentcookie-arm64 $(BIN_DIR)/agentcookie-amd64 -output $(BINARY)
	@rm -f $(BIN_DIR)/agentcookie-arm64 $(BIN_DIR)/agentcookie-amd64
	@echo "make build-universal: wrote $(BINARY) (archs: $$(lipo -archs $(BINARY)))"

# Install to $(GOBIN)/agentcookie and sign in place so steady-state
# `make install` produces a signed binary with the same designated
# requirement as the local build.
install:
	go install -ldflags "$(LDFLAGS)" $(PKG)
	scripts/sign.sh "$(GOBIN)/agentcookie"

sign:
	@if [[ ! -f $(BINARY) ]]; then \
	  echo "make sign: $(BINARY) does not exist; run \`make build\` first" >&2; \
	  exit 1; \
	fi
	scripts/sign.sh $(BINARY)

notarize:
	@if [[ ! -f $(BINARY) ]]; then \
	  echo "make notarize: $(BINARY) does not exist; run \`make build && make sign\` first" >&2; \
	  exit 1; \
	fi
	scripts/notarize.sh $(BINARY)

verify:
	@if [[ ! -f $(BINARY) ]]; then \
	  echo "make verify: $(BINARY) does not exist; run \`make build\` first" >&2; \
	  exit 1; \
	fi
	codesign -d -r- $(BINARY)

test:
	go test -race ./...

vet:
	go vet ./...

clean:
	rm -rf $(BIN_DIR)
