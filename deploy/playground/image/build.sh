#!/usr/bin/env bash
# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
#
# Build the playground driver image for a released DeviceChain version.
#
#   deploy/playground/image/build.sh <version> <image-ref> [--push]
#
# dcctl and dc-simulator come from the release of <version> (verify them as the
# release notes say before you ship the image); the playground binary is built from this
# checkout. Needs docker, gh and go.
set -euo pipefail

if [ $# -lt 2 ]; then
	echo "usage: $0 <version> <image-ref> [--push]" >&2
	exit 2
fi
version="${1#v}"
image="$2"
push="${3:-}"

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
repo_root="$(cd "$here/../../.." && pwd)"
ctx="$(mktemp -d)"
trap 'rm -rf "$ctx"' EXIT

gh release download "v${version}" -R devicechain-io/devicechain \
	-p "dcctl_${version}_linux_amd64.tar.gz" -p "dc-simulator_${version}_linux_amd64.tar.gz" -D "$ctx/dl"
tar -xzf "$ctx/dl/dcctl_${version}_linux_amd64.tar.gz" -C "$ctx" dcctl
tar -xzf "$ctx/dl/dc-simulator_${version}_linux_amd64.tar.gz" -C "$ctx" dc-simulator
rm -rf "$ctx/dl"

(cd "$repo_root/backend/tools/playground" && CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -o "$ctx/playground" .)
cp "$here/Dockerfile" "$ctx/Dockerfile"

docker build -t "$image" "$ctx"
if [ "$push" = "--push" ]; then
	docker push "$image"
fi
