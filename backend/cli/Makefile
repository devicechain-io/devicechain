GIT_COMMIT := $(shell git rev-list -1 HEAD)
GIT_TREE_STATE := $(shell test -z "$$(git status --porcelain 2>/dev/null)" && echo clean || echo dirty)
BUILD_DATE := $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
BUILDDIR ?= $(CURDIR)/build

# Single source of truth for the app/image version: the repo-root VERSION file.
REPO_ROOT := $(abspath $(CURDIR)/../..)
VERSION := $(shell cat $(REPO_ROOT)/VERSION 2>/dev/null || echo dev)

# A release build is a clean tree sitting exactly on the tag matching VERSION.
# Anything else — every local developer build — is a dev build.
RELEASE_TAG := $(shell git describe --tags --exact-match --match "v$(VERSION)" HEAD 2>/dev/null)
IS_RELEASE := $(shell test -n "$(RELEASE_TAG)" -a "$(GIT_TREE_STATE)" = clean && echo yes)

# The VERSION file is a slow-moving constant: pre-GA it has read 0.0.1 for the
# project's entire history. Stamped bare, it makes every dev build ever produced
# self-report the same version, so two binaries months apart are indistinguishable
# (a stale dcctl on PATH then deploys against a chart that has moved underneath
# it, reporting a version that looks perfectly current). A dev build therefore
# carries a UTC build stamp in its version; a release build does not.
ifeq ($(IS_RELEASE),yes)
BUILD_VERSION := $(VERSION)
else
BUILD_VERSION := $(VERSION)-dev.$(shell date -u +%Y%m%dT%H%M%SZ)
endif

# NOTE: BUILD_VERSION deliberately does NOT feed DefaultImageVersion. That value
# is a published container image tag that must resolve in a registry — a
# timestamped dev version would send `dcctl bootstrap` looking for an image tag
# that has never existed. `dcctl version` prints both so the pairing stays
# visible rather than implied by their being equal.
LDFLAGS := -X github.com/devicechain-io/dcctl/cmd.gitCommit=$(GIT_COMMIT) \
           -X github.com/devicechain-io/dcctl/cmd.gitTreeState=$(GIT_TREE_STATE) \
           -X github.com/devicechain-io/dcctl/cmd.buildDate=$(BUILD_DATE) \
           -X github.com/devicechain-io/dcctl/cmd.Version=$(BUILD_VERSION) \
           -X github.com/devicechain-io/dcctl/bootstrap.DefaultImageVersion=$(VERSION)

# Built within the repo's Go workspace (go.work); dependencies resolve from the
# workspace modules, so there is no separate vendor step. (`go mod vendor` is
# rejected in workspace mode — use `go work vendor` at the repo root if a vendor
# tree is ever needed.)
.PHONY: build
build:
	go build -ldflags "$(LDFLAGS)" -o $(BUILDDIR)/dcctl .

.PHONY: clean
clean:
	rm -rf $(BUILDDIR)/*
