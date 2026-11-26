<!--
Copyright The DeviceChain Authors
SPDX-License-Identifier: Apache-2.0
-->

# DeviceChain Demo HUD

The story HUD the DeviceChain Unity demos share: a top strip, a top-centre caption box, a chain card, teardrop alarm pins with
their cleared tick, and a three-level end card. The design language and a usage walk-through are in
[`demos/HUD-DESIGN.md`](../../HUD-DESIGN.md).

```
Runtime/Model/   engine-free rules (DeviceChain.Demos.Hud.Model): the theme, the caption box's fade, the chain card's rows and
                 cues, the alarm pin's severity styles, screen-space size and raise/clear timeline, the top strip, the end card
Runtime/Ugui/    the uGUI views (DeviceChain.Demos.Hud): StoryHud and its five pieces, and the shapes drawn in code
Tests/Runtime/   the rules' tests (also run by Dotnet~)
Dotnet~/         compiles Runtime/Model and runs Tests/Runtime with the dotnet SDK
```

The HUD is drawn last, as a screen-space overlay, after the scene's post-processing. Its panels are 94 % opaque (96 % for an end
card, over a darkened frame), and every text colour holds 4.5:1 on a panel over pure white. There are no image or font assets.

```bash
cd Dotnet~ && dotnet restore --locked-mode && dotnet build --no-restore -c Release && dotnet test --no-build -c Release
```
