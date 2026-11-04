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
#   make release-pinned TAG=vX.Y.Z
#                   - the tagged release build: hash the Linux tarballs
#                     already in dist/ (scripts/linux-release-pins.sh), run
#                     `make release` with those digests embedded, and check
#                     the binary carries them. Build the Linux tarballs
#                     first; see docs/releasing.md.
#   make mac-app    - build, sign, notarize and staple agentcookie.app (the
#                     launcher the LaunchAgents start through, so macOS
#                     Login Items shows "agentcookie") and zip it into
#                     internal/macapp/embedded/agentcookie.zip, which
#                     MACAPP=1 builds embed. MACAPP_NOTARIZE=0 skips
#                     notarization for local testing.
#   make verify     - print the designated requirement of bin/agentcookie
#   make test       - go test -race ./... and scripts/release_asset_test.sh
#   make vet        - go vet ./...
#   make clean      - remove bin/
#
# Build alone does not require an Apple Developer ID. Signing is split into
# `make sign` so contributors can `make build` and `make test` without a
# cert. CI release builds run `make release` (universal build + sign +
# notarize) on a signing-enabled macOS runner.
#
# Override the signing identity by exporting AGENTCOOKIE_SIGN_IDENTITY. See
# docs/runbook-v0.12-codesign.md for how to install / renew the cert.

SHELL := /bin/bash
BIN_DIR := bin
BINARY := $(BIN_DIR)/agentcookie
PKG := ./cmd/agentcookie

# Inject the version at link time so `make build` / `make install` -- and the
# CI release build, which runs `make release` (see the comment above) -- report the
# real tag instead of the "0.0.1-dev" default baked into internal/cli.Version.
# Mirrors the -X ldflag in .goreleaser.yaml. `git describe` yields e.g. 0.17.1
# on a tagged build or 0.17.1-2-gfe6f405 between tags; the leading v is stripped
# to match goreleaser's {{ .Version }}. Falls back to the in-source default when
# git is unavailable (e.g. building from a release tarball).
VERSION := $(shell git describe --tags --always --dirty 2>/dev/null | sed 's/^v//')
LDFLAGS :=
ifneq ($(VERSION),)
LDFLAGS += -X github.com/mvanhorn/agentcookie/internal/cli.Version=$(VERSION)
endif

# The SHA256 of this release's published Linux tarballs, embedded so the
# laptop's teammate pairing notice can pin them without downloading
# anything. Empty (the default) builds a binary that sends no pairing
# notice. `make release-pinned` fills them from dist/; see docs/releasing.md.
LINUX_SHA256_AMD64 ?=
LINUX_SHA256_ARM64 ?=
ifneq ($(LINUX_SHA256_AMD64),)
LDFLAGS += -X github.com/mvanhorn/agentcookie/internal/cli.releaseLinuxAMD64SHA256=$(LINUX_SHA256_AMD64)
endif
ifneq ($(LINUX_SHA256_ARM64),)
LDFLAGS += -X github.com/mvanhorn/agentcookie/internal/cli.releaseLinuxARM64SHA256=$(LINUX_SHA256_ARM64)
endif

# agentcookie.app, embedded by MACAPP=1 builds (build tag macapp). See
# internal/macapp and `make mac-app`. AppRevision in internal/macapp is the
# bundle's CFBundleVersion.
SIGN_IDENTITY := $(or $(AGENTCOOKIE_SIGN_IDENTITY),Developer ID Application: Matthew Charles Van Horn (NM8VT393AR))
NOTARY_PROFILE := $(or $(AGENTCOOKIE_NOTARY_PROFILE),agentcookie-notary)
MACAPP ?= 0
MACAPP_DIR := dist/macapp
MACAPP_ICON ?= assets/macapp/icon-1024.png
MACAPP_NOTARIZE ?= 1
MACAPP_ZIP := internal/macapp/embedded/agentcookie.zip
MACAPP_REVISION := $(shell sed -n 's/^[[:space:]]*AppRevision = \([0-9][0-9]*\).*/\1/p' internal/macapp/macapp.go)
GOTAGS :=
ifeq ($(MACAPP),1)
GOTAGS := -tags macapp
endif

GOBIN := $(shell go env GOBIN)
ifeq ($(GOBIN),)
GOBIN := $(shell go env GOPATH)/bin
endif

.PHONY: all build build-universal install sign notarize release release-pinned mac-app check-macapp-zip verify test vet clean

all: build sign

release:
	$(MAKE) mac-app
	$(MAKE) build-universal MACAPP=1
	$(MAKE) sign notarize

# check-macapp-zip - a MACAPP=1 build without the zip would fail to compile
# with a less helpful error, so say what to run.
check-macapp-zip:
	@if [[ "$(MACAPP)" == 1 && ! -s $(MACAPP_ZIP) ]]; then \
	  echo "MACAPP=1: $(MACAPP_ZIP) is missing; run \`make mac-app\` first" >&2; \
	  exit 1; \
	fi

# mac-app - build agentcookie.app around cmd/agentcookie-launcher, sign it
# with the Developer ID identity, notarize and staple it, and zip it to
# $(MACAPP_ZIP) for MACAPP=1 builds to embed.
mac-app:
	@set -euo pipefail; \
	if [[ -z "$(MACAPP_REVISION)" ]]; then echo "make mac-app: cannot read AppRevision from internal/macapp/macapp.go" >&2; exit 1; fi; \
	app="$(MACAPP_DIR)/agentcookie.app"; \
	rm -rf "$(MACAPP_DIR)"; mkdir -p "$$app/Contents/MacOS" "$$app/Contents/Resources"; \
	for a in arm64 amd64; do \
	  CGO_ENABLED=0 GOOS=darwin GOARCH=$$a go build -trimpath -ldflags "-s -w" -o "$(MACAPP_DIR)/agentcookie-launcher_$$a" ./cmd/agentcookie-launcher; \
	done; \
	lipo -create -output "$$app/Contents/MacOS/agentcookie-launcher" "$(MACAPP_DIR)/agentcookie-launcher_arm64" "$(MACAPP_DIR)/agentcookie-launcher_amd64"; \
	sed -e 's/__VERSION__/$(or $(VERSION),0.0.1-dev)/' -e 's/__REVISION__/$(MACAPP_REVISION)/' internal/macapp/Info.plist.tmpl > "$$app/Contents/Info.plist"; \
	plutil -lint "$$app/Contents/Info.plist" >/dev/null; \
	iconset="$(MACAPP_DIR)/AppIcon.iconset"; mkdir -p "$$iconset"; \
	for s in 16 32 128 256 512; do \
	  sips -z $$s $$s "$(MACAPP_ICON)" --out "$$iconset/icon_$${s}x$${s}.png" >/dev/null; \
	  d=$$((s * 2)); sips -z $$d $$d "$(MACAPP_ICON)" --out "$$iconset/icon_$${s}x$${s}@2x.png" >/dev/null; \
	done; \
	iconutil -c icns -o "$$app/Contents/Resources/AppIcon.icns" "$$iconset"; \
	security find-identity -v -p codesigning | grep -qF "$(SIGN_IDENTITY)" || { echo "make mac-app: codesign identity not found: $(SIGN_IDENTITY) (set AGENTCOOKIE_SIGN_IDENTITY)" >&2; exit 1; }; \
	codesign --force --options runtime --timestamp --sign "$(SIGN_IDENTITY)" "$$app/Contents/MacOS/agentcookie-launcher"; \
	codesign --force --options runtime --timestamp --sign "$(SIGN_IDENTITY)" "$$app"; \
	codesign --verify --strict --deep --verbose=2 "$$app"; \
	if [[ "$(MACAPP_NOTARIZE)" != 0 ]]; then \
	  zip="$(MACAPP_DIR)/notarize.zip"; ditto -c -k --keepParent "$$app" "$$zip"; \
	  echo "make mac-app: notarizing $$app (waiting for Apple)"; \
	  xcrun notarytool submit "$$zip" --keychain-profile "$(NOTARY_PROFILE)" --wait --output-format json > "$(MACAPP_DIR)/result.json" || { cat "$(MACAPP_DIR)/result.json" >&2; exit 1; }; \
	  grep -q '"status": *"Accepted"' "$(MACAPP_DIR)/result.json" || { cat "$(MACAPP_DIR)/result.json" >&2; echo "make mac-app: the app was not accepted" >&2; exit 1; }; \
	  xcrun stapler staple "$$app"; \
	  xcrun stapler validate "$$app"; \
	fi; \
	mkdir -p "$$(dirname $(MACAPP_ZIP))"; rm -f "$(MACAPP_ZIP)"; \
	ditto -c -k --norsrc --noextattr --keepParent "$$app" "$(MACAPP_ZIP)"; \
	if unzip -Z1 "$(MACAPP_ZIP)" | grep -qE '(^|/)(\._|__MACOSX)'; then echo "make mac-app: $(MACAPP_ZIP) holds AppleDouble entries, which would break the app's seal when unpacked" >&2; exit 1; fi; \
	echo "make mac-app: wrote $(MACAPP_ZIP) (AppRevision $(MACAPP_REVISION))"

# release-pinned - the tagged release build (docs/releasing.md). Needs the
# Linux tarballs for TAG in dist/ already: their digests are computed here,
# before the darwin build, and embedded in it. The checked-out commit must
# be exactly TAG on a clean tree so the binary's version is the tag.
release-pinned:
	@if [[ -z "$(TAG)" ]]; then \
	  echo "make release-pinned: set TAG, e.g. make release-pinned TAG=v1.3.0" >&2; \
	  exit 2; \
	fi
	@if [[ "v$(VERSION)" != "$(TAG)" ]]; then \
	  echo "make release-pinned: git describe gives '$(VERSION)', not $(TAG); check out the tag on a clean tree" >&2; \
	  exit 2; \
	fi
	set -euo pipefail; \
	pins="$$(scripts/linux-release-pins.sh "$(TAG)")"; \
	echo "make release-pinned: embedding $$pins"; \
	$(MAKE) release $$pins; \
	scripts/linux-release-pins.sh --check-binary $(BINARY) "$(TAG)"

build: check-macapp-zip
	@mkdir -p $(BIN_DIR)
	go build $(GOTAGS) -ldflags "$(LDFLAGS)" -o $(BINARY) $(PKG)

# build-universal - Produce a Universal 2 (arm64 + x86_64) binary at
# bin/agentcookie via lipo so a single signed + notarized release runs
# natively on both Apple Silicon and Intel Macs. CGO must stay enabled
# (the darwin build links C for the macOS Keychain), so each slice is
# built separately with the target arch pinned through the C compiler
# (CC="clang -arch ..."). Pinning CC per slice makes the build host
# irrelevant: it works whether run on an arm64 or an x86_64 Mac.
# Plain `make build` stays native for local development.
build-universal: check-macapp-zip
	@mkdir -p $(BIN_DIR)
	set -euo pipefail; \
	CGO_ENABLED=1 GOOS=darwin GOARCH=arm64 CC="clang -arch arm64" \
	  go build $(GOTAGS) -ldflags "$(LDFLAGS)" -o $(BIN_DIR)/agentcookie-arm64 $(PKG); \
	CGO_ENABLED=1 GOOS=darwin GOARCH=amd64 CC="clang -arch x86_64" \
	  go build $(GOTAGS) -ldflags "$(LDFLAGS)" -o $(BIN_DIR)/agentcookie-amd64 $(PKG); \
	lipo -create \
	  $(BIN_DIR)/agentcookie-arm64 \
	  $(BIN_DIR)/agentcookie-amd64 \
	  -output $(BINARY); \
	archs="$$(lipo -archs $(BINARY))"; \
	case "$$archs" in *arm64*) ;; *) echo "make build-universal: missing arm64 slice ($$archs)" >&2; exit 1 ;; esac; \
	case "$$archs" in *x86_64*) ;; *) echo "make build-universal: missing x86_64 slice ($$archs)" >&2; exit 1 ;; esac; \
	rm -f $(BIN_DIR)/agentcookie-arm64 $(BIN_DIR)/agentcookie-amd64; \
	echo "make build-universal: wrote $(BINARY) (archs: $$archs)"

# Install to $(GOBIN)/agentcookie and sign in place so steady-state
# `make install` produces a signed binary with the same designated
# requirement as the local build, then refresh the LaunchAgents so they
# start through agentcookie.app when this build qualifies. MACAPP=1 (after
# `make mac-app`) embeds the app; without it, a valid installed app is reused.
install: check-macapp-zip
	go install $(GOTAGS) -ldflags "$(LDFLAGS)" $(PKG)
	scripts/sign.sh "$(GOBIN)/agentcookie"
	@# Point existing LaunchAgents at agentcookie.app (or back to the bare
	@# binary) for this build; see `agentcookie services refresh --help`.
	"$(GOBIN)/agentcookie" services refresh || echo "make install: services refresh failed; run it by hand" >&2

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
	bash scripts/release_asset_test.sh

vet:
	go vet ./...

clean:
	rm -rf $(BIN_DIR)
