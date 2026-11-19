<!--
Copyright The DeviceChain Authors
SPDX-License-Identifier: Apache-2.0
-->

# Harborpulse (Unity)

A 3D scene of the `harborpulse` simulator's waterfront: the fictional Port Alder harbour district, its tide
gauges, street lights, pump stations, EV chargers, two metered buildings and the flood-response truck, through
the scenario's compressed day with its storm and king tide.

**Status: milestone U0.** The project, the scene skeleton and the first live binding:

- the waterfront is laid out in code from the manifest's own coordinates (`Assets/Harborpulse/Resources/harbor_layout.json`);
- a time-of-day sky (sun and moon on arcs, graded dawn, day, dusk and night, stars, a storm overcast) driven by the
  runner's scenario clock;
- a first pass of the harbour water: a custom URP shader with Gerstner waves, depth colour, foam and rain, whose
  height on each quay is that quay's observed tide;
- live, read-only binding of the tide gauges and the street lights through the DeviceChain C# SDK.

Pumps, alarms, commands and the truck's movements come in U1; the full art pass in U5.

## What it shows, and where each thing comes from

| On screen | Source |
| --- | --- |
| Where the street lights stand | the manifest's surveyed positions, 430 m apart, not rescaled |
| Where every other device stands | the scene's own placement along its quay; the manifest gives them no position |
| The time of day, the sun, the stars, the storm | the runner's `GET /status` (`scenario.startedAt`, `day`, `simHour`), and the storm beat's hours |
| The water's height on each quay | the median of that quay's tide gauges' `water_level_m`, as the platform reports them, fresh readings only |
| The waves, foam and rain | visual only, zero-mean; they follow the storm and never move the tide |
| Each street light | the platform's last `lamp_on` and `dim_pct` for it: on, off, silent (nothing for longer than the platform's 45 s silence window) or unknown |

The scene never writes to the platform. It creates no device credential, opens no device session and sends
no command; everything comes from queries and subscriptions under the runner's operator token.

When something is not known, the scene says so instead of drawing a guess: a quay with no gauge reading draws its
water at datum and the readout says "no reading"; a quay whose gauges went quiet holds its last level and says
"held"; a stopped runner, or one that stops answering, freezes the clock and the banner turns to STALE CLOCK.

## Run it

### Offline preview (no instance)

Open the project in Unity 6000.5.3f1, open `Assets/Harborpulse/Scenes/Harborpulse.unity`, tick **Offline
Preview** on the `Harborpulse` object (or set `HARBORPULSE_OFFLINE=1` before starting the Editor, or pass
`-hp-offline` to a player) and press Play. The banner reads OFFLINE PREVIEW: the gauges and lights are a
stand-in that follows the story's shape, not platform data. `-hp-start-hour 18.5` and `-hp-speed 4` move and
speed up the preview's day.

### Live, against a local instance running the harborpulse sim

1. Stage the C# SDK into the Unity package once (the DLLs are build output and are not committed):

   ```bash
   sdks/unity/stage-sdk.sh
   ```

2. Bring up an instance and the sim, for example:

   ```bash
   dcctl install local && dcctl bootstrap local
   dcctl sim create hp --manifest harborpulse
   dc-simulator --handshake <the path sim create printed>     # serves http://localhost:8090
   ```

   The scene reads only what a runner already serves (`/config.json`, `/status`), so any harborpulse runner
   works, including one started by `hack/harborpulse-rig.sh`.

3. Open the project and the scene, leave **Offline Preview** unticked and press Play. The runner address is
   `-dc-runner <url>`, else `HARBORPULSE_RUNNER`, else the component's **Runner Url** (`http://localhost:8090`).
   `-hp-expect-tenant <tenant>` refuses a runner serving another tenant.

The banner turns LIVE with the tenant and instance once `/status` answers; the platform line counts the bound
devices (37 of 37) and the measurement and alarm streams. Within a few seconds each quay's tide line shows
its gauges' median and how many contributed, and the lights switch with the platform's reports (off by day,
on from 18:00, and East Quay's go silent through its feeder outage, 19:45 to 21:15).

Keys: `0` overview, `1`-`3` the quays, `4` an East Quay gauge, `5` the promenade. Drag to orbit, wheel to dolly.

## Layout

```
Assets/Harborpulse/
  Resources/harbor_layout.json   generated from the simulator's manifest (see below)
  Scenes/Harborpulse.unity       a camera, a volume and HarborpulseApp; everything else is built in code
  Shaders/                       HarborWater.shader, HarborSky.shader, HarborWaves.hlsl
  Scripts/Model/                 engine-free: layout, site plan, scenario clock, sky, waves, tide and lamp boards
  Scripts/Live/                  the read-only platform link: runner config, /status, the shared observer
  Scripts/Visuals/               the world builder, sky, water, camera rig and the composition root
  Scripts/Editor/                Harborpulse > Build Scene, Harborpulse > Render Preview Stills
  Tests/EditMode/                the model's tests (also run by Dotnet~)
Dotnet~/                         compiles Scripts/Model and runs Tests/EditMode with the dotnet SDK
```

There are no model, texture or image files: every mesh and material is made by `Scripts/Visuals` when the scene
starts.

### The layout file

`harbor_layout.json` is the manifest's district: every device with its external ID, type and quay, the street
lights' surveyed positions, the depot, the geofences, the story's beats and the flood levels. The simulator's
`TestHarborpulseSceneLayoutFixture` holds it equal to what the manifest produces, so a change to the scenario
that moves a device fails that test until the file is regenerated:

```bash
cd backend/sims/dc-simulator
HARBORPULSE_UPDATE_SCENE=1 go test ./sim -run TestHarborpulseSceneLayoutFixture -count=1
```

## Checks

```bash
# the engine-free model and its tests (CI job harborpulse-model)
cd demos/harborpulse-unity/Dotnet~ && dotnet restore --locked-mode && dotnet build --no-restore -c Release && dotnet test --no-build -c Release

# the model, the live link and the visuals against the UnityEngine reference assemblies (CI job unity-compile-check)
sdks/unity/tools/UnityCompileCheck/compile-check.sh
```

The shaders, the Editor tools and the EditMode suite inside Unity need the Editor:

```bash
Unity -batchmode -projectPath demos/harborpulse-unity -runTests -testPlatform EditMode -testResults results.xml
Unity -batchmode -projectPath demos/harborpulse-unity \
  -executeMethod DeviceChain.Harborpulse.EditorTools.HarborStills.ShootFromCommandLine -hp-stills-dir Stills
```

The second renders look-check stills of the offline preview (morning, midday, dusk, the storm surge, night).
They are stills of the preview's stand-in values and must not be captioned as a live run.
