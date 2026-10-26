#!/usr/bin/env bash
# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
#
# Proves the base image `.ko.yaml` pins is the base ko ACTUALLY BUILDS ON.
#
# hack/check-ko-base-pin.sh guards the VALUE of `defaultBaseImage` (digest-pinned, bumper
# alive). It cannot know whether any build reads it, and for a long time none of the
# release or CI builds did: ko looks for `.ko.yaml` in its WORKING DIRECTORY only, every
# one of those builds ran from a module directory, and none set KO_CONFIG_PATH. They built
# on whatever the base's tag resolved to that day, while the guard kept reporting a
# healthy pin. A guard on a value the build ignores is a gate that cannot fail.
#
# Two checks, because there are two ways to lose this:
#
#   (default)  BUILD one image the way the release builds it and read the base ko
#              recorded: ko writes `org.opencontainers.image.base.name` into the image
#              manifest, and for a pinned base that is `<repo>@sha256:<pin>`. An unpinned
#              build records the bare tag instead. The environment is the caller's: CI
#              sets KO_CONFIG_PATH exactly as release.yml does. Run it with the variable
#              unset to see it fail (that is the negative control).
#   --lint     STATIC, no network: every file that runs `ko build` must set
#              KO_CONFIG_PATH. This is what catches the next invocation someone adds that
#              the one-image build above does not exercise.
#
# Needs for the default mode: ko, network access to the base's registry.

set -euo pipefail

cd "$(git rev-parse --show-toplevel)"

# shellcheck source=lib/ko-base-image.sh
. hack/lib/ko-base-image.sh

# Files that may invoke ko: workflows, scripts, and dcctl's builder.
lint_files() {
  find .github/workflows hack deploy/local backend/cli/bootstrap -maxdepth 1 -type f \
    \( -name '*.yml' -o -name '*.sh' -o -name '*.go' \) ! -name '*_test.go' \
    ! -name 'check-ko-base-used.sh' | sort
}

# non_comment FILE — the file's lines minus whole-line comments.
non_comment() { grep -Ev '^[[:space:]]*(#|//)' "$1" || true; }

lint() {
  local f bad=0 n=0
  while IFS= read -r f; do
    # A ko invocation: `ko build` in shell/YAML, or "ko", "build" as exec arguments in Go.
    if non_comment "$f" | grep -Eq '(^|[^[:alnum:]_./-])ko build\b|"ko",[[:space:]]*"build"'; then
      n=$((n + 1))
      if non_comment "$f" | grep -q 'KO_CONFIG_PATH'; then
        echo "  ok: $f runs ko with KO_CONFIG_PATH"
      else
        echo "::error file=$f::runs \`ko build\` without KO_CONFIG_PATH, so .ko.yaml's pinned base is never read"
        bad=1
      fi
    fi
  done < <(lint_files)
  # Zero invocations found means the scan is broken, not that the repo is clean.
  if [ "$n" -lt 4 ]; then
    echo "::error::lint found only $n ko invocations (expected the release, CI, bumper and local build scripts at least); the scan is broken"
    return 1
  fi
  return "$bad"
}

build_and_check() {
  command -v ko >/dev/null 2>&1 || { echo "::error::ko not found"; return 1; }
  local pin want svc dir out manifest got
  pin="$(ko_base_image_from "$KO_BASE_FILE_REL")"
  [ -n "$pin" ] || { echo "::error::no defaultBaseImage in $KO_BASE_FILE_REL"; return 1; }
  # <repo>:<tag>@sha256:<d> -> <repo>@sha256:<d>, the form ko records.
  want="$(sed -E 's/:[^:/@]+@/@/' <<<"$pin")"

  # KO_CHECK_MODULE picks the module (CI passes one whose dependencies the job already
  # downloaded); by default the first service.
  svc="${KO_CHECK_MODULE:-}"
  if [ -z "$svc" ]; then
    svc="$(find backend/services -mindepth 2 -maxdepth 2 -name main.go | sort | head -n1)"
    [ -n "$svc" ] || { echo "::error::no service found to build"; return 1; }
    svc="$(dirname "$svc")"
  fi
  # ko derives a repository name from the layout path and rejects uppercase and drive
  # letters, so mktemp's mixed-case absolute path fails: use a plain lowercase name
  # relative to the module directory it builds in, and remove it afterwards.
  layout="ko-base-check-layout"
  out="$svc/$layout"
  rm -rf "$out"
  trap 'rm -rf "$out"' RETURN

  echo "building $svc with KO_CONFIG_PATH='${KO_CONFIG_PATH:-}'; expecting base $want"
  # Same shape as release.yml's step (module dir as cwd, --bare, ./), minus the push.
  ( cd "$svc" && KO_DOCKER_REPO=ghcr.io/devicechain-io/base-check \
      ko build --push=false --bare --platform linux/amd64 --oci-layout-path "$layout" ./ >/dev/null )

  manifest="$(sed -n 's/.*"digest": *"sha256:\([0-9a-f]\{64\}\)".*/\1/p' "$out/index.json" | head -n1)"
  [ -n "$manifest" ] && [ -f "$out/blobs/sha256/$manifest" ] \
    || { echo "::error::could not read the built manifest from the OCI layout"; return 1; }
  got="$(grep -o '"org.opencontainers.image.base.name":"[^"]*"' "$out/blobs/sha256/$manifest" \
          | head -n1 | sed 's/.*:"\(.*\)"/\1/')"
  if [ "$got" != "$want" ]; then
    echo "::error::the image was built on '${got:-<no base annotation>}', not the pinned base '$want'."
    echo "  ko only reads .ko.yaml from its working directory; set KO_CONFIG_PATH to the repo's .ko.yaml."
    return 1
  fi
  echo "  ok: built image records base.name = $got"
}

case "${1:-}" in
  --lint) lint ;;
  "")     build_and_check ;;
  *) echo "usage: $0 [--lint]" >&2; exit 2 ;;
esac
