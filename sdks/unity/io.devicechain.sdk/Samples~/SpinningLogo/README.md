<!--
Copyright The DeviceChain Authors
SPDX-License-Identifier: Apache-2.0
-->

# Spinning Logo — slice-4 smoke test

Proves the package loaded and the render loop is alive, with **no backend and no pre-authored scene
assets**. The component spawns its own cube, camera, and light at runtime.

1. Import this sample (Package Manager ▸ DeviceChain SDK ▸ Samples ▸ Import).
2. Create a new empty scene.
3. Create an empty GameObject (`GameObject ▸ Create Empty`) and add the **Spinning Logo** component
   (`Add Component ▸ Spinning Logo`).
4. Press **Play**.

**Expected:** a blue cube rotates and gently pulses on a dark background.

That's the acceptance gate for the Unity plugin scaffolding (sim-subsystem contract §6). The next
increment drives the pulse from a live event rate and then binds real devices by `externalId`.
