<!--
Copyright The DeviceChain Authors
SPDX-License-Identifier: Apache-2.0
-->

# Harborpulse (Unity)

A 3D scene of the `harborpulse` simulator's waterfront: the fictional Port Alder harbour district, its tide
gauges, street lights, pump stations, EV chargers, two metered buildings and the flood-response truck, through
the scenario's compressed day with its storm and king tide.

**Status: U1 scene and evidence views.** The waterfront builds itself in code from the manifest's surveyed
layout. It includes stone quays and jetties, an enclosed Basin with a ferry berth, cut-away pump wells,
gauge variants, chargers and parked hatchbacks, lamps, the utility truck, metered buildings and the town.
Named rig nodes keep geometry refinements separate from the evidence views.

The scenario clock drives the sky and runtime URP grading. Observed gauge medians drive regional tide,
planar reflections and overtopping; observed tide rise and the WORLD storm drive seawall spray. The water
has Gerstner waves, depth colour, foam and rain. Wet wells use tiny ripples with shared lighting and no reflection.
Their floor is independent of harbour datum: `wet_well_m` spans 0-4 m above the well floor. Fixed 0.8 m,
2.5 m and 3.2 m markers stay separate from a reported `well_alarm_m` attribute.

Pump, charger, gauge, lamp and truck views apply one detached model snapshot per frame. Cards show
observed values and ages, command states actually seen, start evidence, OTA outcome words, replacement
readings and platform suppression evidence. Beacon identities survive severity changes; acknowledged
alarms stay steady and a cleared alarm briefly shows a green tick. Missing readings remain missing.
WORLD feeder, OTA and crew cues use cool white and blue-grey styling. Amber identifies platform alarms,
coral identifies failures, green identifies confirmations and blue identifies flow.

## What it shows, and where each thing comes from

| On screen | Source |
| --- | --- |
| Where the street lights stand | the manifest's surveyed positions, 430 m apart, not rescaled |
| Where every other device stands | the scene's own placement along its quay; the manifest gives them no position |
| The time of day, the sun, the stars, the storm | the runner's `GET /status` (`scenario.startedAt`, `day`, `simHour`), and the storm beat's hours |
| The water's height on each quay | the median of that quay's tide gauges' `water_level_m`, as the platform reports them, fresh readings only |
| The waves, foam and rain | visual only, zero-mean; they follow the WORLD storm and never move the tide |
| Pump water, current needle, impeller and outfall | observed `wet_well_m`, `pump_current_a`, `pump_running` and `flow_lps`; rotation and streak rate are visual mappings |
| Alarm rings and counters | observed alarms, by device and alarm key, including acknowledgement and clear |
| Charger screen, cable and car | observed power/session and OTA evidence; connector glow is a visual mapping of reported temperature |
| Truck placement and heading | observed WGS84 fixes, interpolated visually; no fix means depot placement with a "no fix" label |
| Feeder fades, maintenance rings and crew | runner beats and declared plans, WORLD only |
| Lamp suppression ring and service card | an observed platform suppression window targeting that lamp |
| Replacement plate | runner replacement declaration; the card separately counts readings observed afterwards on the bound device |
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

Keys: `0` overview, `1`-`3` the quays, `4` an East Quay gauge, `5` the promenade, `6` North Quay pump,
`7` Basin pump, `8` North Quay chargers, `9` North Quay gauge 3, `F1` East lamp 3, `F2` the following truck shot.
Drag to orbit, wheel to dolly. Shot indices for batch stills are 0-11 (`F1` = 10, `F2` = 11).

## Layout

```
Assets/Harborpulse/
  Resources/harbor_layout.json   generated from the simulator's manifest (see below)
  Scenes/Harborpulse.unity       a camera, a volume and HarborpulseApp; everything else is built in code
  Shaders/                       harbour water, reflection-free wet-well water, sky and shared wave/lighting HLSL
  Scripts/Model/                 engine-free snapshot, evidence reducers, tide, sky, WORLD cues and visual mappings
  Scripts/Live/                  the read-only platform link: runner config, /status, the shared observer
  Scripts/Visuals/               composition, camera, sky, water, runtime grading and planar reflection
    Assets/                     procedural builders, shared named rigs and WORLD crew props
    Views/                      snapshot views, persistent beacons and evidence cards
  Scripts/Editor/                Harborpulse > Build Scene, Harborpulse > Render Preview Stills
  Tests/EditMode/                the model's tests (also run by Dotnet~)
Dotnet~/                         compiles Scripts/Model and runs Tests/EditMode with the dotnet SDK
```

There are no model, texture or image files: every mesh and material is made by `Scripts/Visuals` when the scene
starts.

### The layout file

`harbor_layout.json` is the manifest's district: every device with its external ID, type and quay, the street
lights' surveyed positions, the depot, the geofences, the story's beats and the flood levels. It also carries
the scenario's planned cues (`cues`: sim hours ending `Sim`, wall seconds ending `S`), the truck's route
(`truckRoute`, depot first) and a `controlFrame` rectangle around every position in the file. The cues are hints
for a director's timing, never evidence. The flood-plain fence and the depot stand behind East Quay, inland of
its lights, in the manifest's own coordinates. The simulator's `/status` reports `scenario.layoutDigest`, the
SHA-256 of the layout it generates, so a scene can tell it was built for another layout. The simulator's
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
  -executeMethod DeviceChain.Harborpulse.EditorTools.HarborStills.ShootFromCommandLine -hp-stills-dir Stills -hp-stills-width 1600 -hp-stills-height 900
```

The default set renders eleven U1 look checks: day overview/reflection, Basin pump before and after a
platform-evidenced start, North Quay cut-away hero, East Quay night surge and outage, next-night charger
OTA with a failed target, gauge replacement, lamp service, promenade reflections and the truck. These
are preview stand-ins and must not be captioned as live data. Every PNG keeps a burnt-in OFFLINE PREVIEW
stamp and the same evidence card text as IMGUI (`Camera.Render()` does not capture IMGUI itself).

`-hp-stills-width` and `-hp-stills-height` default to 1600x900. Override the set with, for example,
`-hp-stills "17.95:7,18.35:7,26.7:8"`; hours above 24 select subsequent days. Files use
`harborpulse-<hh>h<mm>-<shot-slug>.png`. The stills path observes a forward lead-in at scenario speed,
then holds the target to settle fades and cleared ticks. It explicitly renders the planar pass before
the scene camera, using the same runtime grading as play mode.

The planar pass excludes layer 30 (assigned in code only to the harbour surface), clips below the median
observed water plane, and clears alpha to zero for the sky fallback. PC uses half resolution; an active
pipeline asset whose name contains `Laptop` uses quarter resolution and updates every other play frame.
The reflection camera has no post-processing or shadows. Runtime materials, meshes, camera, texture and
grading profile are released by `Teardown()`.

### Read-only requests

All platform requests use the runner's operator authority. The scene sends these reads only:

| Area | Reads |
| --- | --- |
| Runner HTTP | `GET /config.json` (including refresh), `GET /status` |
| Device management | `devicesByExternalId` with device type/profile metric and command definitions for binding; `alarms`; `alarmStream` subscription |
| Device state | `latestMeasurements` initial snapshots; `latestLocations` with latitude/longitude, heading and speed each second; `deviceStatesByDeviceToken` presence every five seconds |
| Event management | `measurementStream` subscription |
| Command delivery | shared observer `commands` and `commandsByToken` every second; harborpulse also reads the newest ten commands per bound pump in one aliased query every two seconds, including terminal records and their platform timestamps |
| Device management supplements | `alarmSuppressionWindowsByToken` for declared lamp jobs and numeric SHARED `entityAttributes` for each pump's `well_alarm_m`, every five seconds |
| Update management | `updateRollout` and bounded `updateRolloutTargets` every two seconds after a runner rollout declaration |

The shared observer also checks the active-alarm snapshot every five seconds to detect stream gaps.
There are no credential queries, mutations, device sessions or command submissions.

The portable compile checker accepts `--no-restore` when projects have already been restored. For a
supplemental C# check of URP and Editor code without launching Unity, point it at an installed Editor
and the project's already imported assemblies:

```bash
sdks/unity/tools/UnityCompileCheck/compile-check.sh --no-restore \
  -p:UnityManagedDir="/path/to/Editor/Data/Managed" \
  -p:HarborUrpAssembliesDir="/path/to/harborpulse-unity/Library/ScriptAssemblies" \
  -p:CompileHarborEditor=true
```

This checks C# API compatibility; shader compilation and visual inspection still require Unity.

## U1 snapshot contract

`Model/` has no Unity dependency. Both `PreviewSource` and `HarborLiveSession.Source` implement
`IHarborSource`. After `live.Tick()` drains the shared observer and advances the read-only polls, build
one detached snapshot per frame:

```csharp
var model = new HarborObservedModel(layout); // keep across frames, one instance per runner run
// In Update, on the main thread:
live.Tick();
HarborFrame frame = model.Update(live.Source, live.Clock.Read(now), now);
```

`HarborFrame` holds WORLD cues separately from PLATFORM evidence: quay levels, lamp states, pump
presentations, charger boards, readings (including meter metrics), beacons and per-device alarm counts,
platform command records and observed-status history, truck fix, lamp-service evidence and replacement evidence. Collections are
read-only, and later updates never change an earlier frame. Missing values remain nullable. Every
measurement retains its occurred instant; command evidence retains nullable platform timestamps and its observation instant, and OTA evidence retains its observation instant.
`ReadProblem` reports supplementary-read failures alongside the last evidence and its age.

Use `StoryCues` for WORLD styling only. Runner `beats` take precedence even when the list is empty;
absent beats fall back to the committed layout with an explicit note. Job and replacement instants use
`ScenarioStatus.ToSimSeconds`, the inverse of the verifier's wall-time formula. The OTA night is the day
after the opening day, at `scheduleHour`, for one sim hour. With no S5 OTA declaration there is no OTA
cue. The replacement crew's visual window lasts half a sim hour. These are runner plans, never platform
completion claims. Unknown S5 members are discarded; no credential fields or raw status JSON are retained.

Beacon identity is `(external ID, alarm key)`, so severity changes resize the same beacon. A clear holds
a green tick for two wall seconds from its first observation by the model. Acknowledgement removes the
pulse. Alarm counts exclude cleared ticks. Pump command history shows QUEUED at `queuedTime`, SENT at
`sentTime`, and terminal results at `respondedTime`, only when those platform timestamps exist, plus
statuses actually seen by the observer. Cards label platform records with their timestamps and ages,
show an observed status the platform also recorded once (as the record), and list the newest six rows with
a count of the earlier rows not shown; there is no animation of missed transitions. PLATFORM start evidence compares a successful start-pump's
`respondedTime` with the first running=1 reading after an observed running=0: the response must be at
or before that reading's `occurredTime` and after the off interval began. Previous-cycle successes do
not qualify. A scene that joined while running says UNCONFIRMED (joined while running).
FLOAT-CONSISTENT additionally requires a contemporaneous well at the float and positive coverage from
both the shared observer and the pump-history poll across the transition. Missing coverage stays UNCONFIRMED.
The fixed 2.5 m high marker, 3.2 m float and 0.8 m cut-off are separate from the reported `well_alarm_m`.
Impeller speed and connector glow are explicitly visual mappings.

Live supplementary reads are query constants in `HarborReadQueries` (guarded by tests). Device management
reads the lamp job's suppression window and each bound pump's numeric SHARED `well_alarm_m` every five
seconds. Update management uses a raw bearer-authenticated GraphQL POST every two seconds once the
runner supplies a rollout token, with one request in flight and one retry after a 401 refresh. It preserves
the platform's rollout outcome and attempt-state words independently. Target pages are bounded at 1000;
truncation is reported. There is no SDK change or mutation.

The composition root uses `live.Clock` and `live.Source.Status`, with one status poll. It reconnects and rebinds after a runner restart, discarding the old observer, and creates a new model
before each preview seek, so accumulated evidence cannot leak between runs. `WaterMotion` derives the reflection median, spray mapping and overtopping strength in the
engine-free model; the views only apply those values.

The OFFLINE PREVIEW remains a deterministic stand-in, **not platform data**. It now supplies the pump
sequence (including a North Quay float-consistent case), East Quay flood escalation and outage alarms,
lamp service, charger heat/derate and next-night OTA, truck route and gauge replacement. Every existing
preview banner and still stamp remains in place.
