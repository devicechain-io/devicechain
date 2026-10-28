#!/usr/bin/env bash
# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
#
# Mutation check for the Towerpulse model: breaks one line of the model at a time and requires the
# dotnet tests to go RED. A mutant that survives is a line the tests do not actually pin.
#
#   demos/towerpulse-unity/Dotnet~/mutation-check.sh
#
# Manual, like the repo's other rigs (a full run is a few minutes). It starts by running the tests
# UNMUTATED and aborts if they are not green: a red baseline would "kill" every mutant for free.
# Each mutant must also actually change the file (a sed that matched nothing is reported as a
# failure, not a survivor), and the original is restored on exit, whatever happens.

set -uo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
src="$here/../Assets/Towerpulse/Scripts/Model/BuildingModel.cs"
backup="$(mktemp)"
cp "$src" "$backup"
trap 'cp "$backup" "$src"; rm -f "$backup"' EXIT

run_tests() { (cd "$here/Tests" && dotnet test -c Release --nologo -v q >/dev/null 2>&1); }

if ! run_tests; then echo "ABORT: the baseline is not green; no mutant means anything yet" >&2; exit 2; fi

survived=0; bad=0
# label | exact text in BuildingModel.cs | replacement
mutants=(
  "completion: always complete|if (z.TempC <= z.SetpointC + SetpointToleranceC) {|if (true) {"
  "completion: complete while still hot|if (z.TempC <= z.SetpointC + SetpointToleranceC) {|if (z.TempC >= z.SetpointC + SetpointToleranceC) {"
  "completion: never complete|if (z.TempC <= z.SetpointC + SetpointToleranceC) {|if (false) {"
  "expiry: never expire|else if (t - b.IssuedAtS >= b.WindowS) {|else if (false) {"
  "solar: no gain|return z.GlassM2 * PeakSolarWPerM2 * facing * elevation * clear;|return 0.0;"
  "solar: ignore the facade|return z.GlassM2 * PeakSolarWPerM2 * facing * elevation * clear;|return z.GlassM2 * PeakSolarWPerM2 * elevation * clear;"
  "solar: sun cools|if (facing < 0) facing = 0;|"
  "restart: complete when merely running|c.Running && c.ChwSupplyC <= CompleteChwC|c.Running"
  "boost: damper not forced open|if (z.BoostOverride) cmd = 100.0;|if (false) cmd = 100.0;"
)
for m in "${mutants[@]}"; do
  IFS='|' read -r label from to <<<"$m"
  cp "$backup" "$src"
  FROM="$from" TO="$to" python3 - "$src" <<'PY' || { echo "BAD MUTANT (pattern not found): $label"; bad=1; continue; }
import os, sys
p = sys.argv[1]
s = open(p, newline='').read()
f, t = os.environ['FROM'], os.environ['TO']
if s.count(f) != 1:
    sys.exit(1)
open(p, 'w', newline='').write(s.replace(f, t))
PY
  if run_tests; then echo "SURVIVED: $label"; survived=1; else echo "killed:   $label"; fi
done
cp "$backup" "$src"
[ "$survived" -eq 0 ] && [ "$bad" -eq 0 ]
