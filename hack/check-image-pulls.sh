#!/usr/bin/env bash
# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
#
# Every image this repository pins BY DIGEST must still be pullable ANONYMOUSLY.
#
# WHY
#
# A digest makes a pull reproducible; it does not make a registry keep serving it.
# The object store every default install runs was pinned to a MinIO release on
# quay.io, and when community MinIO's images were withdrawn, quay and Docker Hub
# both began answering an anonymous pull with 401 — by tag AND by digest. Every
# node that had the image cached kept running, so nothing looked wrong; every
# FRESH `dcctl install` stopped with the store in ImagePullBackOff. No CI job saw
# it, because every CI install is `--compact`, and `--compact` without TLS drops
# the object store entirely: nothing in the repository ever pulled that image.
#
# So this asks the registries directly, as a stranger would: the registry API's
# own anonymous handshake (HEAD the manifest; on a 401, take the anonymous bearer
# token the challenge offers; HEAD again), which is the first thing a kubelet's
# pull does, and where a withdrawn or private image is refused.
#
# 🔴 WITH NO CREDENTIALS, and that is load-bearing. curl is run with `-q`, so no
# ~/.curlrc can add a header or a user, and never with -n/-u, so no .netrc is
# read. A logged-in developer or a runner with registry credentials would
# otherwise be served through them and miss precisely the 401 this exists to see.
#
# 🔴 BY DIGEST ALONE, never `name:tag@digest`. Measured: `docker manifest inspect`
# given both resolves the TAG and then checks it against the digest, so it fails
# ("manifest verification failed for digest") the moment upstream moves the tag,
# while the digest itself still pulls. A kubelet pulls the digest, so that is
# what is asked.
#
# 🔴 HEAD, NOT GET. Docker Hub counts manifest GETs against the anonymous pull
# limit and does not count HEADs. An earlier form of this check spent that limit
# on its own probes and then reported the Hub images UNPULLABLE with
# "toomanyrequests" — a failure it had caused.
#
# WHERE IT RUNS, AND WHY NOT ON PULL REQUESTS
#
# From .github/workflows/ko-base-image.yml, weekly, BEFORE that run records its
# success. That success is the heartbeat hack/check-ko-base-pin.sh reads on every
# pull request, so a withdrawn image stops the heartbeat and reaches pull requests
# through a check that is already there — without putting someone else's registry
# in front of every pull request's green (hack/check-image-pins.sh says why that
# is the failure to avoid). The offline half of the self-test (`--self-test-offline`)
# does run on every pull request: it needs no network, and it is what proves each
# verdict below can be reached.
#
# WHAT IT COVERS
#
#   Every `image:tag@sha256:<64 hex>` literal in tracked files — Dockerfiles,
#   scripts, .ko.yaml, Go constants, Tofu defaults — found by pattern rather than
#   from a list, so a new pin is covered the day it lands. Excluded: _legacy/
#   (archived, not built), docs/ (prose quotes references, it does not pull them)
#   and *_test.go (fixtures).
#
# WHAT IT DOES NOT COVER
#
#   - Images reached through Helm charts at pinned chart versions (cert-manager,
#     CloudNativePG and its Barman plugin, ingress-nginx, NATS, the Prometheus
#     stack), and tag-pinned operand defaults (the Postgres and TimescaleDB images
#     in the Tofu roots). Listing those needs helm and the chart repositories; they
#     were pulled by hand when this was written, and all resolved.
#   - Layers. The manifest is where a registry refuses a withdrawn or private
#     image; blobs are not fetched.
#   - Whether the digest is CURRENT. That is the bumper's job, not this one's.
#
# Usage:
#   hack/check-image-pulls.sh                     # probe every pinned image
#   hack/check-image-pulls.sh --self-test         # every control, incl. the network ones
#   hack/check-image-pulls.sh --self-test-offline # the controls that need no network
#
# IMAGE_PULL_TIMEOUT (seconds, default 30) bounds each request. A probe that runs
# out is reported TIMED OUT — its own verdict, never a pass. IMAGE_PULL_ATTEMPTS
# (default 3) and IMAGE_PULL_RETRY_DELAY (seconds, default 5) set the retries.
#
# Requires: git, curl, jq, timeout.

set -euo pipefail

cd "$(git rev-parse --show-toplevel)"

# shellcheck source=lib/object-store-image.sh
. hack/lib/object-store-image.sh

IMAGE_PULL_TIMEOUT="${IMAGE_PULL_TIMEOUT:-30}"
IMAGE_PULL_ATTEMPTS="${IMAGE_PULL_ATTEMPTS:-3}"
IMAGE_PULL_RETRY_DELAY="${IMAGE_PULL_RETRY_DELAY:-5}"

# image:tag@sha256:<64 lowercase hex>, unanchored — it is searched for in text.
REF_RE='[a-z0-9][a-zA-Z0-9._/-]*:[A-Za-z0-9][A-Za-z0-9._-]*@sha256:[0-9a-f]{64}'

# Every manifest type a pin can name: a multi-arch pin is an index, and a
# registry that is not told the client accepts one may answer 404 for it.
ACCEPT=(
  -H 'Accept: application/vnd.oci.image.index.v1+json'
  -H 'Accept: application/vnd.docker.distribution.manifest.list.v2+json'
  -H 'Accept: application/vnd.oci.image.manifest.v1+json'
  -H 'Accept: application/vnd.docker.distribution.manifest.v2+json'
)

usage() {
  echo "usage: $0 [--self-test | --self-test-offline]" >&2
  exit 2
}

# enumerate prints every digest-pinned reference in the tracked tree, once each.
enumerate() {
  git grep -hoE "$REF_RE" -- . ':(exclude)_legacy' ':(exclude)docs' ':(exclude)*_test.go' |
    sort -u
}

# object_store_ref prints the object store module's default, or fails: it is the
# image this check was written for, so a read that finds nothing is a broken
# check, not a clean tree.
object_store_ref() {
  local ref
  ref="$(object_store_image_from "$OBJECT_STORE_MODULE_REL")"
  if ! [[ "$ref" =~ $OBJECT_STORE_IMAGE_PATTERN ]]; then
    echo "::error::could not read a digest-pinned image from $OBJECT_STORE_MODULE_REL (read \"$ref\")" >&2
    return 1
  fi
  printf '%s\n' "$ref"
}

# manifest_url REF prints the registry API URL of REF's manifest BY DIGEST.
#
# The tag is dropped (see the header). The registry is the first path component
# when it looks like a host — it has a dot or a port, or is `localhost` — and
# Docker Hub otherwise, whose API host is registry-1.docker.io and whose
# single-name images live under `library/`. The tag is the text after the LAST
# colon of the name, which a port's colon never is.
manifest_url() {
  local ref="$1" name digest first host path
  name="${ref%@*}"
  name="${name%:*}"
  digest="${ref##*@}"
  first="${name%%/*}"
  if [[ "$name" == */* && ("$first" == *.* || "$first" == *:* || "$first" == localhost) ]]; then
    host="$first"
    path="${name#*/}"
  else
    host="docker.io"
    path="$name"
  fi
  if [ "$host" = "docker.io" ]; then
    host="registry-1.docker.io"
    [[ "$path" == */* ]] || path="library/$path"
  fi
  printf 'https://%s/v2/%s/manifests/%s\n' "$host" "$path" "$digest"
}

# head_once URL HDRFILE [AUTH-HEADER] prints the HTTP status of one HEAD and
# returns curl's (or timeout's) exit status.
head_once() {
  local url="$1" hdr="$2" auth=()
  [ -z "${3:-}" ] || auth=(-H "$3")
  timeout "$IMAGE_PULL_TIMEOUT" curl -q -sS -I -o /dev/null -D "$hdr" -w '%{http_code}' \
    --connect-timeout 10 "${ACCEPT[@]}" "${auth[@]}" "$url"
}

# attempt REF prints `<status> <detail>` for one anonymous handshake and returns
# 0 only when the manifest answered 200. Status is the HTTP code, or TIMEOUT, or
# ERROR for a transport failure.
attempt() {
  local url hdr code rc challenge realm service scope token
  url="$(manifest_url "$1")"
  hdr="$(mktemp)"
  # shellcheck disable=SC2064
  trap "rm -f '$hdr' '$hdr.err'" RETURN

  rc=0; code="$(head_once "$url" "$hdr" 2>"$hdr.err")" || rc=$?
  if [ "$rc" -eq 0 ] && [ "$code" = "401" ]; then
    # The registry's own challenge names where an anonymous token comes from.
    challenge="$(tr -d '\r' <"$hdr" | awk 'tolower($1) == "www-authenticate:" { sub(/^[^:]*:[ \t]*/, ""); print; exit }')"
    realm="$(sed -n 's/.*realm="\([^"]*\)".*/\1/p' <<<"$challenge")"
    service="$(sed -n 's/.*service="\([^"]*\)".*/\1/p' <<<"$challenge")"
    scope="$(sed -n 's/.*scope="\([^"]*\)".*/\1/p' <<<"$challenge")"
    if [[ "$challenge" == Bearer* && -n "$realm" ]]; then
      token="$(timeout "$IMAGE_PULL_TIMEOUT" curl -q -fsS -G --connect-timeout 10 "$realm" \
        --data-urlencode "service=$service" --data-urlencode "scope=$scope" 2>/dev/null |
        jq -r '.token // .access_token // empty' 2>/dev/null || true)"
      if [ -n "$token" ]; then
        rc=0; code="$(head_once "$url" "$hdr" "Authorization: Bearer $token" 2>"$hdr.err")" || rc=$?
      fi
    fi
  fi

  case "$rc" in
    0) ;;
    124 | 28) echo "TIMEOUT no answer in ${IMAGE_PULL_TIMEOUT}s"; return 1 ;;
    *) echo "ERROR $(tail -n 1 "$hdr.err" 2>/dev/null)"; return 1 ;;
  esac
  echo "$code $url"
  [ "$code" = "200" ]
}

# probe REF prints one verdict line and returns 0 only for `ok`.
#
# Four verdicts, because a hang is not a refusal and neither is a pass: ok,
# TIMED OUT, RATE LIMITED and UNPULLABLE (with what the registry said). A 429
# says nothing about whether the image exists — only that this address has
# spent its anonymous allowance — so it is named apart from a refusal, and it
# still fails: a check that cannot see is not a check that passed. Up to
# IMAGE_PULL_ATTEMPTS tries, and the LAST one decides: a withdrawn image is
# refused every time, a dropped connection is not (one probe failed with
# "network is unreachable" while this was written, and passed on the next try),
# and a weekly run that goes red on a blip teaches people to ignore it.
probe() {
  local ref="$1" out rc n=0
  while :; do
    n=$((n + 1))
    rc=0; out="$(attempt "$ref")" || rc=$?
    [ "$rc" -ne 0 ] && [ "$n" -lt "$IMAGE_PULL_ATTEMPTS" ] || break
    sleep "$IMAGE_PULL_RETRY_DELAY"
  done
  case "$rc:$out" in
    0:*) printf 'ok          %s\n' "$ref" ;;
    *:TIMEOUT*) printf 'TIMED OUT   %s   (%s, %d attempt(s))\n' "$ref" "${out#TIMEOUT }" "$n" ;;
    *:"429 "*) printf 'RATE LIMITED %s   (the registry refused to answer this address, not this image; %d attempt(s))\n' "$ref" "$n" ;;
    *) printf 'UNPULLABLE  %s   (%s, %d attempt(s))\n' "$ref" "$out" "$n" ;;
  esac
  [ "$rc" -eq 0 ]
}

# check_enumeration: the list is non-empty, contains the object store's image,
# and contains no all-zero digest (which could only be a planted negative control
# that escaped into the tree, and would fail every run).
check_enumeration() {
  local refs="$1" os_ref="$2"
  if [ -z "$refs" ]; then
    echo "::error::found no digest-pinned image references — the enumeration is broken, not the tree" >&2
    return 1
  fi
  if ! grep -qxF -- "$os_ref" <<<"$refs"; then
    echo "::error::the enumeration did not find the object store's image ($os_ref) — it is not reading the tree it claims to" >&2
    return 1
  fi
  if grep -qE '@sha256:0{64}$' <<<"$refs"; then
    echo "::error::a tracked file carries an all-zero digest — a negative control written as a literal" >&2
    return 1
  fi
}

run_checks() {
  local refs os_ref ref n=0 bad=0
  os_ref="$(object_store_ref)"
  refs="$(enumerate || true)"
  check_enumeration "$refs" "$os_ref"
  while IFS= read -r ref; do
    n=$((n + 1))
    probe "$ref" || bad=$((bad + 1))
  done <<<"$refs"
  if [ "$bad" -ne 0 ]; then
    echo "FAIL: $bad of $n digest-pinned images cannot be pulled anonymously" >&2
    return 1
  fi
  echo "OK: all $n digest-pinned images can be pulled anonymously"
}

# ---------------------------------------------------------------------------
# Self-test. Each verdict is reached on its own, and each control is one the
# check could plausibly get wrong.
# ---------------------------------------------------------------------------

# A stand-in `curl` for the offline controls. It plays a registry that demands
# the anonymous handshake, behaving as $STUB_MODE says, so the real probe() is
# driven end to end without a network. It logs every manifest request to
# $STUB_CALLS as `<url> <auth-or-none>`.
make_stub() {
  local dir="$1"
  cat >"$dir/curl" <<'STUB'
#!/usr/bin/env bash
q=0; [ "${1:-}" = "-q" ] && q=1
head=0 hdr="" url="" auth="none" prev=""
for a in "$@"; do
  case "$prev" in
    -D) hdr="$a" ;;
    -H) [[ "$a" == Authorization:* ]] && auth="${a#Authorization: }" ;;
  esac
  case "$a" in
    -I) head=1 ;;
    https://*) url="$a" ;;
  esac
  prev="$a"
done
# The token endpoint: an anonymous token, as a public registry hands out.
if [ "$head" = 0 ]; then echo '{"token":"anon"}'; exit 0; fi
if [ "$q" != 1 ]; then echo "stub: curl ran without -q, so a ~/.curlrc could add credentials" >&2; exit 2; fi
echo "$url $auth" >>"$STUB_CALLS"
: >"$hdr"
challenge() {
  printf 'HTTP/1.1 401 Unauthorized\r\nwww-authenticate: Bearer realm="https://auth.example.invalid/token",service="example.invalid",scope="repository:x/y:pull"\r\n\r\n' >"$hdr"
  printf 401
}
case "$STUB_MODE" in
  hang) sleep 30; exit 0 ;;
  # One transport failure, then a working registry.
  blip)
    if [ "$(wc -l <"$STUB_CALLS")" -eq 1 ]; then
      echo "curl: (7) Failed to connect: Network is unreachable" >&2; exit 7
    fi ;;
  # A withdrawn image: even the anonymous token is refused the manifest.
  refuse) challenge; exit 0 ;;
  # An address that has spent its anonymous allowance.
  ratelimit) [ "$auth" = "Bearer anon" ] && { printf 429; exit 0; } ;;
esac
[ "$auth" = "Bearer anon" ] || { challenge; exit 0; }
if [ -n "${STUB_WANT_URL:-}" ] && [ "$url" != "$STUB_WANT_URL" ]; then printf 404; exit 0; fi
printf 200
STUB
  chmod +x "$dir/curl"
}

self_test_offline() {
  local tmp out rc refs os_ref zero_ref digest ref want got
  tmp="$(mktemp -d)"
  # shellcheck disable=SC2064
  trap "rm -rf '$tmp'" RETURN
  make_stub "$tmp"
  digest="sha256:$(printf '%064d' 1)"

  # 1. The URL asked for is the manifest BY DIGEST, on the right API host, for
  #    each shape a reference in this tree takes.
  while IFS='|' read -r ref want; do
    got="$(manifest_url "$ref")"
    [ "$got" = "$want" ] || {
      echo "FAIL: manifest_url $ref = $got, want $want" >&2; return 1; }
  done <<EOF
node:26-alpine@$digest|https://registry-1.docker.io/v2/library/node/manifests/$digest
prom/prometheus:v3.5.0@$digest|https://registry-1.docker.io/v2/prom/prometheus/manifests/$digest
docker.io/minio/minio:RELEASE.x@$digest|https://registry-1.docker.io/v2/minio/minio/manifests/$digest
gcr.io/distroless/static:nonroot@$digest|https://gcr.io/v2/distroless/static/manifests/$digest
example.invalid:5000/x/y:1.2-z@$digest|https://example.invalid:5000/v2/x/y/manifests/$digest
EOF
  echo "  ok: the manifest is asked for by digest alone, on the right registry, for every reference shape"

  # 2. The anonymous handshake: challenged, then retried with the anonymous
  #    token and nothing else, and ok.
  : >"$tmp/calls"
  rc=0
  out="$(STUB_MODE=ok STUB_CALLS="$tmp/calls" STUB_WANT_URL="https://example.invalid:5000/v2/x/y/manifests/$digest" \
    PATH="$tmp:$PATH" probe "example.invalid:5000/x/y:1.2-z@$digest" 2>&1)" || rc=$?
  [ "$rc" -eq 0 ] && [[ "$out" == ok* ]] &&
    [ "$(cut -d' ' -f2- "$tmp/calls" | tr '\n' ',')" = "none,Bearer anon," ] || {
    echo "FAIL: the anonymous handshake did not end ok (rc=$rc, requests: $(tr '\n' ';' <"$tmp/calls")): $out" >&2
    return 1
  }
  echo "  ok: an image is ok through the anonymous handshake — challenged, then the anonymous token only"

  # 3. A refusal is UNPULLABLE, says what the registry answered, and is still a
  #    refusal after every attempt: the retry must end in it, not mask it.
  : >"$tmp/calls"
  rc=0
  out="$(STUB_MODE=refuse STUB_CALLS="$tmp/calls" IMAGE_PULL_ATTEMPTS=3 IMAGE_PULL_RETRY_DELAY=0 \
    PATH="$tmp:$PATH" probe "example.invalid/x:y@$digest")" || rc=$?
  [ "$rc" -ne 0 ] && [[ "$out" == UNPULLABLE* ]] && [[ "$out" == *"401 "* ]] && [[ "$out" == *"3 attempt(s)"* ]] || {
    echo "FAIL: a refused image was not UNPULLABLE with its 401 after 3 attempts (rc=$rc): $out" >&2
    return 1
  }
  echo "  ok: a refused image is UNPULLABLE after every attempt, with the registry's 401"

  # 3b. A 429 is RATE LIMITED — named apart from a refusal — and still fails.
  : >"$tmp/calls"
  rc=0
  out="$(STUB_MODE=ratelimit STUB_CALLS="$tmp/calls" IMAGE_PULL_ATTEMPTS=2 IMAGE_PULL_RETRY_DELAY=0 \
    PATH="$tmp:$PATH" probe "example.invalid/x:y@$digest")" || rc=$?
  [ "$rc" -ne 0 ] && [[ "$out" == "RATE LIMITED"* ]] || {
    echo "FAIL: a 429 was not reported RATE LIMITED and failed (rc=$rc): $out" >&2
    return 1
  }
  echo "  ok: a 429 is RATE LIMITED, not ok and not a refusal"

  # 4. A single transport blip is retried, not reported.
  : >"$tmp/calls"
  rc=0
  out="$(STUB_MODE=blip STUB_CALLS="$tmp/calls" IMAGE_PULL_ATTEMPTS=3 IMAGE_PULL_RETRY_DELAY=0 \
    PATH="$tmp:$PATH" probe "example.invalid/x:y@$digest")" || rc=$?
  [ "$rc" -eq 0 ] && [[ "$out" == ok* ]] || {
    echo "FAIL: one failed attempt followed by a working registry was not ok (rc=$rc): $out" >&2
    return 1
  }
  echo "  ok: a single network blip is retried, not reported"

  # 5. A hang is TIMED OUT — its own verdict — and fails.
  : >"$tmp/calls"
  rc=0
  out="$(STUB_MODE=hang STUB_CALLS="$tmp/calls" IMAGE_PULL_TIMEOUT=1 IMAGE_PULL_ATTEMPTS=2 IMAGE_PULL_RETRY_DELAY=0 \
    PATH="$tmp:$PATH" probe "example.invalid/x:y@$digest")" || rc=$?
  [ "$rc" -ne 0 ] && [[ "$out" == "TIMED OUT"* ]] || {
    echo "FAIL: a registry that never answered was not reported TIMED OUT (rc=$rc): $out" >&2
    return 1
  }
  echo "  ok: a registry that never answers is TIMED OUT, not ok"

  # 6. The enumeration finds the real tree's object store image, and refuses an
  #    empty list, a list without it, and a planted all-zero digest.
  os_ref="$(object_store_ref)"
  refs="$(enumerate)"
  check_enumeration "$refs" "$os_ref" || {
    echo "FAIL: the real tree's enumeration was refused" >&2; return 1; }
  echo "  ok: the enumeration finds $(wc -l <<<"$refs") references, the object store's among them"
  rc=0; check_enumeration "" "$os_ref" >/dev/null 2>&1 || rc=$?
  [ "$rc" -ne 0 ] || { echo "FAIL: an empty enumeration was accepted" >&2; return 1; }
  rc=0; check_enumeration "$(grep -vxF -- "$os_ref" <<<"$refs")" "$os_ref" >/dev/null 2>&1 || rc=$?
  [ "$rc" -ne 0 ] || { echo "FAIL: an enumeration missing the object store image was accepted" >&2; return 1; }
  # Built, never written: a literal all-zero digest in this file would be
  # enumerated by the real run and fail it every week.
  zero_ref="$(printf 'cgr.dev/chainguard/minio:latest@sha256:%064d' 0)"
  rc=0; check_enumeration "$refs"$'\n'"$zero_ref" "$os_ref" >/dev/null 2>&1 || rc=$?
  [ "$rc" -ne 0 ] || { echo "FAIL: a planted all-zero digest was accepted" >&2; return 1; }
  echo "  ok: an empty list, a list without the object store, and a zero digest are each refused"

  # 7. The one reader of the module default returns nothing — which its callers
  #    refuse — for a module whose image variable has no default, or when the
  #    only default is another variable's.
  printf 'variable "image" {\n  type = string\n}\n' >"$tmp/nodefault.tf"
  [ -z "$(object_store_image_from "$tmp/nodefault.tf")" ] || {
    echo "FAIL: a variable with no default yielded a value" >&2; return 1; }
  printf 'variable "other" {\n  default = "a:b@sha256:%064d"\n}\n' 1 >"$tmp/other.tf"
  [ -z "$(object_store_image_from "$tmp/other.tf")" ] || {
    echo "FAIL: another variable's default was read as the image" >&2; return 1; }
  echo "  ok: the module reader returns nothing for a missing default or another variable's"
}

self_test_network() {
  local os_ref zero_ref out rc
  os_ref="$(object_store_ref)"

  # Positive control: the image this exists for must pull today.
  rc=0; out="$(probe "$os_ref")" || rc=$?
  [ "$rc" -eq 0 ] || { echo "FAIL: positive control: $out" >&2; return 1; }
  echo "  ok: positive control — $out"

  # Negative control: a real registry and repository, a digest that cannot
  # exist. Deterministic, unlike relying on some registry staying closed.
  zero_ref="$(printf '%s@sha256:%064d' "${os_ref%@*}" 0)"
  rc=0; out="$(IMAGE_PULL_RETRY_DELAY=1 probe "$zero_ref")" || rc=$?
  [ "$rc" -ne 0 ] && [[ "$out" == UNPULLABLE* ]] || {
    echo "FAIL: negative control was not refused (rc=$rc): $out" >&2; return 1; }
  echo "  ok: negative control — ${out%%   (*}"
}

case "${1:-}" in
  --self-test)
    self_test_offline
    self_test_network
    echo "==> Self-test passed (offline and network controls)"
    ;;
  --self-test-offline)
    self_test_offline
    echo "==> Offline self-test passed (network controls NOT run; --self-test runs them)"
    ;;
  "") run_checks ;;
  *) usage ;;
esac
