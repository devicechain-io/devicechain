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
FROM golang:1.26@sha256:2005724102f45917a63e9d092fc0e4ea56ea575048ce147caad5f5f61502c365 AS builder

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
FROM gcr.io/distroless/static:nonroot@sha256:f7f8f729987ad0fdf6b05eeeae94b26e6a0f613bdf46feea7fc40f7bd72953e6
WORKDIR /
COPY --from=builder /workspace/manager .
USER 65532:65532

ENTRYPOINT ["/manager"]
