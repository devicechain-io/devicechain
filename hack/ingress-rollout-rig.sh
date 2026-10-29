#!/usr/bin/env bash
# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
#
# THE INGRESS ROLLOUT RIG: does restarting an ingress controller drop requests, and does a
# controller pod really stay up for the drain window after it is told to stop?
#
# WHAT THIS GATES
#
# deploy/opentofu/modules/{traefik,ingress-nginx}: drain_seconds / min_ready_seconds. Behind a
# cloud load balancer with externalTrafficPolicy: Local the balancer keeps sending to a node
# until its health check fails it, so a controller pod that leaves at once drops requests
# (GKE perf round 21, D-SR3). The modules give the pod a preStop wait (Traefik) or
# --shutdown-grace-period (nginx) and a minReadySeconds.
#
# 🔴 WHAT KIND CAN AND CANNOT PROVE. kind has no cloud load balancer, so the health-check gap
# that causes the defect does not exist here. This rig proves two things only:
#   1. the values the module renders reach the pod and do what they claim -- a pod told to stop
#      keeps serving for the drain window (measured), and a rolling restart under a 1/s request
#      loop loses nothing (no regression);
#   2. the loop and the counter can see a failure at all (the negative controls below).
# It does NOT prove the cloud balancer stops sending to a drained node. That is proven by the
# next GKE round only: repeat the round-21 rollout test and count failures.
#
# THE VALUES UNDER TEST are the module's own: each phase asks `tofu plan` for the cloud-profile
# (LoadBalancer) values of the REAL module and installs them with helm, changing only the Service
# type to NodePort so helm --wait does not hang on a <pending> LoadBalancer (and, for Traefik,
# switching the Gateway provider off, which needs CRDs this rig does not install).
#
# PHASES (per controller, traefik then nginx), a 2-replica controller and an echo backend behind
# an Ingress, and a 1/s curl loop INSIDE the cluster hitting the controller's Service:
#
#   N1  NEGATIVE CONTROL, the counter: scale the controller to 0 for a few seconds under the
#       loop. The loop MUST record failures. If it records none the rig exits 2 (INCONCLUSIVE):
#       a zero count below would mean nothing.
#   N2  NEGATIVE CONTROL, the drain: install with drain_seconds=0 / min_ready_seconds=0 and
#       delete one pod. It MUST be gone in well under the drain window. If it lingers, the
#       measurement cannot tell a drain from a slow stop and the rig exits 2.
#   A   DRAIN IS REAL: install the module's cloud values, delete one pod, and it MUST stay
#       Terminating for at least drain_seconds - 3 (and exit before terminationGracePeriod).
#   B   ROLLOUT UNDER LOAD: with the cloud values, `kubectl rollout restart` of the controller
#       under the loop. Failures MUST be 0 (MAX_FAILURES overrides), and the loop must have
#       run through the whole rollout.
#
# NEEDS: kind, kubectl, helm, jq, tofu (or TF=terraform), network (chart repos, registries).
#
# CLUSTER: creates kind cluster dc-sr3 with its own kubeconfig (SR3_KUBECONFIG, default
# $HOME/.kube/dc-sr3.yaml), exported for every command, and deletes the cluster on exit. It never
# touches another context. Set SR3_KEEP=1 to keep it.
#
# Exit: 0 pass, 1 a check failed, 2 inconclusive (a negative control did not fail).

set -u
set -o pipefail

here="$(cd "$(dirname "$0")" && pwd)"
repo="$(cd "$here/.." && pwd)"
# tofu wants a native path for a module source (Git Bash's /c/... is not one on Windows).
if command -v cygpath >/dev/null 2>&1; then repo="$(cygpath -m "$repo")"; fi
TF="${TF:-tofu}"
CLUSTER="dc-sr3"
export KUBECONFIG="${SR3_KUBECONFIG:-$HOME/.kube/dc-sr3.yaml}"
MAX_FAILURES="${MAX_FAILURES:-0}"
DRAIN=40
READY=15
work="$(mktemp -d)"

fail() { echo "FAIL: $*" >&2; rc=1; }
inconclusive() { echo "INCONCLUSIVE: $*" >&2; cleanup; exit 2; }
rc=0

cleanup() {
  if [ "${SR3_KEEP:-0}" != 1 ]; then
    kind delete cluster --name "$CLUSTER" >/dev/null 2>&1
    rm -f "$KUBECONFIG"
  fi
  rm -rf "$work"
}
trap cleanup EXIT

kind get clusters 2>/dev/null | grep -qx "$CLUSTER" && { echo "cluster $CLUSTER already exists; delete it first" >&2; exit 1; }
cat >"$work/kind.yaml" <<EOF
kind: Cluster
apiVersion: kind.x-k8s.io/v1alpha4
nodes:
  - role: control-plane
  - role: worker
  - role: worker
EOF
kind create cluster --name "$CLUSTER" --config "$work/kind.yaml" --kubeconfig "$KUBECONFIG" --wait 120s >/dev/null || { echo "kind create failed" >&2; exit 1; }
# (Two workers, above, so two replicas land on different nodes, as they do on a cloud.)
kubectl --kubeconfig "$KUBECONFIG" get nodes >/dev/null || exit 1

helm repo add traefik https://traefik.github.io/charts >/dev/null 2>&1
helm repo add ingress-nginx https://kubernetes.github.io/ingress-nginx >/dev/null 2>&1
helm repo update >/dev/null 2>&1

# values_for <module> <drain> <ready> -> path of the module's rendered helm values (YAML)
values_for() {
  local mod="$1" drain="$2" ready="$3" d="$work/plan-$1-$2-$3"
  mkdir -p "$d"
  cat >"$d/main.tf" <<EOF
terraform {
  required_providers {
    helm = { source = "hashicorp/helm" }
  }
}
provider "helm" {}
module "c" {
  source            = "$repo/deploy/opentofu/modules/$mod"
  replicas          = 2
  drain_seconds     = $drain
  min_ready_seconds = $ready
}
EOF
  ( cd "$d" && $TF init -backend=false >/dev/null && $TF plan -out=p >/dev/null && $TF show -json p ) \
    | jq -r '.planned_values.root_module.child_modules[0].resources[] | select(.type=="helm_release") | .values.values[0]' >"$d/values.yaml" \
    || { echo "could not render values for $mod" >&2; return 1; }
  [ -s "$d/values.yaml" ] || return 1
  echo "$d/values.yaml"
}

install_controller() { # <mod> <values>
  kubectl delete ns ing --ignore-not-found --wait >/dev/null 2>&1
  case "$1" in
    traefik) helm upgrade --install ctl traefik/traefik --version 42.0.0 -n ing --create-namespace -f "$2" \
               --set service.spec.type=NodePort \
               --set providers.kubernetesGateway.enabled=false --set gatewayClass.enabled=false --wait --timeout 300s >/dev/null || return 1 ;;
    nginx)   helm upgrade --install ctl ingress-nginx/ingress-nginx --version 4.15.1 -n ing --create-namespace -f "$2" \
               --set controller.service.type=NodePort --wait --timeout 300s >/dev/null || return 1 ;;
  esac
}

selector() { case "$1" in traefik) echo "app.kubernetes.io/name=traefik";; nginx) echo "app.kubernetes.io/name=ingress-nginx,app.kubernetes.io/component=controller";; esac; }
svc_host() { case "$1" in traefik) echo "ctl-traefik.ing.svc";; nginx) echo "ctl-ingress-nginx-controller.ing.svc";; esac; }
class() { case "$1" in traefik) echo traefik;; nginx) echo nginx;; esac; }

backend() { # <mod>
  kubectl apply -f - >/dev/null <<EOF
apiVersion: v1
kind: Namespace
metadata: { name: app }
---
apiVersion: apps/v1
kind: Deployment
metadata: { name: echo, namespace: app }
spec:
  replicas: 2
  selector: { matchLabels: { app: echo } }
  template:
    metadata: { labels: { app: echo } }
    spec:
      containers:
        - name: echo
          image: hashicorp/http-echo:1.0
          args: ["-text=ok", "-listen=:5678"]
          ports: [{ containerPort: 5678 }]
---
apiVersion: v1
kind: Service
metadata: { name: echo, namespace: app }
spec:
  selector: { app: echo }
  ports: [{ port: 80, targetPort: 5678 }]
---
apiVersion: networking.k8s.io/v1
kind: Ingress
metadata: { name: echo, namespace: app }
spec:
  ingressClassName: $(class "$1")
  rules:
    - host: echo.test
      http:
        paths:
          - { path: /, pathType: Prefix, backend: { service: { name: echo, port: { number: 80 } } } }
EOF
  kubectl -n app rollout status deploy/echo --timeout=120s >/dev/null
}

start_loop() { # <mod>
  kubectl -n app delete pod loop --ignore-not-found --wait >/dev/null 2>&1
  kubectl -n app run loop --image=curlimages/curl:8.10.1 --restart=Never --command -- sh -c \
    "while true; do c=\$(curl -s -m 3 -o /dev/null -w '%{http_code}' -H 'Host: echo.test' http://$(svc_host "$1")/); echo \"\$(date +%s) \$c\"; sleep 1; done" >/dev/null
  kubectl -n app wait --for=condition=Ready pod/loop --timeout=120s >/dev/null
  # wait for the first 200 so a slow route sync is not counted as a rollout failure
  for _ in $(seq 1 60); do kubectl -n app logs loop 2>/dev/null | grep -q ' 200$' && return 0; sleep 2; done
  return 1
}

loop_counts() { # prints "<ok> <failed>" from the loop log
  kubectl -n app logs loop | awk '{ if ($2=="200") ok++; else bad++ } END { print ok+0, bad+0 }'
}

pod_term_seconds() { # <mod> -> seconds the oldest controller pod took to disappear after delete
  local p t0 t1
  p="$(kubectl -n ing get pod -l "$(selector "$1")" -o jsonpath='{.items[0].metadata.name}')"
  t0=$(date +%s)
  kubectl -n ing delete pod "$p" --wait=false >/dev/null
  while kubectl -n ing get pod "$p" >/dev/null 2>&1; do sleep 1; [ $(( $(date +%s) - t0 )) -gt 200 ] && break; done
  t1=$(date +%s)
  echo $(( t1 - t0 ))
}

settle() { kubectl -n ing rollout status "deploy/ctl-$( [ "$1" = traefik ] && echo traefik || echo ingress-nginx-controller)" --timeout=300s >/dev/null; }

for mod in traefik nginx; do
  m=$mod; [ "$mod" = nginx ] && m=ingress-nginx
  echo "=== $mod"
  base="$(values_for "$m" 0 0)" || inconclusive "no baseline values for $mod"
  cloud="$(values_for "$m" $DRAIN $READY)" || inconclusive "no cloud values for $mod"

  # the rendered values must carry the drain, or every phase below proves nothing
  grep -q -E "sleep|shutdown-grace-period" "$cloud" || inconclusive "the module's cloud values for $mod carry no drain"
  grep -q -E "sleep|shutdown-grace-period" "$base" && inconclusive "the baseline values for $mod still carry a drain"

  # --- N1/N2 on the baseline
  install_controller "$mod" "$base" || inconclusive "install failed ($mod baseline)"
  backend "$mod" || inconclusive "backend not ready"
  start_loop "$mod" || inconclusive "loop never got a 200 through $mod"

  kubectl -n ing scale deploy -l "$(selector "$mod")" --replicas=0 >/dev/null; sleep 6
  kubectl -n ing scale deploy -l "$(selector "$mod")" --replicas=2 >/dev/null; settle "$mod"
  read -r ok bad < <(loop_counts)
  echo "N1 counter: ok=$ok failed=$bad (a controller taken to zero)"
  [ "$bad" -gt 0 ] || inconclusive "N1: the loop saw no failure with the controller down; a zero count would mean nothing"

  settle "$mod"; sleep 5
  t="$(pod_term_seconds "$mod")"
  echo "N2 baseline pod took ${t}s to go"
  [ "$t" -lt $(( DRAIN - 10 )) ] || inconclusive "N2: a baseline pod lingered ${t}s; cannot tell a drain from a slow stop"

  # --- A and B with the module's cloud values
  install_controller "$mod" "$cloud" || { fail "$mod: install with the cloud values failed"; continue; }
  backend "$mod" || inconclusive "backend not ready"
  start_loop "$mod" || { fail "$mod: loop never got a 200"; continue; }
  settle "$mod"; sleep 20

  t="$(pod_term_seconds "$mod")"
  echo "A  cloud pod took ${t}s to go (drain ${DRAIN}s)"
  [ "$t" -ge $(( DRAIN - 3 )) ] || fail "$mod: pod left after ${t}s, before the ${DRAIN}s drain"
  settle "$mod"; sleep $(( READY + 5 ))

  start_loop "$mod" || { fail "$mod: loop never got a 200"; continue; }
  d="deploy/ctl-$( [ "$mod" = traefik ] && echo traefik || echo ingress-nginx-controller)"
  kubectl -n ing rollout restart "$d" >/dev/null
  kubectl -n ing rollout status "$d" --timeout=420s >/dev/null || fail "$mod: rollout did not finish"
  sleep $(( DRAIN + 5 ))
  read -r ok bad < <(loop_counts)
  echo "B  rollout under load: ok=$ok failed=$bad (limit $MAX_FAILURES)"
  [ "$ok" -ge 60 ] || fail "$mod: the loop ran only $ok good requests; it did not cover the rollout"
  [ "$bad" -le "$MAX_FAILURES" ] || { fail "$mod: $bad requests failed across the rollout"; kubectl -n app logs loop | awk '$2!="200"'; }
done

[ "$rc" = 0 ] && echo "PASS (on kind: no regression and the drain window is real; the cloud balancer gap is proven only by a GKE round)"
exit "$rc"
