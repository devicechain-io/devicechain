<!--
Copyright The DeviceChain Authors
SPDX-License-Identifier: Apache-2.0
-->

# Sitepulse (Unity)

Sitepulse is a showcase scene for DeviceChain: an earthworks quarry where a fleet of dozers,
wheel loaders and haul trucks report telemetry to a DeviceChain instance and carry out commands
sent from it. The scene shows what the platform observed and what the machine did, side by side,
so a viewer can follow a command from the console to the machine and back.

**Status: early.** This project currently holds the machine models, their rigs and a preview
of the fleet. The quarry terrain comes next, then the connection to a DeviceChain instance
through the [Unity SDK](../../sdks/unity). Nothing here talks to a platform yet.

## Prerequisites

- **Unity 6000.5.3f1** with the Windows Build Support (IL2CPP) module. The project is pinned to
  this version in `ProjectSettings/ProjectVersion.txt`; other versions are untested.
- The Universal Render Pipeline and glTFast packages are declared in `Packages/manifest.json` and
  resolve on first open.
- Windows x64 is the first supported player target.

On Windows, open the project from a Windows path (for example a clone under `C:\`). Opening it
through a WSL share (`\\wsl$\...`) is slow and unreliable.

## Opening the project

Open `demos/sitepulse-unity` from Unity Hub (**Add project from disk**), or from the Unity
command line:

```
unity open <path-to-clone>\demos\sitepulse-unity
```

The first open imports every package and model, which takes a few minutes.

## Layout

```
Assets/Sitepulse/
  Scenes/              scenes
  Scripts/Domain/      machine and task state, free of Unity and transport types
  Scripts/Platform/    the DeviceChain connection
  Scripts/Simulation/  movement, tasks and the simulation clock
  Scripts/Visuals/     machine rigs (FleetRig, MachineRig) and the fleet preview
  Scripts/UI/          heads-up display and presenter controls
  Data/                site, route and preset data
  Art/Models/          the machine models (.glb), generated from ArtSource/
  Art/Materials/       shared materials
  Art/Textures/        textures
  Tests/EditMode/      Edit Mode tests
ArtSource/             the code that builds the machine models (see ArtSource/README.md)
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

## Tests

`Tests/EditMode` checks that each imported model keeps the conventions the rigs rely on: no root
rotation, the machine facing +Z, every driven pivot present and at rest, and that the rig rules
(hydraulic aiming, the hauler's load rule) hold. Run them from **Window > General > Test Runner**,
or from the command line with the Editor closed:

```
unity test <path-to-clone>\demos\sitepulse-unity --mode EditMode
```

## Third-party content

The machine models are original work, built by the code in `ArtSource/`. Any third-party asset
added to the project is recorded, with its source and license, in
[`THIRD_PARTY.md`](THIRD_PARTY.md).
