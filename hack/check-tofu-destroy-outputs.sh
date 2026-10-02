#!/usr/bin/env bash
# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
#
# Pins the two facts dcctl destroy's output filter rests on, against a REAL binary.
#
# WHY THIS EXISTS
#
# dcctl destroy runs `tofu destroy` on the instance root with only the two variables it
# needs to reach the cluster. A destroy first refreshes in normal mode, which works every
# output value out again from the configuration and the variables it was given, so the
# plan's closing "Changes to Outputs:" list printed the configuration's DEFAULTS for every
# other input: one broker server for a three-server instance, the default backup path for
# a restored one. tofuExec.Destroy (backend/cli/bootstrap/tofuexec.go) therefore drops that
# block from what it streams, keyed on the header line.
#
# A filter keyed on a line of a third-party tool's text has one way to break: the tool
# renames the line, the filter matches nothing, and the false list comes back. This
# fails CI when that happens, on the pinned binary, reading the header FROM THE GO
# SOURCE so the two cannot drift apart.
#
# It also asserts the mechanism itself — a destroy given no variables prints an output at
# its default, not the value the resource was applied with. If that ever stops holding,
# the list would be TRUE and the filter would be hiding it: this check fails and says so,
# and the filter may then be removable.
#
#   hack/check-tofu-destroy-outputs.sh
#   TOFU=terraform hack/check-tofu-destroy-outputs.sh   # a machine with only terraform
#
# The scratch configuration uses only the built-in terraform_data resource, so init
# downloads nothing and no cluster, backend or credential is involved.

set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
SRC="$ROOT/backend/cli/bootstrap/tofuexec.go"
TOFU="${TOFU:-tofu}"

header="$(sed -n 's/^const tofuOutputsHeader = "\(.*\)"$/\1/p' "$SRC")"
# An empty read must not reach a grep: an empty -x pattern would answer for a blank line.
if [ -z "$header" ]; then
  echo "ERROR: could not read tofuOutputsHeader from $SRC; nothing was checked." >&2
  exit 1
fi

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

cat >"$work/main.tf" <<'EOF'
variable "replicas" {
  default = 1
}

resource "terraform_data" "r" {
  input = var.replicas
}

output "replicas" {
  value = var.replicas
}
EOF

cd "$work"
"$TOFU" init -input=false -no-color >/dev/null
"$TOFU" apply -auto-approve -input=false -no-color -var replicas=3 >/dev/null

# The applied value must be 3, or the default printed below would prove nothing: an apply
# that silently took the default makes "prints 1" true for the wrong reason.
applied="$("$TOFU" output -raw replicas)"
if [ "$applied" != "3" ]; then
  echo "ERROR: the scratch apply recorded replicas=$applied, not 3; the destroy below would prove nothing." >&2
  exit 1
fi

# No -var: that is what dcctl passes for every variable but two.
out="$("$TOFU" destroy -auto-approve -input=false -no-color)"

if ! grep -qxF -- "$header" <<<"$out"; then
  echo "FAIL: $TOFU destroy no longer prints the line tofuExec.Destroy filters on:" >&2
  echo "  want a line exactly: $header" >&2
  echo "  so dcctl destroy would print the output list again, with default values. Its output was:" >&2
  printf '%s\n' "$out" >&2
  exit 1
fi

if ! grep -qxF -- '  - replicas = 1 -> null' <<<"$out"; then
  echo "FAIL: $TOFU destroy, given no variables, no longer prints the output at its default (1)" >&2
  echo "  for a resource applied with 3. If this binary now prints the applied value, the list" >&2
  echo "  is the instance's own and the filter in tofuexec.go may no longer be needed; check" >&2
  echo "  before removing it. Its output was:" >&2
  printf '%s\n' "$out" >&2
  exit 1
fi

version="$("$TOFU" version | sed -n '1p')"
echo "ok: $version prints \"$header\" above default-valued outputs on a destroy"
