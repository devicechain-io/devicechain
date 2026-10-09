#!/usr/bin/env bash
# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
#
# docker-pull.sh [--platform P] <image@sha256:digest>
#
# Pull a DIGEST-PINNED image, retrying Docker Hub and then falling back to a mirror.
#
# WHY
#
# Docker Hub's auth endpoint times out for hours at a time, and every CI job that pulls
# a Hub image (the integration/migrations database, the promtool image, the registry
# the upgrade rig runs) then fails on something that has nothing to do with the tree.
# Retrying one host is not resilience (see hack/go-mod-download.sh, the ko install).
#
# WHY A MIRROR DOES NOT WEAKEN INTEGRITY
#
# Every reference is pinned by `@sha256:`. A pull by digest is verified by the client
# against that digest no matter which registry served the bytes, so mirror.gcr.io (Google's
# Docker Hub cache) or public.ecr.aws/docker/library (AWS's copy of the official images)
# can only hand back the content the pin names, or fail. That is also why a reference with
# no digest is REFUSED: for a tag the mirror would be trusted for content, and a mirror
# may lag the tag. There is no tag fallback.
#
# CONTRACT
#
#   stdout: exactly one line, the reference to USE (`docker run` it). When Docker Hub
#           served the pull that is the reference you passed. When a mirror did, it is a
#           local TAG on the mirrored image: `docker tag` cannot create a name@digest, and
#           the classic image store does not match a pulled mirror digest to the original
#           repository, so `docker run <original>` would go back to the dead registry.
#           Use the printed value:   IMAGE="$(hack/docker-pull.sh "$IMAGE")"
#   stderr: progress, with a ::warning:: when a mirror was needed.
#   exit:   0 pulled; 1 every source failed (says so, and that nothing was verified);
#           2 refused (bad usage, or no digest).
#
# Only Docker Hub references have a mirror. A reference naming another registry
# (ghcr.io/..., quay.io/..., localhost:5000/...) gets the retry only.
#
# Knobs (tests use them; CI leaves the defaults): DC_PULL_ATTEMPTS (3),
# DC_PULL_TIMEOUT seconds per Docker Hub attempt (90; a hung auth endpoint must fail fast, a
# slow-but-moving pull of a few hundred MB must not be killed), DC_PULL_MIRROR_TIMEOUT (600), DC_PULL_BACKOFF seconds (10, multiplied by the attempt number), DC_PULL_MIRRORS
# (space-separated mirror prefixes tried in order; default `mirror.gcr.io`, plus
# `public.ecr.aws/docker/library` for official `library/` images).
set -euo pipefail

# --self-test: drive this script against a stub `docker` on PATH. Run on every pull request;
# it needs no network.
if [ "${1:-}" = "--self-test" ]; then
  self="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/$(basename "${BASH_SOURCE[0]}")"
  tmp="$(mktemp -d)"
  trap 'rm -rf "$tmp"' EXIT
  mkdir "$tmp/bin"
  # The stub records every call. HUB_FAIL / MIRROR_FAIL pick which registries refuse a pull.
  cat >"$tmp/bin/docker" <<'STUB'
#!/usr/bin/env bash
echo "$*" >>"$STUB_LOG"
case "$1" in
  pull)
    ref="${@: -1}"
    case "$ref" in
      mirror.gcr.io/*) [ "${MIRROR_FAIL:-0}" != 0 ] && exit 1 ;;
      public.ecr.aws/*) [ "${MIRROR_FAIL:-0}" = 1 ] && exit 1 ;;
      *) [ "${HUB_HANG:-0}" = 1 ] && sleep 30
         [ "${HUB_FAIL:-0}" = 1 ] && exit 1 ;;
    esac
    exit 0 ;;
  tag) exit 0 ;;
esac
exit 0
STUB
  chmod +x "$tmp/bin/docker"
  export STUB_LOG="$tmp/log" DC_PULL_BACKOFF=0 PATH="$tmp/bin:$PATH"
  d="sha256:$(printf 'ab%.0s' {1..32})"
  want() { # want <name> <expected rc> <expected stdout> <env...> -- <args...>
    local name="$1" erc="$2" eout="$3" out rc; shift 3
    : >"$STUB_LOG"
    local envs=(DC_SELFTEST=1)
    while [ "$1" != "--" ]; do envs+=("$1"); shift; done; shift
    set +e; out="$(env "${envs[@]}" "$self" "$@" 2>"$tmp/err")"; rc=$?; set -e
    if [ "$rc" != "$erc" ] || [ "$out" != "$eout" ]; then
      echo "self-test FAIL ($name): rc=$rc (want $erc) stdout='$out' (want '$eout')" >&2
      cat "$tmp/err" >&2; exit 1
    fi
    echo "self-test ok: $name" >&2
  }
  logged() { grep -q -- "$1" "$STUB_LOG" || { echo "self-test FAIL: stub never saw '$1'" >&2; exit 1; }; }
  unlogged() { ! grep -q -- "$1" "$STUB_LOG" || { echo "self-test FAIL: stub saw '$1'" >&2; exit 1; }; }

  want hub-ok 0 "timescale/timescaledb:pg16@$d" -- "timescale/timescaledb:pg16@$d"
  unlogged mirror.gcr.io
  want hub-down-mirror-ok 0 "timescale/timescaledb:pg16" HUB_FAIL=1 -- "timescale/timescaledb:pg16@$d"
  logged "pull --quiet mirror.gcr.io/timescale/timescaledb@$d"
  logged "tag mirror.gcr.io/timescale/timescaledb@$d timescale/timescaledb:pg16"
  [ "$(grep -c 'pull --quiet timescale/timescaledb' "$STUB_LOG")" = 3 ] ||
    { echo "self-test FAIL: Docker Hub was not retried 3 times" >&2; exit 1; }
  want untagged-ref-gets-a-tag 0 "prom/prometheus:dc-mirror-abababababab" HUB_FAIL=1 -- "prom/prometheus@$d"
  want official-image 0 "registry:2.8.3" HUB_FAIL=1 -- "registry:2.8.3@$d"
  logged "pull --quiet mirror.gcr.io/library/registry@$d"
  want official-image-second-mirror 0 "registry:2.8.3" HUB_FAIL=1 MIRROR_FAIL=gcr -- "registry:2.8.3@$d"
  logged "pull --quiet public.ecr.aws/docker/library/registry@$d"
  want non-official-has-no-ecr 1 "" HUB_FAIL=1 MIRROR_FAIL=gcr -- "x/y:1@$d"
  unlogged public.ecr.aws
  want platform-passed 0 "x/y:1" HUB_FAIL=1 -- --platform linux/amd64 "x/y:1@$d"
  logged "pull --quiet --platform linux/amd64 mirror.gcr.io/x/y@$d"
  want hub-hangs-mirror-runs 0 "x/y:1" HUB_HANG=1 DC_PULL_TIMEOUT=1 DC_PULL_ATTEMPTS=2 -- "x/y:1@$d"
  logged "pull --quiet mirror.gcr.io/x/y@$d"
  want all-down 1 "" HUB_FAIL=1 MIRROR_FAIL=1 -- "x/y:1@$d"
  grep -q "NOTHING WAS VERIFIED" "$tmp/err" || { echo "self-test FAIL: failure message unclear" >&2; exit 1; }
  unlogged "^tag "
  want other-registry-no-mirror 1 "" HUB_FAIL=1 -- "ghcr.io/x/y:1@$d"
  unlogged mirror.gcr.io
  want no-digest 2 "" -- "timescale/timescaledb:latest-pg16"
  [ ! -s "$STUB_LOG" ] || { echo "self-test FAIL: a no-digest ref reached docker" >&2; exit 1; }
  want short-digest 2 "" -- "x/y@sha256:abc"
  want no-args 2 "" -- 
  echo "docker-pull self-test: all verdicts reached" >&2
  exit 0
fi

log() { echo "docker-pull: $*" >&2; }
refuse() { echo "docker-pull: REFUSED: $*" >&2; exit 2; }

platform=()
while [ $# -gt 0 ]; do
  case "$1" in
    --platform) [ $# -ge 2 ] || refuse "--platform needs a value"; platform=(--platform "$2"); shift 2 ;;
    --) shift; break ;;
    -*) refuse "unknown option $1" ;;
    *) break ;;
  esac
done
[ $# -eq 1 ] || refuse "usage: docker-pull.sh [--platform P] <image@sha256:digest>"
ref="$1"
[[ "$ref" =~ ^([^@[:space:]]+)@(sha256:[0-9a-f]{64})$ ]] ||
  refuse "'$ref' is not pinned by digest (name[:tag]@sha256:<64 hex>); a tag is not pulled through a mirror"
repo_tag="${BASH_REMATCH[1]}"
digest="${BASH_REMATCH[2]}"

attempts="${DC_PULL_ATTEMPTS:-3}"
backoff="${DC_PULL_BACKOFF:-10}"

# pull_ref <ref> <seconds per attempt>: up to $attempts tries with linear backoff.
pull_ref() {
  local r="$1" limit="$2" a
  for ((a = 1; a <= attempts; a++)); do
    # A per-attempt timeout: a hung Hub auth endpoint must fail fast so the mirror actually runs.
    if timeout "$limit" docker pull --quiet "${platform[@]}" "$r" >/dev/null 2>&1; then return 0; fi
    log "pull of $r failed (attempt $a/$attempts)"
    [ "$a" -lt "$attempts" ] && sleep $((a * backoff))
  done
  return 1
}

if pull_ref "$ref" "${DC_PULL_TIMEOUT:-90}"; then
  echo "$ref"
  exit 0
fi

# Split name[:tag]. A ':' after the last '/' is a tag; one before it is a registry port.
name="$repo_tag"
tag=""
last="${repo_tag##*/}"
if [[ "$last" == *:* ]]; then
  tag="${last#*:}"
  name="${repo_tag%:*}"
fi

# Only Docker Hub has these mirrors. First path component with a '.' or ':' or
# 'localhost' is a registry host; docker.io / index.docker.io are Hub spelled out.
first="${name%%/*}"
case "$first" in
  docker.io | index.docker.io | registry-1.docker.io) name="${name#*/}" ;;
  *)
    if [[ "$name" == */* ]] && [[ "$first" == *.* || "$first" == *:* || "$first" == localhost ]]; then
      log "$ref is not a Docker Hub reference; it has no mirror"
      echo "docker-pull: FAILED: could not pull $ref. NOTHING WAS VERIFIED OR PULLED." >&2
      exit 1
    fi
    ;;
esac
[[ "$name" == */* ]] || name="library/$name"

mirrors="${DC_PULL_MIRRORS:-mirror.gcr.io}"
if [ -z "${DC_PULL_MIRRORS:-}" ] && [[ "$name" == library/* ]]; then
  mirrors="$mirrors public.ecr.aws/docker/$(dirname "$name")"
fi

read -ra mirror_list <<<"$mirrors"
for m in "${mirror_list[@]}"; do
  mref="$m/${name}@${digest}"
  # public.ecr.aws/docker/library is already the library namespace.
  [[ "$m" == */library && "$name" == library/* ]] && mref="$m/${name#library/}@${digest}"
  log "Docker Hub failed for $ref; trying $mref"
  # Two attempts: the primary already burned the full budget, a mirror is a fallback.
  saved="$attempts"; attempts=2
  if pull_ref "$mref" "${DC_PULL_MIRROR_TIMEOUT:-600}"; then
    attempts="$saved"
    local_tag="${tag:-dc-mirror-${digest:7:12}}"
    local_ref="${name#library/}:${local_tag}"
    docker tag "$mref" "$local_ref"
    echo "::warning::docker pull of $ref failed; served by mirror $m (the digest is verified by the client, so the content is identical)" >&2
    log "pulled $ref via $m; use $local_ref"
    echo "$local_ref"
    exit 0
  fi
  attempts="$saved"
done

echo "docker-pull: FAILED: could not pull $ref from Docker Hub or any mirror ($mirrors). NOTHING WAS VERIFIED; this is a failure to obtain the image, not a verdict about the tree." >&2
exit 1
