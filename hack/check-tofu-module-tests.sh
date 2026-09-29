#!/usr/bin/env bash
# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
#
# Run the `tofu test` suites that live beside the shared OpenTofu modules.
#
# WHY THIS EXISTS
#
# Some of what a module promises is a property of the PLAN, not of any value a
# variable can be given, so neither `tofu validate` nor the validation-block check
# (hack/check-tofu-validations.sh) can see it. The first such promise is the
# object store's: its data volume is sized when it is created, and a later apply
# with a different size leaves it alone. That lives in a `lifecycle` block, and a
# `tofu test` against a mocked provider is the cheapest thing that shows the rule
# doing what it says -- no cluster, no credentials.
#
# 🔴 EACH MODULE IS TESTED FROM A COPY, NEVER IN PLACE. `tofu init` leaves a
# .terraform provider cache and a lock file in the directory it runs in, and dcctl
# embeds deploy/opentofu/modules with `all:` -- which captures what is on DISK at
# build time, not what git tracks. Running here in place would put a provider
# binary into the next dcctl a developer builds. So each module is copied to a
# temporary directory together with its siblings (a module may reach another as
# ../<name>), and tested there.
#
# Usage:
#   hack/check-tofu-module-tests.sh
#
# Requires tofu (or terraform, via TF=terraform) on PATH and network access to
# the provider registry.

set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
modules_dir="$repo_root/deploy/opentofu/modules"

TF="${TF:-}"
if [[ -z "$TF" ]]; then
  if command -v tofu >/dev/null 2>&1; then
    TF=tofu
  elif command -v terraform >/dev/null 2>&1; then
    TF=terraform
  else
    echo "FAIL: neither tofu nor terraform is on PATH" >&2
    exit 1
  fi
fi

# A module has a suite when it has a tests/ directory holding at least one
# *.tftest.hcl -- the directory `tofu test` reads by default.
suites=()
for d in "$modules_dir"/*/; do
  d="${d%/}"
  if compgen -G "$d/tests/*.tftest.hcl" >/dev/null; then
    suites+=("$(basename "$d")")
  fi
done

# An empty list would run nothing and print success. The object store's suite
# exists, so finding none means the discovery broke, not that there is nothing
# to test.
if [[ ${#suites[@]} -eq 0 ]]; then
  echo "FAIL: found no module with tests/*.tftest.hcl under $modules_dir" >&2
  exit 1
fi

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT
cp -R "$modules_dir" "$work/modules"
# The copy must not carry a cache from an earlier in-place run: a stale lock
# would pin whatever provider that run resolved.
find "$work/modules" -name .terraform -type d -prune -exec rm -rf {} +
find "$work/modules" -name .terraform.lock.hcl -type f -delete

# 🔴 TEST AGAINST THE PROVIDER THE SHIPPED ROOTS RUN, NOT THE NEWEST ONE. A module
# names its providers with no version (the roots pin them exactly), so a bare init
# here would resolve whatever the registry calls latest. What a suite holds --
# `ignore_changes` on a path through the provider's schema, for one -- is a fact
# about ONE provider version, and a suite green on a different version says
# nothing about the one users run. So each provider the cluster root pins is
# pinned in the copy too, by an override file, read from the root's own
# versions.tf: that file stays the one place a version is written.
pins_file="$repo_root/deploy/opentofu/cluster/versions.tf"
pins="$(awk '
  /^[[:space:]]*[a-z0-9_-]+[[:space:]]*=[[:space:]]*\{[[:space:]]*$/ { name=$1; src=""; next }
  /^[[:space:]]*source[[:space:]]*=/ { src=$3; gsub(/"/, "", src); next }
  /^[[:space:]]*version[[:space:]]*=/ && name != "" && src != "" {
    v=$3; gsub(/"/, "", v); print name, src, v; name=""; src=""
  }
' "$pins_file")"
# An empty parse would pin nothing and quietly fall back to "latest" -- the very
# thing this block exists to stop. The kubernetes pin is the one every current
# suite depends on, so its absence means the parse broke.
if ! grep -q '^kubernetes hashicorp/kubernetes [0-9]' <<<"$pins"; then
  echo "FAIL: could not read the kubernetes provider pin from $pins_file" >&2
  exit 1
fi

rc=0
for m in "${suites[@]}"; do
  echo "== $m"
  # Pin only the providers this module declares: an override may not introduce
  # a requirement the module does not have.
  override="$work/modules/$m/zz_ci_provider_pins_override.tf"
  {
    echo 'terraform {'
    echo '  required_providers {'
    while read -r name src ver; do
      if grep -qs "source *= *\"$src\"" "$work/modules/$m"/*.tf; then
        printf '    %s = {\n      source  = "%s"\n      version = "%s"\n    }\n' "$name" "$src" "$ver"
        echo "   pinned $src $ver" >&2
      fi
    done <<<"$pins"
    echo '  }'
    echo '}'
  } >"$override"
  if ! (cd "$work/modules/$m" && "$TF" init -backend=false -input=false >/dev/null &&
    "$TF" test -no-color); then
    echo "FAIL: $m" >&2
    rc=1
  fi
done
exit "$rc"
