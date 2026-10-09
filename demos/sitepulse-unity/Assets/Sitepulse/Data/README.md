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

## Site topology

`quarry_topology.json` is written by `ArtSource/terrain/quarry_topology.py`, after the fleets (it needs their sweeps), and describes the site
as an interlocking would need it: the haul loop and the other lane of each wide road as directed lanes cut into 2 m cells, the junction boxes
where lanes conflict, the refuel bay's queue and bay stands with their lanes, and the load point and the dump pad as timed stations (core, exit
buffer, capacity, window, headway, and the loader the load point is coupled to). It also records the ground every other machine works, the SHA-256
of the four files it was made from, and a table of the outlines a lane or stand is deliberately laid out between. Nothing reads it while the demo
runs: it is checked, and read back by the EditMode tests (`Scripts/App/SiteTopologyReader.cs`, `Scripts/Tasks/Traffic`).

```bash
python3 ArtSource/terrain/quarry_topology.py             # write it (run after the fleets)
python3 ArtSource/terrain/quarry_topology.py check       # build it, run the checks on it and on the committed file
python3 ArtSource/terrain/quarry_topology.py selftest    # each check failing on a site made to break it
```

The checks: the cycle holds every machine outside the stations' cores (a slot is a hauler's length and the air haul trucks keep, 12.8 m, however
long a cell is), the span a box takes on the loop leaves room for everyone else, every stand keeps 1.5 m from every lane, sweep, box and outline and is
driven through, every station is safe at every headway from the design spacing up, every conflict between two lanes lies in one box, every lane is
within the grade limit and 3.2 m clear of every outline (but the bay's own), the topology was made from the committed files, every zone has a stand
for each kind it accepts, and no lane or stand comes within 1.5 m of another machine's work area. No zone has a stand yet: the places the
choreography leaves room to stand are not places a lane can reach and leave by those rules, so each zone lists no kinds.
