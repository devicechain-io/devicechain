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
scripts="$here/../Assets/Towerpulse/Scripts"
# every file a mutant touches is backed up whole and restored on exit
files=(Model/BuildingModel.cs Plane/CommandEngine.cs Plane/Readings.cs Plane/Vocabulary.cs Story/CommandCard.cs Story/TintRamp.cs Story/MotionLook.cs Story/ShotFile.cs)
backups="$(mktemp -d)"
for f in "${files[@]}"; do mkdir -p "$backups/$(dirname "$f")"; cp "$scripts/$f" "$backups/$f"; done
restore() { for f in "${files[@]}"; do cp "$backups/$f" "$scripts/$f"; done; }
trap 'restore; rm -rf "$backups"' EXIT

run_tests() { (cd "$here/Tests" && dotnet test -c Release --nologo -v q >/dev/null 2>&1); }

if ! run_tests; then echo "ABORT: the baseline is not green; no mutant means anything yet" >&2; exit 2; fi

survived=0; bad=0
# file | label | exact text in that file | replacement
mutants=(
  "Model/BuildingModel.cs|completion: always complete|if (z.TempC <= z.SetpointC + SetpointToleranceC) {|if (true) {"
  "Model/BuildingModel.cs|completion: complete while still hot|if (z.TempC <= z.SetpointC + SetpointToleranceC) {|if (z.TempC >= z.SetpointC + SetpointToleranceC) {"
  "Model/BuildingModel.cs|completion: never complete|if (z.TempC <= z.SetpointC + SetpointToleranceC) {|if (false) {"
  "Model/BuildingModel.cs|expiry: never expire|else if (t - b.IssuedAtS >= b.WindowS) {|else if (false) {"
  "Model/BuildingModel.cs|solar: no gain|return z.GlassM2 * PeakSolarWPerM2 * facing * elevation * clear;|return 0.0;"
  "Model/BuildingModel.cs|solar: ignore the facade|return z.GlassM2 * PeakSolarWPerM2 * facing * elevation * clear;|return z.GlassM2 * PeakSolarWPerM2 * elevation * clear;"
  "Model/BuildingModel.cs|solar: sun cools|if (facing < 0) facing = 0;|"
  "Model/BuildingModel.cs|restart: complete when merely running|c.Running && c.ChwSupplyC <= CompleteChwC|c.Running"
  "Model/BuildingModel.cs|boost: damper not forced open|if (z.BoostOverride) cmd = 100.0;|if (false) cmd = 100.0;"
  "Plane/CommandEngine.cs|plane: answer a command on arrival, before its ticket is final|if (!p.Ticket.IsFinal) continue;|"
  "Plane/CommandEngine.cs|plane: a redelivered token actuates again|if (!string.IsNullOrEmpty(commandToken) && byToken.TryGetValue(commandToken, out var seen))|if (false && byToken.TryGetValue(commandToken, out var seen))"
  "Plane/Vocabulary.cs|plane: setpoint ceiling wider than the profile|SetpointMax = 28;|SetpointMax = 35;"
  "Plane/CommandEngine.cs|plane: a fractional boost accepted|whole && v != Math.Floor(v)|false && v != Math.Floor(v)"
  "Plane/Readings.cs|plane: a tripped chiller keeps reporting|if (!c.Online) return null;|"
  "Story/CommandCard.cs|card: delivered counts as done|public bool Done { get { return Outcome == CardOutcome.Successful; } }|public bool Done { get { return Delivered; } }"
  "Story/CommandCard.cs|card: a stage reached again is overwritten|if (has[i]) return;|"
  "Story/CommandCard.cs|card: a later word rewrites the terminal one|if (Terminal) return;|"
  "Story/TintRamp.cs|tint: the cool side scaled like the warm side|double w = dev < 0 ? dev / CoolSpanC : dev / WarmSpanC;|double w = dev / WarmSpanC;"
  "Story/MotionLook.cs|lamp: silence drawn as a stop|if (reading == null) return LampState.NotReporting;|if (reading == null) return LampState.Stopped;"
  "Story/MotionLook.cs|fans: a stopped chiller still spins|return running ? ChillerDegPerS : 0;|return ChillerDegPerS;"
  "Story/MotionLook.cs|label: real time reads as accelerated|modelSpeed <= 1.0000001) return null;|modelSpeed < 0.5) return null;"
  "Story/ShotFile.cs|shots: a misspelled key is ignored|foreach (var k in o.Keys) if (!known.Contains(k))|foreach (var k in o.Keys) if (false)"
  "Story/ShotFile.cs|shots: overlapping shots are accepted|if (shot.StartS < prev.EndS - 1e-9)|if (false)"
)
for m in "${mutants[@]}"; do
  IFS='|' read -r file label from to <<<"$m"
  restore
  src="$scripts/$file"
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
restore
[ "$survived" -eq 0 ] && [ "$bad" -eq 0 ]
