# agentcookie Makefile
#
# Targets:
#   make            - build and sign bin/agentcookie (default; not notarized)
#   make build      - go build ./cmd/agentcookie -> bin/agentcookie
#   make install    - go install ./cmd/agentcookie, then sign $(GOBIN)/agentcookie
#   make sign       - sign bin/agentcookie with the Developer ID identity
#   make notarize   - submit bin/agentcookie to Apple's notary service
#                     (5-30 min; required before deploying to a Mac other
#                     than the one this build ran on)
#   make release    - build + sign + notarize in one shot (a fully-portable
#                     binary that launches on any Mac without prompts)
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

GOBIN := $(shell go env GOBIN)
ifeq ($(GOBIN),)
GOBIN := $(shell go env GOPATH)/bin
endif

.PHONY: all build install sign notarize release verify test vet clean

all: build sign

release: build sign notarize

build:
	@mkdir -p $(BIN_DIR)
	go build -o $(BINARY) $(PKG)

# Install to $(GOBIN)/agentcookie and sign in place so steady-state
# `make install` produces a signed binary with the same designated
# requirement as the local build.
install:
	go install $(PKG)
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
