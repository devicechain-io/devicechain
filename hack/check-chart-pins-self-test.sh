#!/usr/bin/env bash
# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
# Invoked by check-chart-pins.sh --self-test; fixtures exercise the real guard.
set -euo pipefail

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
mkdir -p "$tmp/hack"
cp "$(dirname "$0")/check-chart-pins.sh" "$(dirname "$0")/tofu-roots.sh" "$tmp/hack/"
tree=$tmp/deploy/opentofu
mkdir -p "$tree/modules/test"

fixture() {
  cat >"$tree/main.tf" <<'HCL'
module "test" {
  source = "./modules/test"
  chart_version = var.chart_version
}
HCL
  cat >"$tree/variables.tf" <<'HCL'
variable "chart_version" {
  default = "1.2.3"
}
HCL
  cat >"$tree/modules/test/main.tf" <<'HCL'
variable "chart_version" {
  default = "1.2.3"
}
resource "helm_release" "http" {
  repository = "https://example.test/charts"
  version = var.chart_version
}
resource "helm_release" "oci" {
  repository = local.repo
  version = var.chart_version
}
resource "helm_release" "variable_repo" {
  repository = var.repo
  version = var.chart_version
}
resource "helm_release" "bundled" {
  chart = "${path.module}/chart"
}
HCL
  # Definitions in another file and after the resources must resolve too.
  cat >"$tree/modules/test/repositories.tf" <<'HCL'
locals {
  repo = local.alias
  alias = var.repo
}
variable "repo" {
  default = "oci://registry.example.test/charts"
}
HCL
}

guard() { bash "$tmp/hack/check-chart-pins.sh"; }
reject() {
  local label=$1 diagnostic=$2 out
  if out=$(guard 2>&1); then
    echo "SELF-TEST FAIL: $label passed" >&2
    exit 1
  fi
  if ! grep -Fq "$diagnostic" <<<"$out"; then
    printf 'SELF-TEST FAIL: %s failed for the wrong reason:\n%s\n' "$label" "$out" >&2
    exit 1
  fi
}

fixture
out=$(guard)
grep -Fq 'all 3 third-party charts' <<<"$out"
grep -Fq '4/4 helm_release resources accounted for, 1 bundled charts' <<<"$out"

fixture
sed -i '/resource "helm_release" "oci"/,/^}/s/version = var.chart_version/version = var.unpinned/' "$tree/modules/test/main.tf"
cat >>"$tree/modules/test/main.tf" <<'HCL'
variable "unpinned" {
  default = ""
}
HCL
reject 'unpinned OCI through locals' 'variable unpinned has no exact pinned default'

fixture
sed -i 's/alias = var.repo/alias = local.hidden/' "$tree/modules/test/repositories.tf"
reject 'hidden repository indirection' 'discovered 3 charts but counted 4 helm_release resources'

fixture
sed -i 's/alias = var.repo/alias = local.repo/' "$tree/modules/test/repositories.tf"
reject 'cyclic repository indirection' 'refusing incomplete coverage'

fixture
sed -i 's/default = "1.2.3"/default = "1.2.3 - 2.0.0"/' "$tree/modules/test/main.tf"
reject 'range default' 'has no exact pinned default'

fixture
sed -i 's/default = "1.2.3"/default = "2.0.0"/' "$tree/variables.tf"
reject 'module/root disagreement' 'the two pins for chart_version disagree'

fixture
sed -i 's/chart_version = var.chart_version/chart_version = "1.2.3"/' "$tree/main.tf"
reject 'literal module argument' 'An explicit argument BEATS'

fixture
sed -i 's/default = "1.2.3"/default = ""/' "$tree/modules/test/main.tf"
# The planted HCL heredoc is written through a quoted shell heredoc and spliced
# in with sed's r command: spelled inline in a sed script, its opener would read
# as a shell heredoc to hack/lib/early-close-pipes.awk, which does not parse
# quoting and fails closed on an opener it never sees close.
cat >"$tree/heredoc-pin.snippet" <<'HCL'
  description = <<-EOT
  default = "1.2.3"
  EOT
HCL
sed -i "/variable \"chart_version\" {/r $tree/heredoc-pin.snippet" "$tree/modules/test/main.tf"
rm -f "$tree/heredoc-pin.snippet"
reject 'pin planted in heredoc' 'has no exact pinned default'

fixture
cat >"$tree/modules/test/extra.tf" <<'HCL'
 resource "helm_release" "hidden_declaration" {
   repository = "oci://registry.example.test/charts"
   version = var.chart_version
 }
HCL
reject 'declaration missed by parser' 'discovered 4 charts but counted 5 helm_release resources'

echo 'check-chart-pins: self-test OK (correct tree and 8 negative controls)'
