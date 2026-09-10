#!/usr/bin/env bash
# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
# v0.16.0 OPERATOR GATE — the live-cluster run the release is blocked on.
#
# WHAT IT ANSWERS, and why it needs a cluster:
#
# #942 moved sigs.k8s.io/controller-runtime 0.24.1 -> 0.25.0 in backend/k8s. No
# source in that module changed, and NOTHING in CI exercises the reconciler: the
# controllers package is EXCLUDED from the go test matrix by name, and the suite it
# excludes has no specs — it boots an envtest control plane and asserts nothing.
# So "the build is green" says the module COMPILES against the new library and
# nothing whatsoever about whether the manager runs against a real API server.
#
# Everything below is therefore about RUNTIME behaviour on a live apiserver:
# the watch establishes, a create/update/delete on the CRD reaches Reconcile, the
# probes and metrics answer, and the log carries no API errors.
#
# 🔴 EVERY CLAIM HERE CARRIES ITS OWN NEGATIVE CONTROL, because the reconciler's
# only observable effect is a log line. "I saw 'Observed instance'" is not evidence
# that the watch fired — the manager reconciles every existing object at startup,
# so that line is already in the log before this script does anything. The claims
# are made on log lines that appear AFTER a recorded marker, and the script proves
# the detection can come back empty before it reports that it did not.
set -uo pipefail

CTX="${CTX:-kind-devicechain}"
NS="${NS:-dc-k8s-system}"
DEPLOY="${DEPLOY:-dc-k8s-controller-manager}"
REGISTRY="${REGISTRY:-localhost:5000}"
TAG="${TAG:-opgate-$(date +%s)}"
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO="${REPO:-/home/derek/devicechain}"
CR_NAME="opgate-probe"
SP_REFS="${SP_REFS:-$(mktemp)}"

pass=0; fail=0
say()  { printf '\n\033[1;36m==> %s\033[0m\n' "$*"; }
ok()   { printf '\033[0;32m  PASS  %s\033[0m\n' "$*"; pass=$((pass+1)); }
bad()  { printf '\033[1;31m  FAIL  %s\033[0m\n' "$*"; fail=$((fail+1)); }
note() { printf '\033[0;37m        %s\033[0m\n' "$*"; }
k() { kubectl --context "$CTX" "$@"; }

# op_log prints the operator's log from $1 onward (an RFC3339 timestamp).
op_log() { k -n "$NS" logs "deploy/$DEPLOY" --since-time="$1" --tail=-1 2>/dev/null; }
now_rfc3339() { date -u +%Y-%m-%dT%H:%M:%SZ; }

say "0. THE TREE AND THE CLUSTER UNDER TEST"
( cd "$REPO/backend/k8s" && grep -E 'controller-runtime|k8s.io/(api|apimachinery|client-go) v' go.mod | sed 's/^/        /' )
note "apiserver: $(k version -o json 2>/dev/null | jq -r '.serverVersion.gitVersion')"
note "commit:    $(cd "$REPO" && git rev-parse --short HEAD) on $(cd "$REPO" && git rev-parse --abbrev-ref HEAD)"

say "1. BUILD THE OPERATOR FROM THIS TREE AND ROLL IT OUT"
IMG="$REGISTRY/operator:$TAG"
# --image-refs captures the ref WITH ITS DIGEST. The digest is what the identity
# check below is made on, for a reason measured on this cluster: ko's builds are
# reproducible, so every tag this script has ever pushed resolves to the SAME digest,
# and containerd then reports `.status.containerStatuses[].image` as whichever of
# those tags it happened to record first. Comparing that field against the tag we
# just built said "the live pod runs an older image" about a pod that had in fact
# just rolled out correctly.
REFS="$SP_REFS"
( cd "$REPO/backend/k8s" && KO_DOCKER_REPO="$REGISTRY/operator" \
    ko build --bare --tags "$TAG" --platform linux/amd64 --image-refs "$REFS" ./ >/dev/null 2>&1 ) \
  || { bad "ko build"; exit 1; }
BUILT_DIGEST="$(sed -n 's/.*@\(sha256:[0-9a-f]*\).*/\1/p' "$REFS" | head -1)"
[ -n "$BUILT_DIGEST" ] || { bad "ko did not report a digest for the image it published"; exit 1; }
ok "built $IMG ($BUILT_DIGEST)"
k -n "$NS" set image "deploy/$DEPLOY" "$(k -n "$NS" get deploy "$DEPLOY" -o jsonpath='{.spec.template.spec.containers[0].name}')=$IMG" >/dev/null
if k -n "$NS" rollout status "deploy/$DEPLOY" --timeout=180s >/dev/null 2>&1; then
  ok "the manager rolled out and became available on controller-runtime 0.25.0"
else
  bad "the manager did NOT become available"
  k -n "$NS" get pods -o wide; k -n "$NS" logs "deploy/$DEPLOY" --tail=60
  exit 1
fi
# 🔴 THE POD IS RESOLVED BY WAITING FOR EXACTLY ONE LIVE ONE, AND ITS IMAGE IS THEN
# ASSERTED AGAINST WHAT WE JUST BUILT.
#
# The first version of this script took `get pods -l ... -o name | head -1`, and that
# is a gate that cannot fail. `rollout status` returns as soon as the NEW ReplicaSet
# is complete — the OLD pod is usually still Terminating, it carries the same label,
# and whether `head -1` picks it comes down to how two random ReplicaSet hashes sort.
# Measured, not reasoned: it picked the old one. Every claim below then read a pod
# running the PREVIOUS build, the probe port-forward went to a dying container and
# reported 000, and by the last step that pod was gone and `logs` returned nothing.
# The whole exercise would have reported on controller-runtime 0.24.1 while saying
# 0.25.0 at the top.
#
# So: wait until exactly one non-terminating pod remains, refuse to guess if there
# are none or several, and require its running image to be the one this script built.
POD=""
for _ in $(seq 1 60); do
  # jq, not jsonpath: kubectl's jsonpath cannot test for an ABSENT field, and
  # deletionTimestamp is absent on a live pod rather than empty. The jsonpath form
  # matched nothing at all and reported every pod as terminating.
  mapfile -t live < <(k -n "$NS" get pods -l control-plane=controller-manager -o json 2>/dev/null \
    | jq -r '.items[] | select(.metadata.deletionTimestamp == null) | .metadata.name')
  if [ "${#live[@]}" -eq 1 ]; then POD="pod/${live[0]}"; break; fi
  sleep 2
done
if [ -z "$POD" ]; then
  bad "could not resolve a single live manager pod (found ${#live[@]}); refusing to guess"
  k -n "$NS" get pods -o wide
  exit 1
fi
# TWO checks, because neither alone is the claim.
#
#   - the pod's SPEC image says this pod object was created from the Deployment this
#     script patched, rather than being a survivor of the previous ReplicaSet;
#   - the runtime's imageID says the BYTES it is running are the bytes ko published.
#     `.status.containerStatuses[].image` is deliberately NOT used: with reproducible
#     builds several tags share one digest and containerd reports an arbitrary one of
#     them, so that field disagrees with a correct rollout.
SPEC_IMG="$(k -n "$NS" get "$POD" -o jsonpath='{.spec.containers[0].image}')"
RUN_ID="$(k -n "$NS" get "$POD" -o jsonpath='{.status.containerStatuses[0].imageID}')"
if [ "$SPEC_IMG" = "$IMG" ]; then
  ok "the live pod was created from the Deployment this script patched ($IMG)"
else
  bad "the live pod's spec asks for $SPEC_IMG, not $IMG. The rollout did not take."
  exit 1
fi
if [ "${RUN_ID##*@}" = "$BUILT_DIGEST" ]; then
  ok "and it is running exactly the bytes ko published ($BUILT_DIGEST)"
else
  bad "the live pod runs digest ${RUN_ID##*@}, not the $BUILT_DIGEST this script built.
        Nothing below would be about this tree."
  exit 1
fi
note "pod: $POD"
note "restarts: $(k -n "$NS" get "$POD" -o jsonpath='{.status.containerStatuses[0].restartCount}')"

say "2. THE MANAGER CAME UP AND STARTED ITS CONTROLLER"
BOOT="$(k -n "$NS" logs "$POD" --tail=-1 2>/dev/null)"
grep -q "starting manager" <<<"$BOOT" && ok "manager started" || bad "no 'starting manager' in the log"
grep -qi "Starting Controller" <<<"$BOOT" && ok "the Instance controller started" || bad "the controller never started"
grep -qi "Starting workers" <<<"$BOOT" && ok "workers started (the informer synced against the live apiserver)" \
  || bad "workers never started — the cache did not sync, which is the 0.25.0 risk this gate exists for"

say "3. PROBES AND METRICS ANSWER (controller-runtime serves both)"
# The image is distroless, so there is no shell or HTTP client to exec into it.
# Port-forward and probe from here instead.
#
# 🔴 THE FORWARD'S OWNERSHIP IS CHECKED, for the reason hack/dr-rig.sh sets out at
# length on port_forward_alive: a forward leaked by an earlier run answers a bare
# curl perfectly well while OUR kubectl dies on a bind conflict, and the probe then
# reports on whatever pod the stale tunnel points at. A unique port per run plus a
# liveness check on our own process is what keeps "something answered" from being
# read as "this pod answered".
PROBE_PORT=$(( 19000 + RANDOM % 900 ))
k -n "$NS" port-forward "$POD" "$PROBE_PORT:8081" >/dev/null 2>&1 &
PF1=$!
trap 'kill $PF1 2>/dev/null' EXIT
sleep 4
if ! kill -0 "$PF1" 2>/dev/null; then
  bad "the port-forward to $POD died; every probe result below would be from
        something else listening on $PROBE_PORT, which is not evidence about this pod"
else
  for probe in healthz readyz; do
    code="$(curl -s -o /dev/null -w '%{http_code}' --max-time 5 "http://127.0.0.1:$PROBE_PORT/$probe" || true)"
    if [ "$code" = "200" ]; then ok "/$probe answered 200"; else bad "/$probe answered ${code:-nothing}"; fi
  done
  # 🔴 The negative control on the probe: an endpoint the manager does NOT serve must
  # NOT answer 200. Without it, anything at all listening on this port — including a
  # proxy that 200s every path — would satisfy both checks above.
  code="$(curl -s -o /dev/null -w '%{http_code}' --max-time 5 "http://127.0.0.1:$PROBE_PORT/no-such-endpoint" || true)"
  [ "$code" = "200" ] && bad "an endpoint the manager does not serve also answered 200,
        so the two checks above say nothing about which endpoints exist" \
    || ok "an unserved path answered ${code:-nothing}, so the probe checks discriminate"
fi

# METRICS. The deployment passes --metrics-bind-address=0, which DISABLES the metrics
# server (backend/k8s/config/manager/manager.yaml). So the assertion is that the
# operator matches its own configuration, not that it exports series: an earlier
# version of this script asserted controller_runtime_* were served and failed the
# gate on behaviour the platform deliberately turns off.
MARGS="$(k -n "$NS" get deploy "$DEPLOY" -o jsonpath='{.spec.template.spec.containers[0].args}')"
case "$MARGS" in
  *"--metrics-bind-address=0"*)
    ok "metrics are DISABLED by the deployment's own args, and the operator honours that"
    note "🔴 the operator therefore exports NO Prometheus series at all — pre-existing,"
    note "   unchanged by this bump, and worth an operator-facing line" ;;
  *) bad "the deployment no longer passes --metrics-bind-address=0; this check is stale
        and the metrics surface needs asserting rather than skipping. args: $MARGS" ;;
esac

say "4. THE CRD IN THE TREE STILL APPLIES TO A LIVE APISERVER"
# apiextensions-apiserver went 0.36.2 -> 0.37.0 with controller-runtime. The served
# CRD is generated by controller-gen; a --server-side apply is what proves the
# apiserver still accepts the schema this tree generates.
if k apply --server-side --force-conflicts -f "$REPO/backend/k8s/config/crd/bases" >/dev/null 2>&1; then
  ok "the generated CRD applied server-side"
else
  bad "the generated CRD was REJECTED by the apiserver"
  k apply --server-side --force-conflicts -f "$REPO/backend/k8s/config/crd/bases" 2>&1 | tail -5
fi

say "5. THE WATCH IS LIVE — a CREATE reaches Reconcile"
# 🔴 The marker is taken BEFORE the write, and every claim below reads only lines
# after it. Without this the startup reconcile of the existing 'devicechain'
# Instance would satisfy every grep and the watch could be dead.
MARK="$(now_rfc3339)"; sleep 1
# The Instance CRD is CLUSTER-scoped and requires all four of these; a partial
# object is refused by the apiserver's schema validation, which would look like a
# dead watch rather than a bad request.
cat <<EOF | k apply -f - >/dev/null
apiVersion: core.devicechain.io/v1beta1
kind: Instance
metadata:
  name: $CR_NAME
spec:
  name: opgate probe
  description: throwaway Instance created by the v0.16.0 operator gate
  configId: opgate-probe-config
  configuration: {}
EOF
sleep 8
if op_log "$MARK" | grep -q "Observed instance '$CR_NAME'"; then
  ok "CREATE reached Reconcile (a log line for $CR_NAME appeared after the marker)"
else
  bad "CREATE did NOT reach Reconcile within 8s"
  note "lines since the marker:"; op_log "$MARK" | tail -20
fi

say "6. NEGATIVE CONTROL — the detection in step 5 can come back EMPTY"
# Without this, step 5 proves nothing: a grep that matched anything in the log, or
# a --since-time that was silently ignored, would report a live watch over a dead one.
QUIET="$(now_rfc3339)"; sleep 10
if op_log "$QUIET" | grep -q "Observed instance '$CR_NAME'"; then
  bad "the operator kept reconciling $CR_NAME with nothing changing — step 5's grep
        cannot distinguish a watch that fired from a log that always matches, so its
        PASS is not evidence"
else
  ok "10 quiet seconds produced no reconcile line — step 5's detector can report absence"
fi

say "7. AN UPDATE reaches Reconcile"
MARK2="$(now_rfc3339)"; sleep 1
k patch instance "$CR_NAME" --type merge \
  -p '{"metadata":{"annotations":{"opgate/round":"2"}}}' >/dev/null
sleep 8
if op_log "$MARK2" | grep -q "Observed instance '$CR_NAME'"; then
  ok "UPDATE reached Reconcile"
else
  bad "UPDATE did NOT reach Reconcile"; op_log "$MARK2" | tail -20
fi

say "8. A DELETE is clean — no finalizer, no stuck object, no error"
MARK3="$(now_rfc3339)"; sleep 1
k delete instance "$CR_NAME" --timeout=60s >/dev/null 2>&1 \
  && ok "the Instance deleted within 60s" \
  || bad "the delete did not complete — check for a stuck finalizer"
k get instance "$CR_NAME" >/dev/null 2>&1 \
  && bad "the object still exists after delete" \
  || ok "the object is gone"

say "9. THE LOG CARRIES NO API ERRORS ACROSS THE WHOLE EXERCISE"
BAD_PATTERNS='Reconciler error|failed to watch|unable to (start|create)|the server could not find|is forbidden|Unauthorized|no matches for kind|conversion webhook|panic:'
HITS="$(k -n "$NS" logs "$POD" --tail=-1 2>/dev/null | grep -nEi "$BAD_PATTERNS" || true)"
if [ -z "$HITS" ]; then
  ok "no API error, RBAC refusal, watch failure or panic in the manager's log"
else
  bad "the manager's log carries API problems:"; printf '%s\n' "$HITS" | head -20
fi
# 🔴 That check's own negative control: a pattern set that matches nothing because it
# is malformed reports the same clean result as a genuinely clean log.
if k -n "$NS" logs "$POD" --tail=-1 2>/dev/null | grep -qEi 'starting manager'; then
  ok "the pattern engine reads this log at all (a known-present string matched)"
else
  bad "grep found nothing in this log, including a string known to be in it — step 9's
        clean result is vacuous"
fi

say "10. DEPRECATION / SKEW SURFACE"
note "client-go v0.37.0 against apiserver $(k version -o json 2>/dev/null | jq -r '.serverVersion.gitVersion') — client one minor AHEAD of the server"
DEP="$(k -n "$NS" logs "$POD" --tail=-1 2>/dev/null | grep -i "deprecat" || true)"
[ -z "$DEP" ] && ok "no deprecation warning from the client libraries" || { bad "deprecation warnings:"; printf '%s\n' "$DEP" | head; }

say "RESULT: $pass passed, $fail failed"
[ "$fail" -eq 0 ] || exit 1
