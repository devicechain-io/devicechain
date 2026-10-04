<!--
Copyright The DeviceChain Authors
SPDX-License-Identifier: Apache-2.0
-->

# Sitepulse (Unity)

Sitepulse is a showcase scene for DeviceChain: an earthworks quarry where a fleet of dozers,
wheel loaders and haul trucks report telemetry to a DeviceChain instance and carry out commands
sent from it. The scene shows what the platform observed and what the machine did, side by side,
so a viewer can follow a command from the console to the machine and back.

**Status: early.** This project holds the machine models and their rigs, the quarry terrain with
its roads, site props and vegetation, and a looping preview of an 18-machine fleet working the
site (`Scenes/Quarry.unity`). The connection to a DeviceChain instance through the
[Unity SDK](../../sdks/unity) comes next. Nothing here talks to a platform yet, and the scene's
look is still being worked on (see [Known limitations](#known-limitations)).

## Prerequisites

- **Unity 6000.5.3f1** with the Windows Build Support (IL2CPP) module. The project is pinned to
  this version in `ProjectSettings/ProjectVersion.txt`; other versions are untested.
- The Universal Render Pipeline and glTFast packages are declared in `Packages/manifest.json` and
  resolve on first open.
- Windows x64 is the first supported player target.

On Windows, open the project from a Windows path (for example a clone under `C:\`). Opening it
through a WSL share (`\\wsl$\...`) is slow and unreliable.

## Opening the project

Open `demos/sitepulse-unity` from Unity Hub (**Add project from disk**), or with the `unity`
command-line tool that ships with Unity Hub:

```
unity open <path-to-clone>\demos\sitepulse-unity
```

The first open imports every package, model and texture, which takes a few minutes. Then run
**Sitepulse > Quarry > Build Prop Prefabs** followed by **Sitepulse > Quarry > Build Scene** to
generate the prefabs and `Scenes/Quarry.unity` (the scene file itself is not committed). Press
Play to watch the fleet preview.

## Layout

```
Assets/Sitepulse/
  Scenes/              scenes
  Scripts/Domain/      machine and task state, free of Unity and transport types
  Scripts/Platform/    the DeviceChain connection
  Scripts/Simulation/  movement, tasks and the simulation clock
  Scripts/Visuals/     machine rigs (FleetRig, MachineRig), the quarry terrain and the fleet preview
  Scripts/Editor/      the scene and prefab builder (Sitepulse > Quarry menu)
  Scripts/UI/          heads-up display and presenter controls
  Data/                site, route and preset data, and the fleet preview choreography
  Art/Models/          the machine models (.glb), generated from ArtSource/
  Art/Models/Props/    site props and vegetation (.glb), generated from ArtSource/
  Art/Terrain/         the quarry heightmap, layer masks and feature list, generated from ArtSource/
  Art/Prefabs/         prop prefabs with LODs, written by the builder
  Art/Materials/       shared materials
  Art/Textures/        textures
  Tests/EditMode/      Edit Mode tests
ArtSource/             the code that builds the models, props and terrain (see ArtSource/README.md)
```

## The machine models

The dozer, loader and hauler are built from code: Blender Python scripts in
[`ArtSource/`](ArtSource) generate each model and a lower-detail version, export them as glTF
binary (`.glb`), and check the rig against its documented limits. Both levels are committed under
`Assets/Sitepulse/Art/Models/`. See [`ArtSource/README.md`](ArtSource/README.md) to rebuild them.

Every moving part is a node at its pivot, at identity rotation when the machine is at rest, so
posing a machine is setting a local angle on a named node. `FleetRig` holds those rules (angle
limits, the dozer blade's counter-rotation, the loader's Z-bar linkage, the hauler's load rule)
and builds a `LODGroup` from the two model files. `MachineRig` drives one machine from a handful
of inputs: implement angles, steering, and the distance it has travelled, from which it spins
wheels and scrolls track treads.

## The quarry

`QuarryTerrain` builds a Unity terrain at scene start from the generated heightmap, layer masks
and feature list in `Art/Terrain/`, so there is no terrain asset to keep in step: change the
generator, re-run it, and the scene follows. The features file also places the roads, the site
buildings and the vegetation. `QuarryFleetPreview` plays the choreography in
`Data/quarry_fleet.json` (loaders loading haul trucks, trucks running the ramp, dozers working
the dump pad) through the same `MachineRig` the platform connection will drive.
`FrameTimeBenchmark` is off unless a player build is started with `-sitepulse-benchmark`; it
flies a camera over the site and writes the average FPS and the 99th-percentile and worst frame
times to `sitepulse-benchmark.txt` beside the executable.

## Known limitations

- The scene has not had its final look pass: materials, lighting and post-processing are
  first-cut, and the terrain's bench geometry is regular.
- There is no processing plant (crusher, conveyors) yet.
- The preview choreography is simple: some machines work in place and haul trucks share a
  narrow two-way ramp.
- Asset `.meta` files for the quarry assets are generated when the project is first opened.

## Tests

`Tests/EditMode` checks that each imported model keeps the conventions the rigs rely on: no root
rotation, the machine facing +Z, every driven pivot present and at rest, and that the rig rules
(hydraulic aiming, the hauler's load rule) hold. It also checks the quarry data: the heightmap
decoder round-trips and rejects a truncated file, and the fleet choreography holds all 18
machines with tracks of the right length. (`ArtSource/terrain/quarry_fleet.py` fails if any two machines' footprints touch.) Run them from **Window > General > Test Runner**,
or from the command line with the Editor closed:

```
unity test <path-to-clone>\demos\sitepulse-unity --mode EditMode
```

## Third-party content

The machine models, props and terrain are original work, built by the code in `ArtSource/`. The
only third-party content is four CC0 terrain materials from ambientCG; each is recorded, with its
source and license, in [`THIRD_PARTY.md`](THIRD_PARTY.md). Anything added later must be CC0 or
equally permissive and be recorded there.
