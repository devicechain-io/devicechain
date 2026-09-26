#!/usr/bin/env bash
# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
#
# THE RE-RUN RIG: does a `dcctl install` that failed partway recover when it is run
# again, and does a re-run refuse to report success over a store that never came up?
#
# WHAT THIS GATES
#
# The kubernetes provider pin in deploy/opentofu/{cluster,instance}/versions.tf.
# 🔴 RUN IT WHENEVER THAT PIN MOVES. Nothing else can see what it checks: the defect
# lives in a provider binary talking to a real API server, and it shows only under
# a CLI that stores resource identity (Terraform >= 1.12). CI runs OpenTofu 1.9,
# which cannot show it at all.
#
# THE DEFECT
#
# kubernetes provider 2.38.0 stores a Deployment whose CREATE failed after the object
# existed -- its rollout timed out -- with a resource identity whose every field is
# null. Its plugin SDK then rejects that identity on the next refresh as an
# "Unexpected Identity Change", before the tainted resource can be planned for
# replacement, so every later run fails identically. 3.2.1 carries the SDK fix.
#
# PHASES (each one on a small fixture shaped like the object store: one Deployment,
# Recreate, wait_for_rollout, with a 45s timeout instead of 10m)
#
#   A   NEGATIVE CONTROL, old pin 2.38.0: apply an image that can never pull (fails,
#       tainted), then a good one. The second apply MUST fail with "Unexpected
#       Identity Change". If it succeeds, this CLI does not store identity, the
#       phases below would prove nothing, and the rig exits 2 (INCONCLUSIVE).
#       🔴 A PASS UNDER A BINARY THAT MADE A INCONCLUSIVE IS NOT A PASS.
#   A'  THE PIN MOVE IS A NO-OP ON A HEALTHY CLUSTER: the REAL object-store module
#       plus one of every other kubernetes type the roots use, applied healthy under
#       2.38.0, then moved to the shipped versions.tf with `init -upgrade`. The
#       plan MUST be empty -- an unintended diff here would restart the store (or
#       worse, touch its volume) on every existing cluster.
#   B   THE FIX REACHING A CLUSTER THAT ALREADY FAILED: A's directory, moved to the
#       shipped versions.tf with `init -upgrade`. A good apply must replace the
#       tainted Deployment, it must roll out, its identity must be stored, and a
#       following plan must be empty.
#   C   FRESH AND FAIL-CLOSED, shipped pin: bad, bad, good. The second bad apply
#       must fail on the ROLLOUT again (the re-run replaces and waits; it does not
#       succeed over an unready Deployment), with neither identity error.
#   D   THE UPDATE PATH, shipped pin: good, then bad (an update that times out),
#       then bad again. The provider records the new spec even though the update
#       failed, so the third apply is expected to SUCCEED over a Deployment that
#       never rolled out -- this is recorded, not failed. What must hold is that
#       dcctl's own post-apply check (confirmObjectStoreRolledOut, driven through
#       TestLiveObjectStoreRolloutCheck) refuses it, and accepts it once a good
#       image has rolled out.
#
# NEEDS: kind, kubectl, jq, go, timeout, network (the provider registry, Docker Hub's
# registry image and the object store's own image), and TF: terraform if on PATH,
# else tofu, or set TF. No DeviceChain images.
#
# CLUSTER: creates kind cluster dc-rerun-rig and deletes it on exit, unless
# RIG_KUBE_CONTEXT names an existing context, in which case it uses that one (with
# the default kubeconfig) and cleans up only what it created.
#
# Every $TF call runs under `timeout` (300s; 900s for A', whose apply pulls the real
# object store image and waits up to its own 10m); rc 124 is scored as HUNG, never
# as a pass or a fail. Every step is scored on its exit status AND its output.
#
# Exit: 0 all phases held; 1 something failed or hung; 2 INCONCLUSIVE.

set -euo pipefail

repo="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
shipped_versions="$repo/deploy/opentofu/cluster/versions.tf"
object_store_module="$repo/deploy/opentofu/modules/object-store"

TF="${TF:-$(command -v terraform || command -v tofu || true)}"
for tool in "$TF" kind kubectl jq go timeout; do
  if [[ -z "$tool" ]] || ! command -v "$tool" >/dev/null; then
    echo "FAIL: missing tool: ${tool:-terraform or tofu}" >&2
    exit 1
  fi
done
echo "== CLI: $("$TF" version | head -1)"

work="$(mktemp -d)"
export TF_PLUGIN_CACHE_DIR="$work/plugin-cache"
mkdir -p "$TF_PLUGIN_CACHE_DIR"
created_cluster=""
# `.invalid` is a reserved TLD, so the bad image can never pull. It carries no tag and
# no digest ON PURPOSE: it names nothing, and a digest-pinned reference would be
# enumerated by hack/check-image-pulls.sh's weekly pull of every pinned image, which
# this one must fail by design.
bad_image="registry.invalid/dc-rerun-rig/absent"
# Any small image that starts and stays up. The same pin hack/upgrade-rig.sh uses, so
# the weekly pull check already watches it (registry.k8s.io answers its manifest with a
# redirect that check does not follow, which rules out the pause image).
good_image="registry:2.8.3@sha256:a3d8aaa63ed8681a604f1dea0aa03f100d5895b6a58ace528858a7b332415373"

if [[ -n "${RIG_KUBE_CONTEXT:-}" ]]; then
  ctx="$RIG_KUBE_CONTEXT"
  kubeconfig="${KUBECONFIG:-$HOME/.kube/config}"
else
  ctx="kind-dc-rerun-rig"
  kubeconfig="$work/kubeconfig"
  kind create cluster --name dc-rerun-rig --kubeconfig "$kubeconfig" --wait 120s
  created_cluster="dc-rerun-rig"
fi
export KUBECONFIG="$kubeconfig"
k() { kubectl --context "$ctx" "$@"; }

rig_ns="dc-rerun-rig"
cleanup() {
  if [[ -n "$created_cluster" ]]; then
    kind delete cluster --name "$created_cluster" --kubeconfig "$kubeconfig" >/dev/null 2>&1 || true
  else
    k delete namespace "$rig_ns" "$rig_ns-extra" --ignore-not-found --wait=false >/dev/null 2>&1 || true
  fi
  echo "== logs kept in $work"
}
trap cleanup EXIT
k create namespace "$rig_ns"

failures=0
hung=0
fail() { echo "  FAIL: $*"; failures=$((failures + 1)); }
ok() { echo "  ok:   $*"; }

# tf DIR LABEL ARGS... -- runs $TF in DIR, output to $work/LABEL.log; sets rc and log.
# Bounded by STEP_TIMEOUT seconds (default 300).
tf() {
  local dir="$1" label="$2" limit="${STEP_TIMEOUT:-300}"
  shift 2
  log="$work/$label.log"
  set +e
  (cd "$dir" && timeout "$limit" "$TF" "$@") >"$log" 2>&1
  rc=$?
  set -e
  echo "  [$label] rc=$rc"
  if [[ $rc -eq 124 ]]; then
    echo "  HUNG: $label (killed after ${limit}s); not scored as a pass or a fail"
    hung=$((hung + 1))
  fi
}
has() { grep -qF -- "$1" "$log"; }

# Status of the Deployment instance in DIR's local state: "tainted" or "" (ok).
instance_status() {
  jq -r '.resources[] | select(.type=="kubernetes_deployment_v1" and .mode=="managed")
         | .instances[0].status // ""' "$1/terraform.tfstate"
}
instance_identity_name() {
  jq -r '.resources[] | select(.type=="kubernetes_deployment_v1" and .mode=="managed")
         | .instances[0].identity.name // ""' "$1/terraform.tfstate"
}

old_versions() {
  cat >"$1/versions.tf" <<'EOF'
terraform {
  required_providers {
    kubernetes = {
      source  = "hashicorp/kubernetes"
      version = "2.38.0"
    }
  }
}
EOF
}

# fixture DIR NAME -- one Deployment shaped like the object store.
fixture() {
  cat >"$1/main.tf" <<EOF
variable "image" {
  type = string
}

provider "kubernetes" {
  config_path    = "$kubeconfig"
  config_context = "$ctx"
}

resource "kubernetes_deployment_v1" "this" {
  metadata {
    name      = "$2"
    namespace = "$rig_ns"
  }
  spec {
    replicas = 1
    selector {
      match_labels = {
        app = "$2"
      }
    }
    strategy {
      type = "Recreate"
    }
    template {
      metadata {
        labels = {
          app = "$2"
        }
      }
      spec {
        container {
          name  = "main"
          image = var.image
        }
      }
    }
  }
  wait_for_rollout = true
  timeouts {
    create = "45s"
    update = "45s"
  }
}
EOF
}

rollout_msg="Waiting for rollout to finish"
identity_msg="Unexpected Identity Change"
missing_identity_msg="Missing Resource Identity"

# dcctl_check EXPECT DEPLOYMENT -- drives dcctl's post-apply check against the live
# Deployment. Scored on the test's own verdict line, not go's exit status alone: a
# skipped test also exits 0.
dcctl_check() {
  local expect="$1" name="$2" out="$work/dcctl-check-$1-$2.log"
  set +e
  (cd "$repo/backend/cli" &&
    DCCTL_RIG_KUBE_CONTEXT="$ctx" DCCTL_RIG_NAMESPACE="$rig_ns" DCCTL_RIG_DEPLOYMENT="$name" \
      DCCTL_RIG_EXPECT="$expect" timeout 300 go test ./bootstrap/ -run '^TestLiveObjectStoreRolloutCheck$' -count=1 -v) >"$out" 2>&1
  local grc=$?
  set -e
  if [[ $grc -eq 0 ]] && grep -q -- '--- PASS: TestLiveObjectStoreRolloutCheck' "$out"; then
    ok "dcctl's rollout check answered '$expect' for $name"
  else
    fail "dcctl's rollout check did not answer '$expect' for $name (rc=$grc; see $out)"
  fi
}

# ---------------------------------------------------------------------------
echo "== A: negative control under kubernetes 2.38.0"
a="$work/a"
mkdir -p "$a"
fixture "$a" rerun-rig
old_versions "$a"
tf "$a" a-init init -no-color
[[ $rc -eq 0 ]] || { fail "init under 2.38.0"; exit 1; }
tf "$a" a-apply-bad apply -auto-approve -no-color -var "image=$bad_image"
if [[ $rc -ne 0 ]] && has "$rollout_msg"; then ok "the bad apply failed on its rollout"; else
  fail "the bad apply did not fail on its rollout (rc=$rc)"
  exit 1
fi
if [[ "$(instance_status "$a")" == "tainted" ]]; then ok "the Deployment is tainted"; else
  fail "the Deployment is not tainted after a failed create"
  exit 1
fi
tf "$a" a-apply-good apply -auto-approve -no-color -var "image=$good_image"
if [[ $rc -eq 0 ]]; then
  echo "INCONCLUSIVE: $("$TF" version | head -1) does not store resource identity, so it"
  echo "  cannot show this defect; the phases below would prove nothing. Use Terraform >= 1.12."
  exit 2
fi
if has "$identity_msg"; then ok "reproduced: the re-run fails with '$identity_msg'"; else
  fail "the re-run failed, but not with '$identity_msg' (see $log)"
  exit 1
fi

# ---------------------------------------------------------------------------
echo "== A': moving a HEALTHY cluster from 2.38.0 to the shipped pins plans nothing"
p="$work/aprime"
mkdir -p "$p/modules"
cp -r "$object_store_module" "$p/modules/object-store"
old_versions "$p"
k -n "$rig_ns" create secret generic dc-object-store-credentials \
  --from-literal=MINIO_ROOT_USER=rerunrig \
  --from-literal=MINIO_ROOT_PASSWORD="$(head -c 24 /dev/urandom | od -An -tx1 | tr -d ' \n')" >/dev/null
cat >"$p/main.tf" <<EOF
provider "kubernetes" {
  config_path    = "$kubeconfig"
  config_context = "$ctx"
}

# The real object store module: Deployment, PVC and Service exactly as the roots
# ship them.
module "object_store" {
  source    = "./modules/object-store"
  namespace = "$rig_ns"
  buckets   = ["rig-rdb", "rig-tsdb"]
  storage   = "1Gi"
}

# The remaining kubernetes types the roots use.
resource "kubernetes_namespace_v1" "extra" {
  metadata {
    name = "$rig_ns-extra"
    labels = {
      "app.kubernetes.io/managed-by" = "opentofu"
    }
  }
}

resource "kubernetes_config_map_v1" "extra" {
  metadata {
    name      = "rig-config"
    namespace = "$rig_ns"
  }
  data = {
    "ca.crt" = "not a certificate"
  }
}

data "kubernetes_resources" "pvc" {
  api_version    = "v1"
  kind           = "PersistentVolumeClaim"
  namespace      = "$rig_ns"
  field_selector = "metadata.name=dc-object-store-data"
}

output "pvcs" {
  value = length(data.kubernetes_resources.pvc.objects)
}
EOF
aprime() {
  tf "$p" ap-init init -no-color
  STEP_TIMEOUT=900 tf "$p" ap-apply apply -auto-approve -no-color
  if [[ $rc -eq 0 ]]; then ok "healthy apply under 2.38.0"; else
    fail "the healthy apply under 2.38.0 failed (see $log); A' cannot judge the move"
    return
  fi
  # The fixture has to be converged BEFORE the move, or a diff after it proves
  # nothing about the move: the data source is read before the PVC exists on the
  # first apply, so a second one is needed, and a plan under the OLD pin must then
  # be empty.
  tf "$p" ap-apply-2 apply -auto-approve -no-color
  tf "$p" ap-plan-old plan -detailed-exitcode -no-color
  if [[ $rc -eq 0 ]]; then ok "converged under 2.38.0 (the baseline the move is compared against)"; else
    fail "the fixture does not converge under 2.38.0 (rc=$rc; see $log), so A' cannot judge the move"
    return
  fi
  cp "$shipped_versions" "$p/versions.tf"
  tf "$p" ap-init-upgrade init -upgrade -no-color
  if [[ $rc -eq 0 ]] && grep -q '"3.2.1"' "$p/.terraform.lock.hcl"; then
    ok "init -upgrade moved the lock to 3.2.1"
  else
    fail "init -upgrade did not move the lock to 3.2.1 (see $log)"
  fi
  tf "$p" ap-plan plan -detailed-exitcode -no-color
  if [[ $rc -eq 0 ]]; then ok "no change planned after the pin move"; else
    fail "the pin move plans a change on a healthy cluster (rc=$rc; see $log)"
  fi
}
aprime

# ---------------------------------------------------------------------------
echo "== B: the fix reaching the cluster A left stuck"
cp "$shipped_versions" "$a/versions.tf"
tf "$a" b-init-upgrade init -upgrade -no-color
if [[ $rc -eq 0 ]] && grep -q '"3.2.1"' "$a/.terraform.lock.hcl"; then
  ok "init -upgrade moved the lock to 3.2.1"
else
  fail "init -upgrade did not move A's lock to 3.2.1 (see $log)"
fi
tf "$a" b-apply-good apply -auto-approve -no-color -var "image=$good_image"
if [[ $rc -eq 0 ]] && has "must be replaced"; then ok "the tainted Deployment was replaced"; else
  fail "the re-run did not replace the tainted Deployment (rc=$rc; see $log)"
fi
if has "$identity_msg"; then fail "'$identity_msg' still reported under the shipped pin"; fi
if k -n "$rig_ns" rollout status deployment/rerun-rig --timeout=60s >/dev/null; then
  ok "the replacement rolled out"
else
  fail "the replacement did not roll out"
fi
if [[ "$(instance_identity_name "$a")" == "rerun-rig" ]]; then ok "its identity is stored"; else
  fail "its stored identity is '$(instance_identity_name "$a")', want rerun-rig"
fi
tf "$a" b-plan plan -detailed-exitcode -no-color -var "image=$good_image"
if [[ $rc -eq 0 ]]; then ok "converged"; else fail "plan after the recovery is not empty (rc=$rc)"; fi

# ---------------------------------------------------------------------------
echo "== C: fresh under the shipped pin, and fail-closed"
c="$work/c"
mkdir -p "$c"
fixture "$c" rerun-rig-c
cp "$shipped_versions" "$c/versions.tf"
tf "$c" c-init init -no-color
tf "$c" c-apply-bad-1 apply -auto-approve -no-color -var "image=$bad_image"
if [[ $rc -ne 0 ]] && has "$rollout_msg" && [[ "$(instance_status "$c")" == "tainted" ]]; then
  ok "the first bad apply failed on its rollout and left the Deployment tainted"
else
  fail "the first bad apply did not fail on its rollout with a tainted Deployment (rc=$rc)"
fi
tf "$c" c-apply-bad-2 apply -auto-approve -no-color -var "image=$bad_image"
if [[ $rc -ne 0 ]] && has "$rollout_msg"; then
  ok "the re-run replaced and failed on the rollout again, rather than succeeding"
else
  fail "the re-run over an unpullable image did not fail on its rollout (rc=$rc; see $log)"
fi
for bad in "$identity_msg" "$missing_identity_msg"; do
  if has "$bad"; then fail "the re-run reported '$bad'"; fi
done
tf "$c" c-apply-good apply -auto-approve -no-color -var "image=$good_image"
if [[ $rc -eq 0 ]]; then ok "a good image then installs"; else fail "the good apply failed (see $log)"; fi
tf "$c" c-plan plan -detailed-exitcode -no-color -var "image=$good_image"
if [[ $rc -eq 0 ]]; then ok "converged"; else fail "plan after C is not empty (rc=$rc)"; fi

# ---------------------------------------------------------------------------
echo "== D: an UPDATE that never rolled out"
d="$work/d"
mkdir -p "$d"
fixture "$d" rerun-rig-d
cp "$shipped_versions" "$d/versions.tf"
tf "$d" d-init init -no-color
tf "$d" d-apply-good apply -auto-approve -no-color -var "image=$good_image"
if [[ $rc -eq 0 ]]; then ok "installed"; else fail "the first good apply failed (see $log)"; fi
dcctl_check ready rerun-rig-d
tf "$d" d-apply-bad-1 apply -auto-approve -no-color -var "image=$bad_image"
if [[ $rc -ne 0 ]] && has "$rollout_msg"; then ok "the bad update failed on its rollout"; else
  fail "the bad update did not fail on its rollout (rc=$rc)"
fi
tf "$d" d-apply-bad-2 apply -auto-approve -no-color -var "image=$bad_image"
if [[ $rc -eq 0 ]]; then
  echo "  observed: the re-run's apply SUCCEEDED over the unrolled update (the provider"
  echo "            recorded the new spec); dcctl's own check is what must refuse it"
else
  echo "  observed: the re-run's apply failed (rc=$rc); the provider re-waited"
fi
dcctl_check unready rerun-rig-d
tf "$d" d-apply-good-2 apply -auto-approve -no-color -var "image=$good_image"
if [[ $rc -eq 0 ]]; then ok "a good image then rolls out"; else fail "the recovering apply failed (see $log)"; fi
dcctl_check ready rerun-rig-d

# ---------------------------------------------------------------------------
echo
if [[ $hung -gt 0 ]]; then
  echo "RESULT: $hung step(s) HUNG, $failures failure(s)"
  exit 1
fi
if [[ $failures -gt 0 ]]; then
  echo "RESULT: FAILED ($failures)"
  exit 1
fi
echo "RESULT: PASS -- A reproduced the defect under 2.38.0; A', B, C and D held under the shipped pin"
