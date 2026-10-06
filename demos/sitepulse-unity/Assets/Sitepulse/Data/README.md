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
| `quarry_fleet.json` | Choreographed | Five trucks on the haul loop, a fifth apart; `SP-HL-0006` makes a scripted visit to the refuel bay. |
| `quarry_fleet_live.json` | Live (`--live`) | Six trucks on the haul loop, a sixth apart (about 37.9 s on a 227.3 s loop); no scripted refuel visit. The loader's cycle matches the spacing. |
