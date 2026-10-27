#!/usr/bin/env bash
# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
#
# broker-image-smoke.sh <broker-image-ref>
#
# Run the BUILT broker image (backend/broker/dc-nats-server) in a container and check it from
# outside, the way a device and the chart's hooks see it:
#
#   1. the MQTT listener sends a CertificateRequest; a device presenting a certificate completes
#      TLS and its chain (16 KiB included) reaches a real auth callout as ClientTLS.Certs; a token
#      device presenting no certificate connects and publishes exactly as before; a certificate
#      signed with the wrong key fails the handshake (brokersmoke check);
#   2. the binary's own gauge reads dc_broker_mqtt_client_auth_request 1;
#   3. a SIGHUP (what the chart's config reloader sends) and `nats-server -sl=reload` through
#      the binary on the image's PATH both reload, and the listener still requests certificates;
#   4. `nats-server -sl=ldm=<pid file>` — the chart's preStop command, which works only while the
#      binary is named nats-server and on PATH — takes the server through lame duck mode to a
#      clean exit (status 0);
#   5. 🔴 THE NEGATIVE CONTROL: the identical check against the UPSTREAM image must FAIL, and
#      pass only when told the listener requests nothing. Without it, step 1 could be passing
#      because the harness cannot tell a certificate request from its absence.
#
# The module's own tests cover the same ground against an embedded server; this is the copy
# that runs against the artifact the release publishes — ko's build, the base image, its user
# (65532), its PATH.
#
# Requires docker, go and curl. Every container it starts is named dc-broker-smoke-<pid>-* and
# removed on exit; nothing else is touched.

set -euo pipefail

image="${1:-}"
if [ -z "$image" ]; then
  echo "usage: $(basename "$0") <broker-image-ref>" >&2
  exit 2
fi

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
module="$root/backend/broker/dc-nats-server"

# The upstream image the negative control runs: the version the broker is built on (the
# version-skew guard keeps this in step), pinned by digest.
upstream_image="nats:2.15.1-alpine@sha256:7ae34c25dfd6fe2e01c6483eabef20d796690fdcac6c4d94ca780a6cf9877fde"

for tool in docker go curl; do
  command -v "$tool" >/dev/null 2>&1 || { echo "FAIL: $tool is required" >&2; exit 2; }
done

say() { printf '\033[1;36m==> %s\033[0m\n' "$*"; }
fail() { printf '\033[1;31mFAIL: %s\033[0m\n' "$*" >&2; exit 1; }

work="$(mktemp -d)"
prefix="dc-broker-smoke-$$"
started=()
cleanup() {
  local c
  for c in "${started[@]+"${started[@]}"}"; do
    docker rm -f "$c" >/dev/null 2>&1 || true
  done
  rm -rf "$work"
}
trap cleanup EXIT

say "building the smoke client"
(cd "$module" && go build -o "$work/brokersmoke" ./cmd/brokersmoke)
smoke="$work/brokersmoke"

say "minting the PKI, the callout keys and the broker config"
"$smoke" prepare -dir "$work/pki" -san localhost -san 127.0.0.1 -conf-dir /smoke
chmod 755 "$work/pki"
chmod 644 "$work/pki"/*

# run_broker <name> <image> — starts a broker on loopback ports chosen by docker.
run_broker() {
  local name="$1" img="$2"
  docker run -d --name "$name" \
    -v "$work/pki:/smoke:ro" \
    --tmpfs /var/run/nats:uid=65532 \
    -p 127.0.0.1::1883 -p 127.0.0.1::4222 -p 127.0.0.1::7778 \
    "$img" -c /smoke/nats.conf >/dev/null
  started+=("$name")
}
# The host port docker chose. Captured first and cut in the shell rather than piped through
# `head`: an early-closing reader turns the writer's SIGPIPE into the pipeline's status under
# pipefail (hack/lib/early-close-pipes.awk).
port() {
  local out
  out="$(docker port "$1" "$2/tcp")"
  out="${out%%$'\n'*}"
  echo "${out##*:}"
}
metric() { curl -fsS "http://127.0.0.1:$1/metrics" 2>/dev/null | awk -v n="$2" '$1 == n { print $2 }'; }
logs() { docker logs "$1" 2>&1; }

# ---------------------------------------------------------------------------
say "starting the broker image: $image"
ours="$prefix-ours"
run_broker "$ours" "$image"
mqtt="127.0.0.1:$(port "$ours" 1883)"
nats="tls://127.0.0.1:$(port "$ours" 4222)"
met="$(port "$ours" 7778)"

say "checking it from outside"
"$smoke" check -dir "$work/pki" -nats "$nats" -mqtt "$mqtt" || {
  logs "$ours" | tail -40
  fail "the broker image did not pass the device certificate checks"
}

# The binary's own watcher. It probes once the server is ready, so give it a moment.
v=""
for _ in $(seq 1 30); do
  v="$(metric "$met" dc_broker_mqtt_client_auth_request)"
  [ "$v" = "1" ] && break
  sleep 1
done
[ "$v" = "1" ] || fail "dc_broker_mqtt_client_auth_request is '$v', want 1"
echo "    dc_broker_mqtt_client_auth_request 1"

# reload <how...> — runs the reload, waits for the probe that follows it, and re-checks.
reload_and_check() {
  local label="$1"
  shift
  local before after reloads_before reloads_after
  before="$(metric "$met" dc_broker_mqtt_client_auth_probes_total)"
  reloads_before="$(logs "$ours" | grep -c 'Reloaded server configuration' || true)"
  "$@" >/dev/null
  for _ in $(seq 1 30); do
    after="$(metric "$met" dc_broker_mqtt_client_auth_probes_total)"
    [ -n "$after" ] && [ "$after" -gt "$before" ] && break
    sleep 1
  done
  reloads_after="$(logs "$ours" | grep -c 'Reloaded server configuration' || true)"
  # A reload that did not happen proves nothing about a reload.
  [ "$reloads_after" -gt "$reloads_before" ] || fail "$label: the broker log records no reload"
  [ "$(metric "$met" dc_broker_mqtt_client_auth_request)" = "1" ] || fail "$label: the gauge fell to 0 after the reload"
  # The whole check again, not just the probe: it brings its own callout responder, which a
  # certificate device needs to be admitted at all.
  "$smoke" check -dir "$work/pki" -nats "$nats" -mqtt "$mqtt" >"$work/reload.out" 2>&1 ||
    { cat "$work/reload.out"; fail "$label: after the reload the broker no longer passes the device certificate checks"; }
  echo "    $label: reloaded, and every device certificate check still passes"
}

say "reloading the way the chart's config reloader does (SIGHUP)"
reload_and_check "SIGHUP" docker kill -s HUP "$ours"

say "reloading through the binary on the image's PATH (nats-server -sl=reload)"
reload_and_check "-sl=reload" docker exec "$ours" nats-server -sl=reload=/var/run/nats/nats.pid

say "lame duck shutdown through the chart's preStop command (nats-server -sl=ldm)"
docker exec "$ours" nats-server -sl=ldm=/var/run/nats/nats.pid
status=""
for _ in $(seq 1 90); do
  if [ "$(docker inspect -f '{{.State.Running}}' "$ours")" = "false" ]; then
    status="$(docker inspect -f '{{.State.ExitCode}}' "$ours")"
    break
  fi
  sleep 1
done
[ -n "$status" ] || fail "the broker did not exit within 90s of -sl=ldm"
[ "$status" = "0" ] || { logs "$ours" | tail -20; fail "the broker exited $status after lame duck mode, want 0"; }
logs "$ours" >"$work/ldm.log"
grep -q "Entering lame duck mode" "$work/ldm.log" || fail "the broker log does not record lame duck mode"
echo "    entered lame duck mode and exited 0"

# ---------------------------------------------------------------------------
say "NEGATIVE CONTROL: the same checks against the upstream image"
pulled="$("$root/hack/docker-pull.sh" "$upstream_image")"
up="$prefix-upstream"
run_broker "$up" "$pulled"
umqtt="127.0.0.1:$(port "$up" 1883)"
unats="tls://127.0.0.1:$(port "$up" 4222)"
if "$smoke" check -dir "$work/pki" -nats "$unats" -mqtt "$umqtt" >"$work/control.out" 2>&1; then
  cat "$work/control.out"
  fail "the checks PASSED against the upstream image, which requests no client certificate: they cannot tell the broker image from upstream"
fi
grep -q '^FAIL  the MQTT listener sends a CertificateRequest' "$work/control.out" ||
  { cat "$work/control.out"; fail "the control failed, but not on the certificate request; it proves nothing"; }
"$smoke" check -dir "$work/pki" -nats "$unats" -mqtt "$umqtt" -expect-request=false ||
  fail "the harness could not drive the upstream broker at all, so its failure above proves nothing"
echo "    the upstream image fails the checks, and passes only when told it requests nothing"

say "broker image smoke passed"
