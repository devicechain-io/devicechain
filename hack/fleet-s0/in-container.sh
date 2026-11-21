#!/usr/bin/env bash
# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
# Run Tier K experiments (E-g pg, E-j, E-l) reproducibly inside the pinned toolchain image.
#   in-container.sh [E-g|E-j|E-l ...]      default: all three
# Everything is run-specific and removed on exit: one throwaway Postgres container, one docker
# network, one kind cluster (named by run.sh). It never touches another kind cluster or container.
# The candidate premise record is left in $FLEET_S0_OUTPUT_DIR (default: ./fleet-s0-out-<epoch>).
set -euo pipefail
here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
export MSYS_NO_PATHCONV=1
ids=("$@"); (( ${#ids[@]} )) || ids=(E-g E-j E-l)
run="fleet-s0-$(date +%s)-$$"
out="${FLEET_S0_OUTPUT_DIR:-$PWD/$run-out}"
mkdir -p "$out"
pgpass="$(python3 -c 'import secrets;print(secrets.token_hex(16))' 2>/dev/null || date +%s%N)"
image=fleet-s0-runner:local
cleanup() { docker rm -f "$run-pg" >/dev/null 2>&1 || true; }
trap cleanup EXIT
native() { (cd "$1" && { pwd -W 2>/dev/null || pwd; }); }
docker build -q -t "$image" "$(native "$here/runner")" >/dev/null
repo="$(native "$here/../..")"
rc=0
for id in "${ids[@]}"; do
  net=()
  env=(-e FLEET_S0_PROVIDER_VERSION="${FLEET_S0_PROVIDER_VERSION:-8.4.0}" -e FLEET_S0_OUTPUT_DIR="/out/$id")
  case "$id" in
    E-g|E-j)
      docker rm -f "$run-pg" >/dev/null 2>&1 || true
      docker run -d --name "$run-pg" -e POSTGRES_PASSWORD="$pgpass" postgres:16 >/dev/null
      until docker exec "$run-pg" pg_isready -U postgres >/dev/null 2>&1; do sleep 1; done
      net=(--network "container:$run-pg")
      env+=(-e FLEET_S0_PG_ADMIN_URL="postgres://postgres:$pgpass@localhost:5432/postgres?sslmode=disable") ;;
    E-l) net=(--network kind); env+=(-e FLEET_S0_KIND_INTERNAL=1) ;;
  esac
  mkdir -p "$out/$id"
  docker run --rm "${net[@]}" "${env[@]}" --security-opt seccomp=unconfined \
    -v /var/run/docker.sock:/var/run/docker.sock -v "$repo:/w:ro" -v "$(native "$out"):/out" -w /w \
    "$image" bash hack/fleet-s0/run.sh "$id" || rc=$?
done
echo "candidate records under $out"
exit "$rc"
