GIT_COMMIT := $(shell git rev-list -1 HEAD)
BUILDDIR ?= $(CURDIR)/build

# "clean"/"dirty", or "unknown" when git itself fails (e.g. a container build
# tripping git's dubious-ownership check). Branch on git's exit status rather
# than on empty output: `git status` printing nothing because it errored is
# indistinguishable from a clean tree, and defaulting that to "clean" would stamp
# a confident falsehood onto a dirty build. Scoped to the whole repo on purpose —
# dcctl compiles sibling workspace modules via go.work, so a change anywhere in
# the tree can change this binary.
GIT_TREE_STATE := $(shell out=$$(git status --porcelain 2>/dev/null) && { test -z "$$out" && echo clean || echo dirty; } || echo unknown)

BUILD_DATE := $(shell date -u +%Y-%m-%dT%H:%M:%SZ)

# The seed for this build's SELF-REPORTED version, and nothing else.
#
# 🔴 It is not a release version, and no release sets it. The release pipeline
# derives everything from the git TAG — `version="${GITHUB_REF_NAME}"` in
# .github/workflows/release.yml — and reads this file nowhere at all. That is why
# it has read 0.0.1 for the project's entire history, and why bumping it would
# not make a local build correct: it would only make a stale default look
# plausible. See the LDFLAGS note below.
REPO_ROOT := $(abspath $(CURDIR)/../..)
VERSION := $(shell cat $(REPO_ROOT)/VERSION 2>/dev/null || echo dev)

# Derived from BUILD_DATE, never a second `date` call: two invocations can
# straddle a second/day boundary and stamp a version whose timestamp contradicts
# the build date printed beside it — an incoherence that reads as a tooling bug
# in the one command whose entire job is to be trusted about build identity.
BUILD_STAMP := $(subst :,,$(subst -,,$(BUILD_DATE)))

# The VERSION file is a slow-moving constant: pre-GA it has read 0.0.1 for the
# project's entire history. Stamped bare, it makes every dev build ever produced
# self-report the same version, so two binaries months apart are indistinguishable
# (a stale dcctl on PATH then deploys against a chart that has moved underneath
# it, reporting a version that looks perfectly current). Every build this makefile
# produces therefore carries a UTC build stamp.
#
# There is no release branch here, and there was never a working one. It used to
# test `git describe --exact-match --match "v$(VERSION)"`, which asks for the tag
# v0.0.1 — a tag that has never existed — so the condition could not be true on
# any commit in the project's history. A branch that has never once been taken is
# not a fallback, it is a claim that make can produce a release build. It cannot:
# releases are cross-compiled by goreleaser, which stamps its own ldflags from the
# tag (backend/cli/.goreleaser.yaml).
BUILD_VERSION := $(VERSION)-dev.$(BUILD_STAMP)

# 🔴 DefaultImageVersion IS DELIBERATELY NOT STAMPED HERE, and that absence is
# the point of this block.
#
# It names a container image tag that must RESOLVE IN A REGISTRY. This makefile
# has no way to know one: the images a working tree corresponds to have not been
# published, because publishing is what a release does. Stamping the VERSION file
# sent every published-path `dcctl bootstrap` after ghcr.io/…/<area>:0.0.1, a tag
# that has never been pushed — ImagePullBackOff on every workload, several minutes
# into a bootstrap that looked healthy, from a value shaped exactly like a real
# release so IsUnpublishedImageVersion could not catch it.
#
# Left unstamped, the in-source default is "dev", which that guard DOES catch, so
# a locally built dcctl refuses immediately and names both ways forward:
#
#   this dcctl build has no pinned image version ("dev" is not a published tag);
#   deploy a tagged release with --version <tag>, or build from source with --build
#
# Bumping the VERSION file per release would NOT fix this and would make it worse:
# on main the value would then name the PREVIOUS release, whose images do exist —
# so bootstrap would silently pair old binaries with this tree's chart, whose
# instance config they have never seen and their typed config rejects. A refusal
# you can read beats a crash-loop you have to diagnose.
#
# A released binary is unaffected: goreleaser stamps DefaultImageVersion from the
# git tag, which is exactly the value that does resolve.
LDFLAGS := -X github.com/devicechain-io/dcctl/cmd.gitCommit=$(GIT_COMMIT) \
           -X github.com/devicechain-io/dcctl/cmd.gitTreeState=$(GIT_TREE_STATE) \
           -X github.com/devicechain-io/dcctl/cmd.buildDate=$(BUILD_DATE) \
           -X github.com/devicechain-io/dcctl/cmd.Version=$(BUILD_VERSION)

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
