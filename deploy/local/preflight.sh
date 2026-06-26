#!/usr/bin/env bash
# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
#
# Local-cluster preflight doctor.
#
# Proactively diagnoses everything that commonly bites a local bring-up BEFORE
# you waste time on a half-created cluster: required tooling, the docker engine,
# kernel/cgroup settings, disk headroom, networking, and version skew. Prints an
# actionable fix for every problem it finds.
#
# This is the runnable source-of-truth for the host baseline; as we discover new
# gotchas, add a check here (and mirror it into `dcctl preflight`). Exit code is
# non-zero if any hard requirement (FAIL) is unmet; WARNs don't block.

set -uo pipefail

# ---- config (override via env) ---------------------------------------------
MIN_CPUS=${MIN_CPUS:-4}
MIN_MEM_GI=${MIN_MEM_GI:-8}
MIN_DISK_GI=${MIN_DISK_GI:-40}
REGISTRY_PORT=${REGISTRY_PORT:-5000}

# ---- output helpers --------------------------------------------------------
# Color-aware: honour NO_COLOR and disable on non-TTY / dumb terminals.
if [ -t 1 ] && [ -z "${NO_COLOR:-}" ] && [ "${TERM:-dumb}" != "dumb" ]; then
  RED=$'\e[31m'; GREEN=$'\e[32m'; YELLOW=$'\e[33m'; BOLD=$'\e[1m'; RESET=$'\e[0m'
else
  RED=""; GREEN=""; YELLOW=""; BOLD=""; RESET=""
fi
fails=0; warns=0
pass() { printf '  %s✅ %s%s\n' "$GREEN" "$1" "$RESET"; }
warn() { printf '  %s⚠️  %s%s\n     %s↳ %s%s\n' "$YELLOW" "$1" "$RESET" "$YELLOW" "$2" "$RESET"; warns=$((warns+1)); }
fail() { printf '  %s❌ %s%s\n     %s↳ %s%s\n' "$RED" "$1" "$RESET" "$RED" "$2" "$RESET"; fails=$((fails+1)); }
section() { printf '\n%s%s %s%s\n' "$BOLD" "$2" "$1" "$RESET"; }
have() { command -v "$1" >/dev/null 2>&1; }

# ---- 1. required tooling ---------------------------------------------------
section "Tooling" "🧰"
check_tool() { # name guidance [alt...]
  local primary="$1" guidance="$2"; shift 2
  for n in "$primary" "$@"; do
    if have "$n"; then pass "$n ($(command -v "$n"))"; return; fi
  done
  fail "$primary not found" "$guidance"
}
check_tool docker        "install a native Docker engine (not Docker Desktop)"
check_tool kind          "go install sigs.k8s.io/kind@latest"
check_tool kubectl       "https://kubernetes.io/docs/tasks/tools/"
check_tool helm          "https://helm.sh/docs/intro/install/"
check_tool tofu          "install OpenTofu (https://opentofu.org) or Terraform" terraform
# cloud-provider-kind is needed for type=LoadBalancer (ingress-nginx, NATS MQTT).
if have cloud-provider-kind; then pass "cloud-provider-kind ($(command -v cloud-provider-kind))"
else warn "cloud-provider-kind not found" "go install sigs.k8s.io/cloud-provider-kind@latest — LoadBalancer services will stay <pending> without it"; fi

# ---- 2. docker engine ------------------------------------------------------
section "Docker engine" "🐳"
if have docker && docker info >/dev/null 2>&1; then
  pass "docker daemon reachable"
  ctx=$(docker context show 2>/dev/null || echo unknown)
  if [ "$ctx" = "default" ]; then pass "docker context = default (native engine)"
  else warn "docker context = $ctx" "for the clean WSL2/Linux-native path run: docker context use default"; fi

  cpus=$(docker info --format '{{.NCPU}}' 2>/dev/null || echo 0)
  memb=$(docker info --format '{{.MemTotal}}' 2>/dev/null || echo 0)
  memgi=$(( memb / 1024 / 1024 / 1024 ))
  [ "${cpus:-0}" -ge "$MIN_CPUS" ] && pass "engine CPUs: $cpus" || warn "engine CPUs: $cpus" "recommend >= $MIN_CPUS for the full stack"
  [ "${memgi:-0}" -ge "$MIN_MEM_GI" ] && pass "engine memory: ${memgi}Gi" || warn "engine memory: ${memgi}Gi" "recommend >= ${MIN_MEM_GI}Gi"

  sd=$(docker info --format '{{.Driver}}' 2>/dev/null)
  [ "$sd" = "overlay2" ] && pass "storage driver: overlay2" || warn "storage driver: $sd" "overlay2 is recommended for kind"
else
  fail "docker daemon not reachable" "start dockerd (e.g. 'sudo service docker start') and ensure your user is in the 'docker' group"
fi

# ---- 3. kernel / cgroup ----------------------------------------------------
section "Kernel & cgroups" "🧬"
cg=$(stat -fc %T /sys/fs/cgroup 2>/dev/null || echo unknown)
[ "$cg" = "cgroup2fs" ] && pass "cgroup v2 (cgroup2fs)" || warn "cgroups: $cg" "kind prefers cgroup v2; modern WSL2/distros default to it"

read_sysctl() { sysctl -n "$1" 2>/dev/null || echo 0; }
ino_inst=$(read_sysctl fs.inotify.max_user_instances)
ino_watch=$(read_sysctl fs.inotify.max_user_watches)
[ "${ino_inst:-0}" -ge 256 ] && pass "fs.inotify.max_user_instances = $ino_inst" \
  || fail "fs.inotify.max_user_instances = $ino_inst (too low)" \
       "echo 'fs.inotify.max_user_instances=512' | sudo tee /etc/sysctl.d/99-kind.conf && sudo sysctl -p /etc/sysctl.d/99-kind.conf"
[ "${ino_watch:-0}" -ge 524288 ] && pass "fs.inotify.max_user_watches = $ino_watch" \
  || warn "fs.inotify.max_user_watches = $ino_watch (low)" \
       "echo 'fs.inotify.max_user_watches=524288' | sudo tee -a /etc/sysctl.d/99-kind.conf && sudo sysctl -p /etc/sysctl.d/99-kind.conf"

# ---- 4. disk ---------------------------------------------------------------
section "Disk" "💾"
# Headroom on the filesystem backing the docker data-root (images + PVCs).
droot=$(docker info --format '{{.DockerRootDir}}' 2>/dev/null || echo /var/lib/docker)
avail_gi=$(df -BG --output=avail "$droot" 2>/dev/null | tail -1 | tr -dc '0-9')
if [ -n "${avail_gi:-}" ]; then
  [ "$avail_gi" -ge "$MIN_DISK_GI" ] && pass "free on $droot: ${avail_gi}Gi" \
    || fail "free on $droot: ${avail_gi}Gi (low)" "free up space; need >= ${MIN_DISK_GI}Gi for images + DB PVCs"
else warn "could not read free space for $droot" "check 'df -h $droot'"; fi
# Warn if the docker root lives on a Windows 9p mount (slow/unsafe for DB fsync).
case "$droot" in
  /mnt/*) warn "docker data-root is on a 9p mount ($droot)" "move it to native ext4 — 9p is too slow/unsafe for database PV fsync";;
esac

# ---- 5. WSL2 specifics -----------------------------------------------------
if grep -qiE "microsoft|wsl" /proc/version 2>/dev/null; then
  section "WSL2" "🪟"
  pass "running under WSL2 ($(uname -r))"
  net=$(cat /mnt/c/Users/*/.wslconfig 2>/dev/null | grep -iE "networkingMode" | head -1)
  case "$net" in
    *mirrored*) pass "networkingMode=mirrored (localhost shared with Windows)";;
    "") warn "networkingMode not set in .wslconfig" "consider networkingMode=mirrored so the Windows browser can reach the cluster on localhost";;
    *) warn "networkingMode is non-mirrored" "mirrored mode simplifies reaching the cluster from the Windows browser";;
  esac
  if cat /mnt/c/Users/*/.wslconfig 2>/dev/null | grep -qiE "sparseVhd\s*=\s*true"; then
    pass "sparseVhd=true (freed disk returns to the host)"
  else warn "sparseVhd not enabled" "add 'sparseVhd=true' to .wslconfig so the vhdx reclaims freed space (otherwise it only grows)"; fi
fi

# ---- 6. port / registry conflicts -----------------------------------------
section "Ports & registry" "🔌"
port_in_use() { (have ss && ss -ltn 2>/dev/null | grep -q ":$1 ") || (have lsof && lsof -iTCP:"$1" -sTCP:LISTEN >/dev/null 2>&1); }
for p in 80 443 "$REGISTRY_PORT"; do
  if port_in_use "$p"; then
    if [ "$p" = "$REGISTRY_PORT" ] && docker ps --format '{{.Names}}' 2>/dev/null | grep -q kind-registry; then
      pass "port $p in use by the kind-registry container (expected)"
    else warn "port $p already in use" "free it or another service may conflict with the cluster/registry"; fi
  else pass "port $p free"; fi
done

# ---- 7. existing cluster ---------------------------------------------------
section "Existing state" "📂"
if have kind && kind get clusters 2>/dev/null | grep -qx devicechain; then
  warn "a kind cluster named 'devicechain' already exists" "'make up' is idempotent, or run 'make down' first for a clean slate"
else pass "no conflicting 'devicechain' kind cluster"; fi

# ---- summary ---------------------------------------------------------------
printf '\n%s' "$BOLD"
if [ "$fails" -gt 0 ]; then
  printf '%sPreflight: %d failure(s), %d warning(s) — fix failures before bring-up.%s\n' "$RED" "$fails" "$warns" "$RESET"
  exit 1
elif [ "$warns" -gt 0 ]; then
  printf '%sPreflight: ready, with %d warning(s) — review above.%s\n' "$YELLOW" "$warns" "$RESET"
else
  printf '%sPreflight: all checks passed.%s\n' "$GREEN" "$RESET"
fi
exit 0
