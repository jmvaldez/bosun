# Every target builds with CGO_ENABLED=0 (LESSONS L-50); bosun is a static binary.
export CGO_ENABLED := 0

BIN      := bin/bosun
PREFIX   ?= $(HOME)/.local
INSTALL_DIR := $(PREFIX)/bin
VERSION  := $(shell cat VERSION)
# Tool versions are pinned so CI and local runs agree.
GORELEASER_VERSION  := v2.19.0
STATICCHECK_VERSION := v0.8.2
TARGETS  := linux/amd64 linux/arm64 darwin/amd64 darwin/arm64

.PHONY: build install test test-short vet cross check clean release staticcheck

build:
	go build -o $(BIN) .

# Copy to a temp file in the target dir, then rename over the binary. mv within
# one filesystem is atomic, so a running bosun (or a hook mid-exec) never sees a
# half-written file, and replacing a running binary does not fail with ETXTBSY.
install: build
	mkdir -p $(INSTALL_DIR)
	tmp=$$(mktemp $(INSTALL_DIR)/.bosun.XXXXXX) && \
	  cp $(BIN) "$$tmp" && chmod 755 "$$tmp" && mv -f "$$tmp" $(INSTALL_DIR)/bosun

vet:
	go vet ./...

test:
	go test ./...

test-short:
	go test -short ./...

# Build and vet every release target; the binaries are discarded.
cross:
	@set -e; for t in $(TARGETS); do \
	  os=$${t%/*}; arch=$${t#*/}; \
	  echo "build $$os/$$arch"; \
	  GOOS=$$os GOARCH=$$arch go build -o /dev/null . ; \
	  GOOS=$$os GOARCH=$$arch go vet ./... ; \
	done

check: vet test cross staticcheck

# staticcheck and goreleaser run from the module cache with `go run`; nothing is
# installed system-wide.
staticcheck:
	go run honnef.co/go/tools/cmd/staticcheck@$(STATICCHECK_VERSION) ./...

# A local snapshot of everything a release builds (tarballs, .deb, .rpm,
# checksums, the brew formula) into the gitignored dist/. Needs a git checkout
# with at least one commit: goreleaser reads the commit for the snapshot version.
release:
	BOSUN_VERSION=$(VERSION) go run github.com/goreleaser/goreleaser/v2@$(GORELEASER_VERSION) release --snapshot --clean

clean:
	rm -rf bin dist
