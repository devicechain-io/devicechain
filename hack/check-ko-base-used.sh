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
#              build records the bare tag instead. It builds through hack/ko-build.sh,
#              the wrapper every release/CI/local build uses; KO_BUILD_WRAPPER points it at
#              something else (a bare-ko stub is the negative control).
#   --lint     STATIC, no network: no bare ko build/publish/resolve/apply/run outside the
#              wrapper (dcctl's Go must set a non-empty KO_CONFIG_PATH), and an exact count
#              of wrapper callers. Catches the next invocation someone adds that the
#              one-image build above does not exercise.
#   --self-test  plants each defect in a throwaway tree; --lint must refuse every one.
#
# Needs for the default mode: ko, network access to the base's registry.

set -euo pipefail

cd "$(git rev-parse --show-toplevel)"

# shellcheck source=lib/ko-base-image.sh
. hack/lib/ko-base-image.sh

# ---------------------------------------------------------------------------
# LINT. Every ko invocation goes through hack/ko-build.sh, which sets KO_CONFIG_PATH
# unconditionally. So the rule is not "each file sets the variable" (a per-file test is
# satisfied by ANY step in the file that does) but "no bare ko build/publish/resolve/
# apply/run anywhere outside the wrapper", plus an exact count of wrapper callers so a
# scan that finds nothing cannot pass.
#
# The one other place ko is run is dcctl (backend/cli/bootstrap/steps.go), in Go; the
# lint checks that it sets a NON-EMPTY KO_CONFIG_PATH.
# ---------------------------------------------------------------------------
# Callers of the wrapper, counted by file: release.yml, ci.yml, ko-base-image.yml,
# deploy/local/build-images.sh, deploy/local/bounce.sh. Adding a caller means raising this.
EXPECTED_WRAPPER_CALLERS=5

# logical_lines FILE — whole-line comments dropped, backslash continuations joined, so a
# `ko \` / `build` split across lines is one line to the matcher.
logical_lines() {
  awk '/^[[:space:]]*(#|\/\/)/ { next }
       { line = $0
         if (line ~ /\\[[:space:]]*$/) { sub(/\\[[:space:]]*$/, "", line); buf = buf line " "; next }
         print buf line; buf = "" }
       END { if (buf != "") print buf }' "$1"
}

BARE_KO='(^|[;&|(`!]|[[:space:]](then|do|else|if|exec)|\$\(|run:)[[:space:]]*(env[[:space:]]+)?([A-Za-z_]+=[^[:space:]]*[[:space:]]+)*ko[[:space:]]+(build|publish|resolve|apply|run)([[:space:]]|$)|"ko",[[:space:]]*"(build|publish|resolve|apply|run)"'
WRAPPER_CALL='(^|[^[:alnum:]_.-])(hack/)?ko-build\.sh([[:space:]"]|$)'

# lint_tree ROOT EXPECTED — scan ROOT's workflows, scripts and dcctl.
lint_tree() {
  local root="$1" expected="$2" f rel bad=0 callers=0 text
  while IFS= read -r f; do
    rel="${f#"$root"/}"
    case "$rel" in
      hack/ko-build.sh | hack/check-ko-base-used.sh | backend/cli/bootstrap/steps.go) continue ;;
    esac
    text="$(logical_lines "$f")"
    if grep -Eq "$BARE_KO" <<<"$text"; then
      echo "::error file=$rel::runs ko without hack/ko-build.sh, so .ko.yaml's pinned base is never read"
      bad=1
    fi
    if grep -Eq "$WRAPPER_CALL" <<<"$text"; then
      callers=$((callers + 1))
    fi
  done < <(find "$root/.github" "$root/hack" "$root/deploy" "$root/backend/cli"              -type f \( -name '*.yml' -o -name '*.yaml' -o -name '*.sh' -o -name '*.go' -o -name 'Makefile' -o -name '*.mk' \)              ! -name '*_test.go' ! -path '*/node_modules/*' 2>/dev/null | sort)

  # dcctl runs ko from Go; it must pass a non-empty KO_CONFIG_PATH.
  if [ -f "$root/backend/cli/bootstrap/steps.go" ]; then
    text="$(logical_lines "$root/backend/cli/bootstrap/steps.go")"
    if ! grep -Eq '"KO_CONFIG_PATH="[[:space:]]*\+[[:space:]]*[A-Za-z.]+\([^)]*"\.ko\.yaml"' <<<"$text"; then
      echo "::error file=backend/cli/bootstrap/steps.go::dcctl's ko build does not set KO_CONFIG_PATH to a .ko.yaml path"
      bad=1
    fi
  else
    echo "::error::backend/cli/bootstrap/steps.go not found"; bad=1
  fi

  if [ "$callers" -ne "$expected" ]; then
    echo "::error::found $callers files calling hack/ko-build.sh, expected exactly $expected (a new caller raises EXPECTED_WRAPPER_CALLERS; fewer means the scan is broken or a build stopped using the wrapper)"
    bad=1
  fi
  return "$bad"
}

lint() {
  lint_tree "$PWD" "$EXPECTED_WRAPPER_CALLERS" && echo "  ok: no bare ko; $EXPECTED_WRAPPER_CALLERS files call hack/ko-build.sh; dcctl sets KO_CONFIG_PATH"
}

# self_test: plant each defect in a throwaway tree and require the lint to refuse it.
self_test() {
  local t rc=0
  t="$(mktemp -d)"
  trap 'rm -rf "$t"' RETURN
  mk_tree() {
    rm -rf "${t:?}/tree"
    mkdir -p "$t/tree/.github/workflows" "$t/tree/hack" "$t/tree/deploy" "$t/tree/backend/cli/bootstrap"
    printf '%s\n' 'jobs:' '  a:' '    steps:' '      - run: |' \
      '          hack/ko-build.sh --local ./' \
      '          echo "ko build is fine in prose"' > "$t/tree/.github/workflows/w.yml"
    printf '%s\n' 'cmd.Env = append(env, "KO_CONFIG_PATH="+filepath.Join(root, ".ko.yaml"))' \
      'exec.Command("ko", "build")' > "$t/tree/backend/cli/bootstrap/steps.go"
  }
  plant() { printf '%s\n' "$2" >> "$t/tree/$1"; } # <file> <line>
  expect() { # <name> <want: pass|fail>
    local got=pass
    lint_tree "$t/tree" 1 >/dev/null 2>&1 || got=fail
    if [ "$got" != "$2" ]; then
      echo "  SELF-TEST FAILED: $1 (wanted $2, got $got)"; rc=1
    else
      echo "  ok: $1 -> $got"
    fi
  }
  mk_tree; expect "clean tree" pass
  mk_tree; plant .github/workflows/w.yml '      - run: ko build --local ./'; expect "bare ko in a step" fail
  mk_tree; plant .github/workflows/w.yml '          ko \'; plant .github/workflows/w.yml '            build --local ./'
  expect "continued line" fail
  mk_tree; plant .github/workflows/w.yml '          ko publish ./'; expect "ko publish" fail
  mk_tree; plant hack/x.sh 'KO_DOCKER_REPO=x ko resolve -f a.yaml'; expect "env-prefixed ko resolve in a script" fail
  mk_tree; plant deploy/y.sh 'x=$(ko build ./)'; expect "ko in a command substitution" fail
  mk_tree; plant backend/cli/z.go 'exec.Command("ko", "build")'; expect "bare ko in other Go" fail
  mk_tree; printf '%s\n' 'cmd.Env = append(env, "KO_CONFIG_PATH=")' > "$t/tree/backend/cli/bootstrap/steps.go"
  expect "dcctl with an empty KO_CONFIG_PATH" fail
  mk_tree; rm "$t/tree/.github/workflows/w.yml"; expect "no wrapper callers (broken scan)" fail
  return "$rc"
}

build_and_check() {
  command -v ko >/dev/null 2>&1 || { echo "::error::ko not found"; return 1; }
  local pin want svc out manifest got layout wrapper
  pin="$(ko_base_image_from "$KO_BASE_FILE_REL")"
  [ -n "$pin" ] || { echo "::error::no defaultBaseImage in $KO_BASE_FILE_REL"; return 1; }
  # <repo>:<tag>@sha256:<d> -> <repo>@sha256:<d>, the form ko records.
  want="$(sed -E 's/:[^:/@]+@/@/' <<<"$pin")"

  # KO_CHECK_MODULE picks the module (CI passes one whose dependencies the job already
  # downloaded); by default the first service.
  svc="${KO_CHECK_MODULE:-}"
  if [ -z "$svc" ]; then
    svc="$(find backend/services -mindepth 2 -maxdepth 2 -name main.go | sort)"
    [ -n "$svc" ] || { echo "::error::no service found to build"; return 1; }
    svc="$(dirname "$(sed -n 1p <<<"$svc")")"
  fi
  # ko derives a repository name from the layout path and rejects uppercase and drive
  # letters, so mktemp's mixed-case absolute path fails: use a plain lowercase name
  # relative to the module directory it builds in, and remove it afterwards.
  layout="ko-base-check-layout"
  out="$svc/$layout"
  rm -rf "$out"
  trap 'rm -rf "$out"' RETURN

  echo "building $svc; expecting base $want"
  # Same shape as release.yml's step (module dir as cwd, the wrapper, --bare, ./), minus
  # the push. KO_BUILD_WRAPPER exists for the negative control: a bare-ko stub must fail.
  wrapper="${KO_BUILD_WRAPPER:-$PWD/hack/ko-build.sh}"
  ( cd "$svc" && KO_DOCKER_REPO=ghcr.io/devicechain-io/base-check \
      "$wrapper" --push=false --bare --platform linux/amd64 --oci-layout-path "$layout" ./ >/dev/null )

  manifest="$(sed -n 's/.*"digest": *"sha256:\([0-9a-f]\{64\}\)".*/\1/p' "$out/index.json")"
  manifest="$(sed -n 1p <<<"$manifest")"
  [ -n "$manifest" ] && [ -f "$out/blobs/sha256/$manifest" ] \
    || { echo "::error::could not read the built manifest from the OCI layout"; return 1; }
  got="$(grep -o '"org.opencontainers.image.base.name":"[^"]*"' "$out/blobs/sha256/$manifest" || true)"
  got="$(sed -n '1s/.*:"\(.*\)"/\1/p' <<<"$got")"
  if [ "$got" != "$want" ]; then
    echo "::error::the image was built on '${got:-<no base annotation>}', not the pinned base '$want'."
    echo "  ko only reads .ko.yaml from its working directory; build through hack/ko-build.sh, which sets KO_CONFIG_PATH."
    return 1
  fi
  echo "  ok: built image records base.name = $got"
}

case "${1:-}" in
  --lint) lint ;;
  --self-test) self_test ;;
  "")     build_and_check ;;
  *) echo "usage: $0 [--lint|--self-test]" >&2; exit 2 ;;
esac
