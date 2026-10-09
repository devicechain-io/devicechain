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
of the four files it was made from, a table of the outlines a lane or stand is deliberately laid out between, and the thresholds it was made with
(its `rules`, and each lane's `neighbour_span_m`), so that a reader takes every number from the file. Nothing reads it while the demo runs: it is
checked, and read back by the EditMode tests (`Scripts/App/SiteTopologyReader.cs`, `Scripts/Tasks/Traffic`).

```bash
python3 ArtSource/terrain/quarry_topology.py             # write it (run after the fleets)
python3 ArtSource/terrain/quarry_topology.py check       # build it, run the checks on it and on the committed file
python3 ArtSource/terrain/quarry_topology.py selftest    # each check failing on a site made to break it
```

How two cells conflict. A lane is cut into 2 m cells along the line a machine drives; the ground a cell sweeps is a hauler posed along it (a pose at
most 0.25 m and 1 degree from the last, at the lane's tangent heading, not the cell's chord) and the hull of each box over each pair of poses; on the
loop it is the track's own frames. Two cells of different lanes conflict when their sweeps come nearer than 1.0 m (both lanes are haul lanes: the
loop and the roads, drawn a metre of air apart) or 0.3 m (either lane is the bay's, whose turns cannot keep a metre). Two cells of one lane, or of
one stand's way through it, within a slot (12.8 m, `neighbour_span_m`) of each other along it are a machine and its follower; further apart than
that they conflict only if they overlap, which is a lane folding back over itself (the loop does it at the ramp bottom, near (-22, 15); the pad's
trucks do it in the pad, which the pad's station holds). The distance is the Euclidean one between the swept polygons. A hull covers a little more
than a turning box sweeps, so a gap reads at most 0.03 m below the gap to the continuous sweep (the worst of 1200 pairs nearest a threshold read
0.022 m low) and never above it. The nearest pair that does not conflict is 1.004 m (haul), 0.526 m (bay) and 0.056 m (overlap, in the pad).

The checks: the cycle (the loop and the detour through the refuel bay) holds every machine outside the stations' cores (a slot is a hauler's length
and the air haul trucks keep, 12.8 m, however long a cell is), the span a box takes on the loop leaves room for everyone else, every stand keeps
1.5 m from every lane, sweep, box and outline and is driven through (its in and out lanes never overlap along the way through), every station is
safe at every headway from the design spacing up, every conflict lies in one box (or is a stand's own way through, a station, or a diverge), every
lane pose is within the grade limit and 3.2 m clear of every outline (the task layer's own reach for a driving hauler; but the bay's own), the
topology was made from the committed files, every zone has a zone stand for each kind it accepts, and no lane or stand comes within 1.5 m of
another machine's work area. The refuel queue and bay are service stands: they do not make the yard a place to send a machine to. No zone has a
stand yet: the places the choreography leaves room to stand are not places a lane can reach and leave by those rules, so each zone lists no kinds.
