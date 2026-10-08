<!--
Copyright The DeviceChain Authors
SPDX-License-Identifier: Apache-2.0
-->

ScriptableObject assets: site geometry, routes, the machine visual catalog and demo presets. Device profiles, alarm states and command schemas are platform data and are not copied here.

## Generated choreography

`quarry_fleet.json` and `quarry_fleet_live.json` are written by `ArtSource/terrain/quarry_fleet.py`
and have the same format (the format is described in that script's header: `tracks[]` of looping
frames, `machines[]` with id, kind, track and offset). Do not edit them by hand.

| File | Mode | Haulers |
| --- | --- | --- |
| `quarry_fleet.json` | Choreographed | Five trucks on the haul loop, a fifth apart (45.103 s on a 225.517 s loop); `SP-HL-0006` makes a scripted visit to the refuel bay. |
| `quarry_fleet_live.json` | Live (`--live`) | Six trucks on the haul loop, a sixth apart (38.651 s on a 231.903 s loop); no scripted refuel visit. The loader's cycle matches the spacing. |

The loop's periods are generated, not chosen: they follow the road lengths, the grade-limited speeds and the stops, so they move whenever
the site or the loop's line does. Do not copy them into code; read them from the file.

Regenerate in this order (each reads the one before): `python3 ArtSource/terrain/quarry_heightmap.py` (terrain, colour maps and
`quarry_features.json`), then `python3 ArtSource/terrain/quarry_fleet.py` and `python3 ArtSource/terrain/quarry_fleet.py --live`. Both
generators write LF line endings on every platform. `python3 ArtSource/terrain/quarry_fleet.py check` builds both fleets in memory and runs
every check without writing (it exits 2 on a defect), and `quarry_fleet.py selftest` shows each site check failing on a site made to break it.
The checks are: no two machines touch, haul trucks keep `HAULER_CLEARANCE` between them, every driving track is clear of what stands by the
reach the task layer plans routes with, the haul loop's driven line is within the grade limit (12 % over 10 m), and each zone has room for a
hauler, a loader and a dozer to stand 1.5 m clear of every machine's sweep, road, prop and pile.
