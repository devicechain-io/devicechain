GIT_COMMIT := $(shell git rev-list -1 HEAD)
BUILDDIR ?= $(CURDIR)/build

# Single source of truth for the app/image version: the repo-root VERSION file.
# Stamped into BOTH the CLI version and the default image version, so
# `dcctl version` and the default deployed image tag always match.
REPO_ROOT := $(abspath $(CURDIR)/../..)
VERSION := $(shell cat $(REPO_ROOT)/VERSION 2>/dev/null || echo dev)

LDFLAGS := -X github.com/devicechain-io/dcctl/cmd.gitCommit=$(GIT_COMMIT) \
           -X github.com/devicechain-io/dcctl/cmd.Version=$(VERSION) \
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
