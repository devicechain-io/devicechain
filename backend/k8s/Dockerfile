# Build the manager binary.
#
# 🔴 THIS FILE DOES NOT BUILD THE SHIPPED OPERATOR IMAGE. It is kubebuilder
# scaffolding, reachable only through this module's `make docker-build`, which
# nothing in CI or the release workflow invokes. The operator that actually
# ships is built by **ko**, alongside every service image, from the repo-root
# `.ko.yaml` — so its base is `defaultBaseImage` there, NOT the distroless
# reference below. The two disagree today, which is the trap this comment
# exists to close: debugging the shipped image by reading this file leads to
# the wrong base entirely.
#
# Base images are pinned by digest as well as tag: a tag is mutable, so an
# unpinned rebuild of an unchanged commit can pick up a different base. The
# digests below are the multi-arch INDEX digests, so pinning them still lets the
# builder select the right platform. Dependabot's `docker` ecosystem covers this
# directory and rolls both the tag and the digest forward weekly — without that,
# a digest pin would freeze the base at whatever was current when it was written,
# which is worse for security than a floating tag.
# 🔴 THIS PIN BUILDS NOTHING THAT SHIPS, INCLUDING THE OPERATOR'S GO TOOLCHAIN.
# It reads like the security-relevant pin and is not one, which is worth stating
# twice because the consequence is a wrong conclusion during advisory triage.
#
# ko does not use this Dockerfile. It compiles the operator with whatever
# toolchain the WORKFLOW installed, and layers that binary onto `.ko.yaml`'s
# base. The workflow that publishes is release.yml's `images` job, and its
# `setup-go` reads `go-version-file: go.work`.
# So go.work IS the toolchain the shipped operator is built with, and
# govulncheck in CI, which reads that same toolchain, is reporting on the binary
# we actually publish. Bumping go.work is the whole fix, not half of one.
#
# Keep this pin rolling anyway: `make docker-build` in this module is a real
# local path, and Dependabot's `docker` ecosystem covers this directory. It is
# hygiene for a developer convenience, not a supply-chain control.
FROM golang:1.27@sha256:4013ae0f9e7994f8535c58c811f8f863fbed38b72e0d51e6592156f758d66146 AS builder

WORKDIR /workspace
# Copy the Go Modules manifests
COPY go.mod go.mod
COPY go.sum go.sum
# cache deps before building and copying source so that we don't need to re-download as much
# and so that source changes don't invalidate our downloaded layer
RUN go mod download

# Copy the go source
COPY main.go main.go
COPY api/ api/
COPY controllers/ controllers/

# Build
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -a -o manager main.go

# Use distroless as minimal base image to package the manager binary
# Refer to https://github.com/GoogleContainerTools/distroless for more details
FROM gcr.io/distroless/static:nonroot@sha256:1c2c046bc09ed40fad370b599a0b1ae7987f55b01e247cf27a7c27cd97e5bbc7
WORKDIR /
COPY --from=builder /workspace/manager .
USER 65532:65532

ENTRYPOINT ["/manager"]
