<!--
Copyright The DeviceChain Authors
SPDX-License-Identifier: Apache-2.0
-->

# Harborpulse (Unity)

A 3D scene of the `harborpulse` simulator's waterfront: the fictional Port Alder harbour district, its tide
gauges, street lights, pump stations, EV chargers, two metered buildings and the flood-response truck, through
the scenario's compressed day with its storm and king tide.

**Status: U2b scene integration; Editor rendering and live observer wiring remain to verify.** The scene builds itself in code from `PresentationMap` and the committed generated manifest layout. It includes stone quays and jetties, an enclosed Basin with a ferry berth, cut-away pump wells,
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
| Where devices stand | compact `PresentationMap` anchors; the engineer overlay discloses the manifest positions |
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
devices (43 of 43) and the measurement and alarm streams. Within a few seconds each quay's tide line shows
its gauges' median and how many contributed, and the lights switch with the platform's reports (off by day,
on from 18:00, and East Quay's go silent through its feeder outage, 19:45 to 21:15).

With the director off (`D`), presets: `0` overview, `1`-`3` the quays, `4` an East Quay gauge, `5` the promenade, `6` North Quay pump,
`7` Basin pump, `8` North Quay chargers, `9` North Quay gauge 3, `F1` East lamp 3, `F2` the following truck shot, `F3` the night sky
over the basin. Drag to orbit, wheel to dolly. Shot indices for batch stills are 0-12 (`F1` = 10, `F2` = 11, `F3` = 12).

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
| Command delivery | shared observer `commands` and `commandsByToken` every second; harborpulse also reads the newest ten commands per bound pump in one aliased query every two seconds, including terminal records and their platform timestamps; the director's command read selects each command's recorded `cause` |
| Device management supplements | `alarmSuppressionWindowsByToken` for declared lamp jobs and numeric SHARED `entityAttributes` for each pump's `well_alarm_m`, every five seconds |
| Update management | `updateRollout` and bounded `updateRolloutTargets` every two seconds after a runner rollout declaration |

The shared observer also checks the active-alarm snapshot every five seconds to detect stream gaps.
U2b also reads notification attempts by alarm token, alarm suppression stamps/status, the replacement
journal and retired credentials' tokens and `enabled` flags, artifact trust/signers, rollout targets and
assignment attempts, quay `anchorDevices`, and bounded measurement history for replacement and restore.
Credential reads select no secret or credential material. There are no mutations, device sessions or command submissions.

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

## U2a director model

`Scripts/Model/Director/` adds the compact presentation map, strict storyboard, evidence predicates,
deterministic live scheduler, free-roam ownership model and NDJSON recorder. This slice supplies model
APIs and offline tests. U2b wires the Unity scene to them; the observer is supplied separately through `IDirectorFeed`.

- `PresentationMap` owns manifest-to-presentation conversion. Its constrained mesh splits the manifest
  frame on each control coordinate, with Steiner vertices at intersections, and triangulates every
  resulting rectangle. Fence edges are chains of triangle edges. The authored strip maps are monotone;
  `Validate()` reports inverted triangles. Non-rectilinear fence constraints fail explicitly. Device
  anchors, quay polygons, water, breakwaters, road and named camera poses are exposed in metres.
  Pass recorded platform area membership to apply M2 and inspect `ReadinessWarnings` before directing.
- `Storyboard.Parse(json, map)` loads `Resources/storyboard.json`. Caption alternatives share a slot;
  the first satisfied claim wins, then pre/scenario/fallback text. Captions retain authored line breaks.
  Cut segments have absolute recorded wall spans, independent move durations, caption slots and allowed
  chain nodes. The short cut excludes the flood rail as well as the extra pages.
- `Observation` identities include run, kind, device, alarm key/cycle, command, attempt, status and
  source. The read-only observer adapter must supply real record identities and exact platform states.
  Measurement identities include their occurred instant. Derived adapter fields such as `afterFailure`
  and `afterReplacement` must be established from the named records, never from scenario cues.
  `$trigger.cycle` binds proof to the active alarm cycle. Numeric caption placeholders bind to explicitly
  declared evidence fields. A scenario ledger supplies an expected set, never proof of platform storage.
- Positive coverage and gaps both go into `ObservationStore`. Exact-set and absence predicates require
  positive coverage over the whole interval and reject any overlapping gap. A reconnect snapshot retains
  the original event identities, so the scheduler's per-run consumed set prevents a second trigger.
- Call `Scheduler.Tick` with an absolute sim hour (26 means the following night's 02:00), monotonic
  seconds, clock freshness, digest agreement and defects. Its returned events carry page states, shots,
  caption text and evidence keys. A stale clock freezes decisions; digest disagreement turns direction
  off. `FreeRoam` separately reports a continuous director blend weight and remains available then.
- Supply four caller-owned writers to `Recorder`: observed, world, director and checkpoints. Call
  `Observe`, `World`, `Decision`, `Coverage`, and `Consumption(scheduler.Consumed)` as inputs arrive, and
  `Advance` every frame, including idle frames. Checkpoints fall on exact 60-second recording boundaries.
  `RecordingReader.Seek` reconstructs the observed state and recorded decisions; `SeekCut` maps show
  time to the recording and silently consumes skipped events. It does not rerun a live scheduler or
  invent missing observations. `RecordedState.Hash()` is stable across dictionary and token-set order.

Authoring resolutions: the source timing table and individual shots disagree in several places. The
file keeps page budgets at exactly 90 and 300 seconds, and gives every caption at least three seconds.
The short cold open therefore uses two seconds of storm plus a three-second title; the short well
entry uses a two-second move plus a three-second caption. Restore shots sum to 32 seconds and OTA
shots to 38 seconds in the longer cut. The platform-verb fallbacks “The fault is detected.” and
“Silence detected.” are gated claims. “A firmware update” is treated as a noun phrase by the lint.
The opening recording disclosure and the end-card sub-line and position disclosure are scheduled
as separate overlays in both cuts. The c90 OTA card carries the bound terminal totals and concurrency
limit. Each segment explicitly carries its shared home seam pose and half-second dissolve.

Windows verification with cached NuGet packages (redirects every project's intermediate/output files
under this demo, and avoids inaccessible user SDK/config directories):

```powershell
cd demos/harborpulse-unity/Dotnet~
powershell -NoProfile -ExecutionPolicy Bypass -File Verify-Director.ps1
powershell -NoProfile -ExecutionPolicy Bypass -File Verify-Director.ps1 -CompileCheck
python verify_director_mutants.py
```

The mutation runner changes one production expression at a time, requires a named failing NUnit test
(compiler errors do not count), and restores the original bytes in `finally`. Logs, TRX results and the
mutation summary are in ignored `Dotnet~/.build/`.

U2a review corrections: the latitude strips now cover the southern control frame and keep depot-to-plain
travel northbound. The metre grid spans the plain, depot and route plus ten metres. Observation history
retains refreshed snapshots; `mode: occurred` selects an immutable event, while `mode: current` first
reduces snapshots by entity/cycle before testing status and freshness. Event consumption never depends
on observation time. Named conjunction records (`as`, `$match`) and trigger references join notification,
OTA, replacement and clear proof. Missing set fields return an explicit evidence error; empty platform
sets cannot prove storage, and the six-device claim requires cardinality six.

Caption opportunities survive provisional text and shot changes until their authored hold deadline.
Coverage is fed incrementally during playback; an open-window absence statement uses `throughNow`,
while a completed-window statement requires its entire interval. Truck page five has an explicit
missing-trigger skip policy. The control fixtures compare complete emitted output deltas against the
clean recording, including caption text/endings, evidence identities, rail labels/links, shots, skips
and wall times. Refresh fixtures only with `HARBORPULSE_UPDATE_FIXTURES=1`, then review every delta.
Layout cue references resolve silence and air-lock wall offsets using the actual day length; the
same timeline tests run at 1,440 and 2,880 seconds per day.

The mutation verifier first requires a green complete baseline and confirms every selected test ran.
Each mutant receives a unique results directory. A compile failure or absent selected test cannot
count as a kill. Source restoration is followed by a fresh complete build/test, even on failure.
SDK and package selection use the caller's environment rather than an account-specific path.

## U2b Unity integration

The scene draws the horseshoe from `PresentationMap`: south-facing North Quay, west-facing East Quay,
east-facing Basin, mapped truck road and fence rings, depot and breakwater lights. U3 replaced the town terraces and ridge (see below).
Device roots retain the U1 node contract and evidence views, with geometry oriented in quay-local frames.
Water uses `TideField` rectangle distances on CPU and HLSL, with the exact zero-distance limit on a quay
and identity vertical metres. Missing readings still use the U1 held value or datum. The continuity grid
test covers a 0.30 m regional spread; a universal 1 cm bound for arbitrary differences between pinned
quays is mathematically impossible.

`DirectorHost` owns the store, scheduler, recorder and free-roam state. `HarborpulseApp.DirectorFeedFactory`
is an optional `Func<IDirectorFeed>` supplied before `Initialize()`. The default live feed is
`DirectorObserver` over the session's `DirectorReads`; an injected missing feed reports **no observer feed**.
Offline preview supplies labelled scenario stand-ins and platform-read gaps. The feed receives only
`DirectorInputs`.

The camera-space uGUI Canvas is in `Scripts/Hud`, with text and visibility rules in engine-free `HudModel`.
The portable checker excludes this package-dependent renderer, like towerpulse's story UI. It uses
`LegacyRuntime.ttf`, white captions on a dark scrim, source words and shapes on the rail, and an offline
preview disclosure that survives still rendering. `E` toggles the U1 engineer readout and cards (initially
off), `D` toggles direction, and `H` hides the HUD. Drag/wheel, W/A/S/Q, arrows and PageUp navigate.
Reserved E and D never navigate: PageUp moves up and Right Arrow moves right. After 20 s idle, free roam
returns over 3 s. With direction off, the U1 number/F-key slots select named map poses. The overlay adds
the director log tail, presentation-scale disclosure, and millisecond display timestamps.

The local digest is SHA-256 of committed resource bytes after CRLF-to-LF normalization. Missing runner
digest is a mismatch, with reason `runner reports no layout digest`. A mismatch disables camera direction
while leaving navigation available. `-hp-layout-digest-override <64 hex digits>` is a labelled CONTROL
injection. Banner precedence is layout mismatch, reconnecting, then persistent red `TEST CONTROL: <defects>`;
test-control starts omit the title.

Live recordings go under `-hp-record-dir <root>` (default `Application.persistentDataPath/recordings`),
in a filesystem-safe run-start/session directory. A changed runner `startedAt` closes the previous
recording and starts another. Four caller-owned queued writers produce `observed.ndjson`, `world.ndjson`,
`director.ndjson`, and `checkpoints.ndjson`. The desktop worker flushes each second and drains on teardown;
IO or queue-full problems are visible. `world` contains typed /status clock/digest/defect samples, not raw
config or credentials. `sim-hour` director context events associate each scheduler event batch's show
second with its absolute sim hour without changing the committed U2a codec. The same reader can seek
these files. A browser player needs an alternative persistence sink before live recording; this player
target is Windows.

Runtime capture flags:

- `-hp-capture-dir <dir> -hp-capture-every <wall seconds>`: screenshots include the Canvas, sim time,
  day and active page in the filename.
- `-hp-quit-after-sim <absolute hour>`: quit at that runner/preview time (26 means next day 02:00).
- `-hp-resolution 1920x1080`, or `-hp-portrait` for 1080x1920 and `pose:home:portrait`.

Editor batch entry points:

```text
DeviceChain.Harborpulse.EditorTools.HarborBuild.BuildWindowsPlayer -hp-build-dir <dir>
DeviceChain.Harborpulse.EditorTools.HarborStills.HomeCheckFromCommandLine -hp-stills-dir <dir>
DeviceChain.Harborpulse.EditorTools.HarborStills.DirectorContactSheetFromCommandLine -hp-stills-dir <dir>
```

The HOME check holds offline preview at 12:30, renders beauty and flat URP Unlit masks at 1920x1080 and
1080x1920, restores materials and render settings, and writes `home-check.json`. Device counts use only
visible pixels with their own colour, requiring an 8 px bbox height. Water includes the separately
coloured mapped mouth. All three quays, mouth and ridge require visible pixels. The method exits 1 if
water exceeds 45%, sky exceeds 25%, fewer than 30 devices qualify, or a required region is absent.
The contact sheet advances the preview continuously; `-hp-director-hours <comma-separated hours>`
selects samples. It keeps the preview stamp and renders only evidence the installed feed supplies.

**HOME passes at both aspects.** The side quays keep their devices on the water-facing wall of their
northern two thirds (their southern ends are nearer the camera than HOME's frustum is wide there), and the
9:16 HOME looks east along the horseshoe from over the western town, (-560, 300, -60) to (40, 0, 10) at a
52 degree vertical field of view, because a north-facing 9:16 frame holds only North Quay. The masks check
at 12:30 gives 36 of 37 devices at 16:9 (water 44 %, sky 0 %; the truck is hidden in its depot) and 36 of
37 at 9:16 (water 35 %, sky 3 %), with all three quays, the mouth and the ridge visible. The camera-space
Canvas does not render reliably outside Play mode, so the HOME beauty frame is the scene with its burnt-in
stamp; HUD-bearing frames come from Play mode or the player's `-hp-capture-dir`.

Verification:

```powershell
cd demos/harborpulse-unity/Dotnet~
powershell -NoProfile -ExecutionPolicy Bypass -File Verify-Director.ps1
powershell -NoProfile -ExecutionPolicy Bypass -File Verify-Director.ps1 -CompileCheck
python verify_u2b_mutants.py
python check_director_log.py <recording>/director.ndjson
```

The verification script uses `.build/u2b` and disables MSBuild/compiler-server reuse to avoid cross-user
cache permissions on Windows. The synthetic clean timeline test exports `clean-director.ndjson` in the
test work directory, checks rendered caption transitions with C#, and the operator Python checker uses
the same rendered transitions, including titles, with recording-end or EOF as the final boundary. Mutation logs and named failing tests are under
`.build/u2b/mutants`. Shader compilation, uGUI and Editor-tool compilation, real masked visibility, player
build, live probe problems, live control runs, disk flushing under load and capture output need the Editor
and runner. No scene operation writes to the platform or creates a device session.

Editor review values: deck 2.4 m, flood-plain surface 1.7 m, gauge mast 4.2 m, charger canopy 3.1 m;
the U3 town and hills are described under U3 art pass. Verify rig occlusion and staff separation, cut-away apertures, outfalls, the mapped road
foundation and depot headland, tide-board alignment and CPU/shader water agreement. Verify the 34 px
caption at 1080p and its >=4.5:1 scrim contrast, camera-space capture, source shapes/words and declared
arrows, <=4 world tags (2 portrait), 5-node rail cap (2 displayed portrait), 20 s idle + 3 s return,
reserved keys, and preset selection with direction off. Live review must check missing/mismatched digest,
TEST CONTROL precedence, stale/reconnecting behavior, observer problems, run rollover, one-second disk
flushes and teardown drain. Build the Windows player and inspect HUD-bearing captures with sim/page names.

The verification gates cover the engine-free model, desktop and WebGL compilation, and both caption
checkers. These do not establish an Editor visibility or visual pass.

One U2a bug fix is included: `Scheduler` defers stale-clock caption ends to the three-second visible
floor, including a reconnect before that deadline, and re-offers the slot for fresh evidence evaluation.
`StaleCaptionFloorAlsoSurvivesShortReconnect(false/true)` failed against the committed behavior before
the fix; the failing TRX is retained in `.build/u2b/stale-caption-before.trx`. The review corrections below extend the observer, predicates, storyboard and recording checks.

### U2b observer feed

`HarborLiveSession.DirectorReads` publishes immutable read snapshots from `Live/DirectorReads.cs`.
Each document has a bind-time operator authorization probe, then scoped polls at a three-second cadence,
with up to six requests in flight for the per-device reads (alarms, notifications, histories, commands) and one for each fleet-wide read. Pages are bounded at 1000; truncation is a reported failure.
Unanswered data remains absent with state `waiting`; failed probes stay visible in `Problems` and
invalidate that read's coverage for the recording. The shared observer tap retains escalation event
times before the U1 latest-state reducer runs. Reconnect counters preserve gaps even across a frame
that sees the streams live again.

Construct `new DirectorObserver(layout, () => live.DirectorReads)` once per run and call `Update`
after building the U1 frame, supplying the run, recording epoch, wall instant, absolute sim hour,
source and frame through the committed `DirectorInputs` seam. Feed its observations and coverage to
the recorder/store, and display `Problems`. Layout device identities in the storyboard are translated
from bound external IDs; alarm tokens/cycles, journal IDs, command tokens and attempt tokens remain
the platform's identities. Offline preview emits only supplied stand-ins with SCENARIO provenance
and platform-read gaps. Restore-set identities use the verifier's database microsecond precision;
unexpected stored times remain in the observed set and prevent an exact tally.

`Update` runs on every frame and the reads are republished every few seconds, so the observer does on a
frame only what changed: a read is parsed once per distinct answer (a poll that brings back the same bytes
keeps its rows and moves its as-of), and each join (alarms, attempts, the replacement, commands, the
rollout, membership, the measurement histories, the runner's declarations) runs again only when something
it reads has changed or an instant it was waiting for has come: a record stamped after "now", a service
window's end, the minute a replacement waits for its first reading. A join that found a record unreadable
restates that gap on the frames it sleeps through. `Dotnet~/Tests/DirectorObserverChangeTests.cs` holds
this to a fixture day shaped like a recorded one (`DirectorFixtureDay`): the records emitted are the same
with every join run on every frame (`RunEveryJoin`), their digests are those of the observer before it
waited for changes, and a frame's record visits do not grow with what unchanged reads hold. To see where
a tick's time goes, minute by minute:
`HP_FRAME_PROFILE=1 dotnet test -c Release --filter FullyQualifiedName~DirectorFrameProfile --logger "console;verbosity=detailed"`.

Two storyboard predicate corrections are covered by real-identity regressions: page 6 groups each
device's own alarm cycle within the outage interval, and page 8 joins a post-failure reading to that
device's actual attempt, rather than treating a rollout as an attempt shared by all targets.


### U2b review corrections

Page 2 now says **"No notification recorded / while the work is on."** Its rail says **"No notification
recorded in the window"**. This is a wording change the maintainer must approve: the best-effort attempt
ledger cannot establish that no notification happened. Both caption lines fit the 42-character limit;
the existing claim lint and cut timing law still apply.

Rollout `TERMINAL` observations do not carry counts. A separate `COUNTS` snapshot is available only
when complete successful target and relevant assignment reads cover the terminal instant. Updated
comes from platform `progress.succeeded`; refused counts only reported `VERIFICATION_FAILED` failures
with `platform=false`. Missing, failed, truncated or older child reads, unresolved targets and missing assignment identities
withhold counts, report a problem and create an updates gap. Read coverage uses the request start, so a response finishing after a
transition cannot make an earlier request cover it.

The shared observer tap retains every measurement before latest-state reduction, including intermediate
well readings, and records them with their platform times. Stream liveness and reconnect counters gate
coverage on that path. Commands and notification attempts defer their cycle join until a complete alarm
read covers their own platform instant; later raises cannot re-key unchanged earlier records.

Credential, artifact trust, target outcome and membership reads have no transition timestamp in the
selected contract. They are snapshots marked with `asOf` (recording seconds at request start), not dated
transitions. Their numeric occurrence slot equals observation time for wire compatibility, but temporal
predicates cannot order them; `occurredAtS` is unavailable as a predicate field. Artifact trust is never
backdated to rollout start. Real alarm, attempt, command and measurement transition timestamps remain
platform timestamps. Snapshot predicates reduce to current state before testing it.

Every complete membership result, including an empty one, replaces its previous set. Once all quay
reads succeed, the live scene rebuilds `PresentationMap` with platform membership. Moved anchors rebuild
the world, ground apertures, views and camera maps. Waiting/failed membership reads and M2 readiness
warnings remain visible in the HUD problem line and engineer overlay. Missing members are explicitly
unknown. Recording creation failures have their own persistent problem, cleared only when a recording
sink is established. Frozen-shot caption opportunities shift across reconnects, including a 60-second
outage.

`DirectorHost` records `visible-caption`/`visible-caption-end` after the HUD hold queue, including title
text. Scheduler decisions remain in the log for evidence. Both checkers prefer those rendered
transitions when present, reject empty acceptance logs and check the last visible caption through
`recording-end` (written on normal teardown), or EOF after an interrupted recording.

Run `python verify_review_mutants.py` from `Dotnet~` for all ten corrected findings, including both
implementations of 12. It requires a complete green baseline, a named test failure for each broken guard, byte-for-byte
source restoration and a complete green final gate. Logs, TRX files, restoration hashes and summary are
under `.build/u2b/review-mutants/`. The normal verification gate also runs the Python checker regressions.

## U3 art pass: town, hills and water

> The hills, forest, rocks and HOME cameras described in this and the next two sections were superseded by
> [U5 landform, forest, outcrops and cameras](#u5-art-pass-landform-forest-outcrops-and-cameras) below.

The U2b town was a uniform grid of identical houses on six terraces, and the water mirrored it as a hard grid.
U3 replaces both. `Scripts/Visuals/Assets/TownBuilder.cs` builds everything behind the waterfront, and it is all
WORLD scenery: no device stands there and nothing in it reads the platform.

- **Ground.** Inside the core (|x| < 240 m, z < 140 m: the water, the three quays, the mapped road, plain and
  depot) the ground is the deck, exactly as before. Outside it a height field (`TownBuilder.Height`) rises gently
  for the town, then into forested hills, then higher ridges far off. The grid is dense (10 m) round the town and
  coarser outward, with no T-junctions, and leaves out every quad inside the core.
- **Town.** A seeded plan of eight streets: Main Street climbs inland from the North Quay road between the lofts
  and the north pump and bends as it goes, with side streets off it, Shore Road behind East Quay and a lane on the
  western side land. Lots are laid along each street and kept off the core, the streets, each other, the park,
  the mapped fences and road, and 30 m round every device anchor and the depot; every corner is tested against
  the buildings already placed, the church first. There are five archetypes: a
  clapboard house (one or two storeys, side or front gable, sometimes a porch), a shopfront house, a brick
  commercial block with a cornice, shop windows and an awning, a waterfront shed and an inn with dormers and a
  veranda. One white church with a steeple stands where Church Street leaves Main Street. Windows are on every
  facade, about half of them lit at night, and the eastern town is on the east feeder (`EastWindows`), as the
  U2b houses were.
- **Trees and hills.** Low-poly deciduous trees in autumn colours and dark conifers stand in the yards and verges
  and round the park. The hills carry a forest of larger crowns, sparser and coarser with distance, with
  clearings, every crown wholly over land. Crowns more than 520 m outside the core are baked with the eight-face
  ellipsoid (a fixed choice, not a camera LOD). No tree casts a shadow, and the trees sit on a code-only layer
  (29) that the planar reflection leaves out, so the water reflects the town and the hills' ground without a
  second pass over every crown.
- **Waterfront.** There is a park lawn at the harbour's north-west corner with a gazebo by the water and a path,
  a boatyard with three sheds and a hauled-out boat behind East Quay, two timber piers on pilings (off East
  Quay's south end and inside the Basin's southern dock), and eight moored boats that ride the water like the
  buoys. The five box "fishing boats" are gone, the flood plain's parked cars have a body and a cabin, the lofts
  are brick with a cornice and the ferry terminal is grey clapboard under a pitched roof.
- **Batching.** Geometry is batched by material into 300 m tiles, 900 m for the hills' forest (`MeshKit.Place`
  builds a placed object into a shared kit), so culling works per tile. The HOME check logs the world's renderer,
  vertex and shadow-caster counts.
- **Water.** The planar reflection now has mips, and `HarborWater.shader` samples it soft: a seven-tap,
  vertically stretched kernel on a blurred mip, blurring more with distance. The pass clears to transparent black,
  so the taps' colours are already weighted by coverage; the kernel divides by the summed coverage once. Beyond the
  fade the taps are skipped. Stronger ripple distortion breaks
  it up, its strength is 0.62, and it fades out between 300 m and 1100 m. The Fresnel peak is lowered to 0.82,
  so the water keeps its colour at grazing angles. The deep colour is a teal rather than a navy. A turquoise
  shallow tint lies along the quays (75 m wide), lit like the body, and the storm greys the harbour down.
  The detail ripple is a little stronger.

`MeshKit.Quad` now sums both triangles' normals: the cone sides of the conifers have one collapsed edge, and a zero
normal shaded as NaN, which bloom spread across the frame.

HOME still passes at both aspects with every quay, the mouth and the ridge (now the terrain and its forest)
visible: 16:9 water 43.5 %, sky 0 %, 36/37 devices (the truck is in its depot); 9:16 water 23.0 %, sky 0.7 %,
36/37 (East Quay lamp 4 is under the 8 px floor, as it was before U3).


## U3 art pass: light

- **Overcast day.** `DayLighting.Cloud` (0.55) is a fixed high cloud. It softens the sun (×0.78) and its shadows
  (`SkyState.ShadowStrength`: 0.57 at noon, lower in the storm), pales the sky to a grey-blue with a pearl horizon,
  raises the ambient (×1.19) and adds haze (fog density 0.00026). `SkyState.CloudCover` (the high cloud or the
  storm, whichever is thicker) is what the sky shader draws; `Overcast` stays the storm's own cover.
- **Readable storm and night.** The ambient never drops below a cool moonlit floor (sky blue at least 0.24). The
  storm's sky is lighter and its ambient is lifted ×1.5, so it darkens the sky rather than the town. The moon is 0.5.
  The grading lifts exposure by up to 0.55 EV at night and 0.1 EV in the storm, desaturates the night (never past -16) and caps
  bloom at 0.5.
- **Emission in the player.** URP Lit's `_EMISSION`, `_ENVIRONMENTREFLECTIONS_OFF` and `_SURFACE_TYPE_TRANSPARENT`
  are `shader_feature` keywords: a player compiles a variant only if some material in the build uses it, and every
  material here is made at run time. Before U3 no window, lamp head, beacon or flow streak emitted in a player, and
  the well glass was opaque (the Editor compiles every variant, so its stills hid this). `HarborSceneBuilder` now
  creates `Assets/Harborpulse/Materials/LitVariant-{Emission,Matte,Transparent}.mat`, and the scene references them
  through `HarborpulseApp.litVariants`. With emission real, the signal emissives are tuned for the player (alarm
  levels 0.6/0.8/1.0, beacon 1.0, lamp head 4, halo 1.2), low enough that the tonemapper keeps each severity's
  colour, and the gauge alarm count has a dark plate behind it.
- **Lamps and windows.** The town's own street lamps stand about 26 m apart along every street, alternating sides,
  clear of buildings, other streets, the mapped ground and 12 m round devices. They are scenery: the quays' lights
  are the devices. About half the town's windows are lit at night (emission 4). The eastern town's windows and
  lamps go dark through the east feeder outage, a WORLD cue like `EastWindows` before.
- **Frame stats.** `-hp-frame-stats` turns vsync off and logs the mean, 95th-percentile and worst frame time every
  10 s.

### Quay surfaces and prop clearance

The quay tops use vertex-coloured concrete, promenade pavers and asphalt, with derivative-filtered procedural
expansion joints, stone joints, weathering and darker water-edge staining in `Resources/HarborScenery.shader`.
Coping, worn bollard/cleat paint, road dashes and parking bays share merged meshes. Adjacent 2 m paving tiles
at the same height share top strips; the well apertures and low-plain boundaries retain their original shape.
Seabed-to-deck perimeter skirts close the ends and back of each quay and merge into its existing stone batch.
The vertex savings exceed the skirts' cost without adding renderers. Core surfaces evaluate additional lights
per fragment through URP 17.5's Forward/clustered light loops, so lamp state and dimming illuminate the ground.
No imported textures or per-frame surface generation are used.

Gauge alarm counters use an opaque unlit dark plate and unlit font pass, ordered above the emissive beacon.
The plate fits the text bounds; neither weather nor point lighting changes the label palette. Both materials
use a shader in Resources so their player variants are retained. Shader rendering and final pixel contrast,
like HOME masks and frame time, require the Unity capture rig; the portable compile checks validate C# only.

`Model/WorldPropPlan` owns the seeded placements consumed by the waterfront, town, terrain and tree builders.
Its `FootprintRegistry` checks oriented rectangles with 0.15 m clearance using a build-time spatial hash.
Device anchors are fixed reservations; cars try deterministic alternative bays, while colliding decorative
props are refused. Conservative envelopes include building overhangs and annexes, full tree crowns/lean,
granite lobes, gauge staffs, benches, bollards and cleats. Decks, rails, buried plumbing and overhead canopies
are attached surfaces rather than independent ground props. The pump's attached well is 13 m along local X
to clear North lamp 2; geometry, markers, water mask and deck aperture use the same `WellFootprint` offset.

`WorldPropPlanTests` independently checks every accepted footprint pair, device preservation and repeatability,
and plants a car on a fixed lamp to prove both the audit and placement guard reject a real collision. These
sidecar checks and desktop/WebGL compilation do not establish HOME pixel masks, shader rendering or frame time;
those still require Unity. The prior HOME results above describe the U3 capture.

### Round 2: readable coastal scenery

The HOME cameras, presentation anchors and quay outlines are unchanged. The coastal ridge now settles behind
an exposed rounded summit and ends at 2.4 km; the pure height-field projection checks 10-16% sky at landscape
HOME. This is an analytic terrain check, not a new pixel-mask result. Meadow greens, rust bracken, worn-earth
street edges and granite facets replace the pale town ground. Larger grey-pink shelves are reserved at the
shore and on the HOME-facing upper slopes.

Scenery palettes are decoded from display RGB to linear vertex albedo, and direct diffuse light uses the
Lambert scale. This prevents the custom surface shader from overexposing concrete and grass relative to Lit
materials. Concrete/promenade colours are warm mid-grey (0.54-0.59 display RGB), seams are softer, and the
water edge has a darker wet band. Main Street is 10.5 m wide with a 2 m meadow verge, batched into terrain.

Day haze matches the cool grey-blue horizon. Density is 0.00016, with a much lighter height term; both ramp
only beyond 1,050 m outside the core, preserving the town and middle slope. The far third fades gradually.
Woodland follows seeded connected stands with meadow gaps and exposed summit ledges. Trees have fuller
two-tone crowns, thicker trunks with buried dark flares, and tiered conifers. There are 806 trees versus
1,056 in round 1; estimated tree vertices fall from 182,803 to 131,869 and occupied tree tiles from 47 to 32.
Granite and verges share terrain meshes; trees still cast no shadows and remain excluded from reflections.

Round-2 gates: locked restore, Release build, all 416 sidecar tests, and desktop/WebGL compile checks.
Unity was not run: the new beauty, both HOME masks, actual total renderers/vertices and the 12.4 ms frame-time
baseline still need the capture rig. The earlier pixel-mask results describe the earlier captures only.

### Round 3: closed backdrop and exposed summit

Round 2 exposed the camera-following far-water ring beyond the terrain's 2.4 km edge. The HOME checker
assigns unlit colours by renderer category; wet-edge colours do not affect its classification. In the
round-2 masks, 61,176 landscape water pixels (2.95023 percentage points) project onto inland ground.
Terrain now continues to 4.8 km in the existing edge batches, fading into the existing far fog. The far
water ring also intersects the WORLD coast and harbour core, so inland water cannot reappear above the
ridge or beyond the finite terrain. Its eight reusable patches have 32 vertices instead of 5,600;
only camera grid-cell changes update their vertices, without allocating a new mesh.

The central rounded summit reaches about 110 m, with lower shoulders and an exposed saddle. Sage/olive
meadow, ochre-tan bracken, sparse muted rust and grey-green conifers soften the hills; autumn crowns
remain. Larger pink-grey granite clusters with dark facets share the terrain batches. Whole-crown
reservations stop the far forest before the summit, removing the distant treetop band. Quay asphalt is
now a lighter top surface rather than a dark slab with exposed seabed-to-deck corner faces. Presentation
anchors, quays, HOME poses and device geometry are unchanged; the mask checker and its thresholds are unchanged.

Expected HOME water, obtained by projecting the supplied round-2 mask pixels onto the WORLD coast:
**16:9 43.52691%, 9:16 23.32948%** (36 and 37 visible devices respectively, as in round 2).
The terrain silhouette estimates 8.65% landscape sky; retaining the open sea gives roughly 5.2% portrait
sky. Trees and granite can change those silhouette estimates. These are predictions, not Unity mask
measurements. Both views should retain all three quays, mouth and ridge; the rig must verify pixels and
the 12.4 ms frame-time baseline. Terrain continuation, water and road changes save about 3,868 vertices
before forest savings and add no renderers. The seeded plan now has 444 trees (806 in round 2) and
27 granite clusters. Trees remain shadow-free and excluded from reflections.

Round-3 gates: locked restore, Release build, all 421 sidecar tests, and desktop/WebGL compile checks.
No Unity run or commit was made.

## U5 art pass: landform, forest, outcrops and cameras

The U4 look read as one bare lawn of hill with no sky behind the story cameras. U5 lowers the ground, closes the view in
haze, plants a wooded coast, seats the rock in the slope and drops the story cameras to eye level. It is all WORLD
scenery and camera framing: presentation anchors, quays, device positions and the U4 prop-overlap checks did not move.

- **Landform** (`Model/CoastalTerrain.cs`). The core is still the deck. Outside it the ground rises about 9 m across
  the first 450 m (the town's apron), then rolls in knolls and hollows of 6-16 m with a smooth ceiling at 32 m above
  the deck; nothing within 1,300 m of the core is higher than 34 m (the measured maximum is 18.4 m). From 6 m above
  North Quay the bare-ground skyline is under 1 degree (limit 2.5), and from the East Quay and Basin eyes under 0.7
  (limit 3). The skyline is a separate, distant ridge instead (`RidgeBuilder`, 155 quads = 310 triangles, no trees):
  two or three overlapping hills 2.35-3.05 km out, with dome, cusp and plateau profiles, the highest crest 2.54 degrees
  above the horizontal from the harbour and shoulders 1-1.8, falling to nothing at both ends. It sits under the HOME
  checker's "ridge" mask with the terrain. The terrain mesh is the plan's own grid (`Model/TerrainGrid.cs`: 10 m cells
  round the town, 20 m on the rolling ground, 50-100 m beyond, 200 m flat flanks to x = +-3.8 km), 125,688 vertices.
  `TerrainGrid.MeshHeight` is the height of the two triangles actually drawn, which is what rocks and trees stand on.
  The 4.8 km continuation is gone. A west headland where the near land falls to the sea was skipped: the far-water ring
  covers only the sea south of the coast, so a falling coast would open holes rather than a horizon.
- **View distance.** Scenery fog counts from the camera (no 900 m dead zone) and ramps in with the distance outside
  the core (120-420 m), so the town and its yards stay crisp: about 54 % fog at 900 m and 88 % at 1,500 m at noon, 53 %
  at 550 m in the storm. The shader multiplies the density by `SceneryFogGain` (6): the engine's own fog on the lit
  materials and the water reads the same density and must stay light, so only the scenery gains it. The ridge has its
  own lighter fog (vertex alpha 0.6 selects it; `RidgeFogScale`) and its foot dissolves into the haze by height: about
  40 % transmission at noon, 13 % in the storm. Every camera far clip is 3,600 m.
- **Forest.** 9,674 trees (the U4 plan had 444), 60 % conifers by count (spruce, fir, pine in deep greens, a few
  blue-green, lighter toward the top tiers), 40 % broadleaf in three shapes (round maple, tall oval birch with a pale
  trunk, broad oak) whose autumn colour follows a slow position noise, so gold, orange, crimson, russet and green-olive
  come in drifts. Heights are 8-16 m in the forest and 5-10 m in yards. About 70 % of the open land 250-1,100 m out is
  woodland in broad stands, with twelve clearings of 60-200 m (some touching the town's edge, one on a knoll summit),
  planted on a jittered 8 m grid thinned to a 12 m crown spacing near the town (the vertex budget's price for full-detail
  crowns) and 15-21 m beyond, fading to nothing at 1,250 m so nothing pops. Yard trees stand behind and beside about one
  house in two, and the park carries trees on its perimeter only. No tree stands on a street, a lot, a quay, a park path
  or lawn, within 14 m of an anchor, in a rock footprint, or on a story camera's line of sight. Detail is by distance
  from the core, baked per tile: within 380 m 4-5 conifer tiers or a full crown with a side lobe; 380-800 m two tiers
  or one low-poly crown with a short trunk; beyond, one or two four-sided cones or an eight-face blob (`TreeStyle`).
  2,765 near, 4,433 medium and 2,476 far trees cost 393,116 + 215,235 + 50,196 = 658,547 vertices (limit 700,000) in
  48 tile meshes. Trees still cast no shadows and sit on layer 29.
- **Rock.** 62 outcrops in 16 clusters of 2-5 (the shore outside the quays, clearing flanks, woodland edges), each a
  stepped, faceted granite ledge of 8-30 m in plan and 0.6-4 m high (never more than 0.4 of its plan radius), whose long
  axis follows the contour. `Model/Outcrop.cs` fits each against the drawn terrain: the base ring is sunk until every
  point along its edges, sampled every half metre, is below the surface (the tightest margin is 0.58 m), the tops follow
  the fitted terrain plane, and the ground in a skirt round it is darkened. Footprints are in the registry, so trees and
  props avoid them. They share the terrain tiles (4,560 vertices in all).
- **Ground colour.** Forest floor under the wood, dry straw-green meadow in warmer and greener drifts between, heath
  tone on steeper facets, lawn green only on the park and in yards, worn earth at the street edges. Per-facet variation
  is a smooth noise plus under 1 %, so there is no checkerboard.
- **Cameras.** HOME 16:9 is (0, 100, -420) looking at (0, 4, 50), vertical FOV 35: horizon 17.6 % down, sky 19.6 % by
  the analytic silhouette (terrain, ridge and tree tops), flat-plane water 42 % (the same estimate gives 45 % for the U4
  pose, where the mask measured 43.5 %), every device but the truck inside the frame with a 3 % margin, both breakwater
  lighthouses, the mouth and most of each quay in frame, and the frame's bottom edge on the water at z = -240. HOME 9:16
  is (-560, 300, -60) looking at (40, -25, 10), FOV 52: all 37 devices inside, sky 11.5 %. The land now stands far below
  that eye, so the far ground ends well under the true horizon; the look is steepened a little rather than raised to keep
  the sky share down. The ground-level story poses (`kiosk-ext`, `pair`, `close`, `wide`, `promenade`, `insert` for each
  device, and `east-quay:mouth-wide`, `plain:wide`, `truck:chase`, `depot:wide`) now stand 6.5-10 m up with the look
  flattened so the horizon falls 29-36 % from the top and the subject is in the middle third. A few step sideways so their
  sight line passes beside a lamp or a gauge mast instead of through it. `well` and `well-locked` still look down into
  the well.

`U5CameraTests`, `U5ForestTests` and the extended `CoastalSceneryTests` pin these numbers: the terrain apron, ceiling and
skyline rays, the ridge's crest angles, woodland cover, fog at 900 m, 1,500 m and 550 m, the ridge's fog, the 60/40
species ratio, heights, no tree on any reservation (each check has a planted negative control), the vertex budget per band,
the rock fit (a lifted base ring fails it), the HOME frames from pose, FOV and aspect, and every ground pose's horizon,
middle third and line of sight. A literal-checksum test proves the 104 building lots and 71 street lamps are exactly what
origin/main generated: the town's random stream is never read by the new rock and tree generation, which has its own
seeds.

Not verified without Unity (the sidecar and both compile checks do not run it): the shader's syntax and look, the HOME
masks and the actual renderer and vertex totals (estimated at about 870 renderers and 1.29 M vertices against the
budgets of 950 and 1.5 M), frame time, the rendered colours and silhouettes, and whether the far-clip edge of the sea and
fogged ground meets the sky's below-horizon colour without a visible seam: the fog and the sky share the horizon colour,
but the sky blends toward its ground colour just under the horizon.

## U5 level of detail: richer buildings and vehicles near the camera

HOME sees the town from 450-900 m, where a window frame is a fraction of a pixel; the ground-level story cameras
see the first row of buildings, the sheds, the terminal, the lofts, the parked cars, the truck and the moored boats
from 20-45 m. Each building and vehicle therefore has two levels.

- **FAR (what HOME and every distant view draws).** The same merged tile meshes as before, with the fine detail taken
  out: no window frames, sills, shutters or mullions, no clapboard courses, no steps. What stays is the silhouette
  and what the night is carried by: walls, the real roof shape and overhang, chimneys, the porch slab and roof and its
  end posts, the stone base, corner boards and door frames, the flat window panes (the shared `Windows`, `EastWindows`
  and `WindowsDark` materials, so the night's lit windows are untouched), shop glass and awnings. The far town loses
  about 171,000 vertices (an estimate, from the generation order: 1,585 windows lose a frame each, the rest is
  courses, steps and one post per porch), and the renderer count is unchanged.
- **NEAR (within 110 m of the active camera).** An extra set per building or vehicle, drawn on top of the far level:
  window frames, sashes, mullions, sills and heads, shutters on some houses (chosen from a hash of the building's
  position, never from the town's seeded streams), flower boxes, panelled doors with lights and transoms, stoops,
  porch rails with balusters, newel posts and stairs, a bench, pots, firewood or a barrel on some porches, clapboard
  and brick courses, board-and-batten, corrugated ribs, friezes, gutters and downpipes, shingle courses, dormer frames
  and a second dormer, an attic window in some front gables, chimney flashing, corbels and pots, shopfront bays over a
  bulkhead with a valance and brackets, sliding shed doors on their track with ramps, lobster pots and buoys, the lofts'
  entrance, string courses, pilasters, loading doors and roof plant, the church's framed and pointed windows,
  buttresses, louvres and finial, the terminal's entrance, the depot's roller doors, the ferry berth (pontoon planks,
  rails, gangway, bollards) and the ferry itself (portholes, fenders, window frames, rails, bridge, funnel, life
  rings). Vehicles get wheels with arch liners, rims and hubs (the cars' far boxes have none), glazing pillars, lamps,
  mirrors, bumpers, door seams and handles; the truck gets a roof rack load (sandbags, cones, a hose coil, a pump,
  cans) and chevrons; boats get lifelines and pulpits or a wheelhouse with frames, radar, aerials, buoys, a hauler,
  an outboard, oars, planking and cleats; the hauled-out boat gets props, rails, window frames, a rudder and propeller.
  No text or signage anywhere.
- **The switch is additive.** A near set only adds parts over the far level's own walls, roof and window panes, so
  nothing has to be hidden while it is shown: no hole can open, nothing pops but the added detail, the same windows
  are lit in both levels, and at HOME every set is inactive. This is why the far meshes stay merged per tile (a
  `LODGroup` would need a renderer per building, hundreds of extra renderers at HOME) and why there is no list of
  buildings whose far mesh must disappear.
- **Who decides.** `Model/DetailSwitch` is the whole decision and has no engine in it: a set enters at 110 m and
  leaves at 130 m measured to the set's nearest edge (centre distance less its radius), at most 25 sets are near
  at once (the nearest win, a set already near has a 6 m head start so the cap cannot flicker), the work allocates
  nothing after construction, and it is retaken every four frames or when the camera has moved a metre.
  `Visuals/DetailManager` owns the sets: it builds each one the first time it is wanted (or at load, inactive, when a
  story camera can bring it within reach: the authored poses, 20 m of margin on each camera and 130 m about each
  look point, from `DetailCandidates`) and switches the root on and off. The truck and a charger's car follow
  their own transform every refresh; a car the view has hidden is never near. The planar reflection draws whichever
  level the main camera has.
- **The stills path.** `HarborpulseApp.Tick` refreshes the switch every frame; `RenderReflectionNow`, which every
  Editor still calls just before `Camera.Render`, refreshes it forced for the camera's final position. A still shot
  after `Rig.Select` or `DirectorCamera.ForcePose` therefore shows the level that camera would have in play. At HOME
  nothing is near; `HarborHomeCheck` logs the near sets (built hidden, shown) and fails if any is shown at HOME. Its
  world renderer and vertex counts leave the near roots out (they are hidden at HOME), so they remain comparable with
  the earlier captures. Near roots sit under the town, the device rigs or a boat, so the HOME mask classes them with
  their parent; they are scenery and never hide a device (they stay inside the lot the building reserved).
- **Budget.** A near set is one vertex-coloured renderer (no shadows: it is all small trim, and the far body keeps
  casting) plus one more per window material only where a set has windows of its own (the attic window and the second
  dormer). Typical vertices per set: house 5.8 k (2.7-9.1 k), shopfront 4.6 k, commercial block 10.2 k, inn 13.8 k,
  shed 2.6 k, church 3.2 k, lofts 12.6 k, terminal 6.8 k, depot 3.3 k, ferry berth 5.2 k, truck 3.9 k, parked or
  charging car 2.1 k or 1.8 k, hauled boat 1.9 k, moored boats 0.6-1.7 k. From each authored ground-level pose
  the sidecar tests find 6-17 near sets and about 23-90 k vertices, and bound every plan so twenty-five of the largest
  houses stay under the 250 k budget.

`NearDetailTests` pin: the hysteresis band (no change anywhere between 110 and 130 m), the 25-set cap and its
stability, the range measured to the edge, a moving set switching by its position, an inactive set never near,
the retake schedule, no allocation per decision, HOME in both aspects selecting nothing, every ground-level pose
selecting something, every set a story camera or its 20 m margin can bring near being a load-time candidate,
every near plan staying inside the lot or footprint the building reserved (and vehicles inside theirs plus
6 cm of trim), no near box covering a far window pane, a frame for every lofts pane, bounded vertex cost per kind,
and that the per-house choices come from the position and repeat. The sidecar and compile gates establish none of
the visual result: the look of the near sets, HOME pixels, the stills' switch timing and frame time need the Unity
capture rig (the budget to hold is the earlier 4-5 ms mean at 1920x1080).

### U5 round 2: haze range, visible meadows and granite, lighter conifers, lamp and mouth poses

This supersedes the fog, forest, rock and pose numbers above where they differ.

- **Haze range.** Scenery fog starts 350 m from the camera (`SceneryFogStartM`) with a gain of 3.5. From HOME at noon transmission
  is 0.87 at 1,000 m, 0.83 at 1,100 m, 0.53 at 1,750 m and 0.23 at 2,500 m; in the storm half falls at about 1,000 m. The ridge
  (own fog, scale 0.40, darker bands) keeps about 0.6 at noon and about 0.1 in the storm. The engine's fog density is back to
  its pre-U5 value. The planted forest runs to 1,700 m from the core (minimal detail, spacing growing to about 29 m, faded from
  1,250 m); the terrain grid is 20 m to 1.2 km and 30 m to 1.9 km.
- **Counts.** 12,276 trees (59.5 % conifers), 721 k vertices (2,877 near, 4,259 mid, 5,140 far). Woodland covers 68 %.
- **Meadows and granite.** Six meadows of 85-130 m sit where HOME looks (two touch the town's edge), with a cluster of granite ledges
  at each; lead ledges are 18-35 m. 78 outcrops in all. A test projects them through the HOME pose.
- **Conifers.** Greens from (.16,.34,.19) to (.28,.44,.24), about 15 % blue-green, top tiers 25 % lighter, whole stands drifting
  in tone; far broadleaf crowns are brightened and more saturated.
- **Poses.** Lamp subjects (`lamp-east`, `north-quay`) stand 14-18 m off, 30 degrees round from the quay's axis at eye 5-6 m, the
  look just under the head so the whole lamp sits upper-middle of the frame. `east-quay:mouth-wide` stands inside the harbour at
  (-130, 9.5, 70) looking at the quay (150, -10) with FOV 56; no breakwater or lighthouse is within 80 m of its axis.

## U5 sky and weather

The sky is a pure function of the scenario hour and the layout's `storm` beat (`DayLighting.Evaluate`), and the light follows the same
cloud the sky draws.

- **Cloud cover** is no longer a constant. `DayLighting.FairCloud` gives the fair-weather cover of the hour (about a fifth of the sky in
  the small hours, two fifths in the early afternoon). `Weather.Front` is the storm's cloud front: it builds over the three hours before
  the beat, is closed through it, and breaks up over the two hours after it, on either side of midnight.
- **The sky shader** (`HarborSky.shader`) draws a three-stop gradient, the low sun's glow along the horizon, sun and moon discs that
  cloud hides, stars, a high streaky layer and a low cumulus layer shaded in three flat tones, and rain curtains under the storm deck.
  The cloud drifts on a daily circle driven by the scenario clock, so a still of an hour is always the same sky. The horizon line is the
  fog colour, so the haze over the land meets the sky without a seam.
- **The moon's arc is continuous.** The hidden part of an arc now has its own length; the moon's direction used to jump once a day.
- **Dim daylight is lit.** A storm dusk and the hour before dawn have neither sun nor moon as a key light. The ambient floor is lifted
  then (`KeylessFloorLift`), and the grade opens up for dim daylight (`SkyState.ExposureLift`), without changing noon or the night.
- **Rain** is a box of slanted streaks that follows the camera and wraps seamlessly; it is off whenever the weather is dry.
- **Lamp poses** stand 20-24 m off and look up a little (`PresentationMap.LampHorizon`), so the lantern, 8.3 m above the deck, stands
  against the sky. This supersedes the 14-18 m lamp stand-off described above.

`SkyWeatherTests` pins the cover through the day, continuity across every minute and across midnight, distinct skies at the seven
reviewed hours, noon exposure within 15 % of the earlier grade, the night floor, and the sun's and moon's continuity. Shader output,
the masks and frame time still need the Unity capture rig.

## U6 depth, light and coast

U5 left six things short: the 9:16 HOME was mostly haze under an out-of-frame horizon, the land between the last trees and the
ridge was one pale band, the hour was hard to tell from HOME (it looks north, and the sun and the moon cross the south), the
granite read as pale patches, a lit lamp was a faceted blob and the truck's near level was trim over a box, and the browser
build had only ever been compiled. This supersedes the U5 numbers for the 9:16 HOME, the ridge and the haze where they differ.

- **Layered backdrop** (`CoastalTerrain.HillLayer`, `RidgeBuilder`). Two more ranges stand in front of the U5 ridge: wooded
  foothills 1.88-2.13 km out with crests of 30-44 m, and a middle range 2.15-2.45 km out with crests of 30-76 m. Their summits
  stand between the ridge's, so the three crest lines cross; from HOME their highest points are at about -1.1, -0.3 and
  +0.5 degrees, so the nearer two stay under HOME's eye line and the skyline is still the ridge's. Each layer has the ridge's
  lighter fog (transmission 0.72, 0.64 and 0.60 at their summits at noon, 0.23, 0.14 and 0.10 in the storm) and its foot
  dissolves into the ground haze over its own height (14, 26 and 50 m; the layer's vertex alpha, 0.72, 0.66 or 0.6, tells the
  shader which). The same kind of layer runs north from the coast on each side of the town (`CoastalTerrain.Uplands`: east
  and west foothills at |x| 1.96-2.21 km and ranges at 2.4-2.94 km, crests to 124 m): the 16:9 HOME holds none of them, the
  9:16 HOME looks straight at the eastern pair, and a ground-level camera that looks east or west has a skyline. Seven layers,
  about 1,800 quads in all, no trees, none within the plantation (which ends 1,700 m from the core), none more than 3 degrees
  above the horizontal from a quay.
- **Haze with depth** (`SkyState.GroundHaze`, `HazeColor`). The haze a view ray fades into now depends on its elevation: the
  horizon's colour at and above the horizon, and a darker, bluer ground haze from 2.9 degrees below it down. The scenery shader,
  the water shader and the sky's own below-horizon part draw the same function, so far ground fades to a tone that is darker
  than the sky above it, the land keeps an edge against the horizon, and no seam shows where the terrain or the sea ends.
- **The sea fades too** (`SkyState.SeaTransmission`). Open water now takes the scenery's distance haze (from 350 m out, no
  height term) in place of the engine's fog: it was dark to its far edge, which drew a hard line under the sky and along the hazed coast in any view that
  held the sea horizon. From HOME the harbour's own water is unchanged (transmission above 0.95 at 700 m).
- **The sky is held back from the exposure lift** (`SkyState.SkyGain`). The grade opens up by `ExposureLift` for a dim town at
  dawn, at dusk and under the storm; the sky, its cloud and the haze are now multiplied by 2^(-0.8 x the dim-light and storm
  lift) when they are drawn, so a dawn sky keeps its rose and a storm sky is slate instead of white. The light on the town,
  the night and a fair noon are unchanged.
- **The hour, from a camera that looks north.** A low sun is a key light sooner (`LowSunKey`: 0.62 of full at 7 degrees, where
  the plain curve gave a third of that) and is warm rather than red (2,500 K at the horizon), with slightly darker shadows:
  for an hour at each end of the day the faces turned east or west are plainly lit and the others are not. The sky-coloured
  ambient loses most of its blue while the sun is low, so flat ground does not turn periwinkle. The cloud's bases are lit
  from below while the sun is within 13 degrees of the horizon (`CloudUnderlight`), all round the sky and more strongly
  towards the sun, so the east and west halves of HOME's sky differ; a closed storm front leaves 7 % of it. At night the moon
  silvers the faces of the cloud everywhere and the cloud round itself most (`MoonCloudGlow`), off under the storm's deck.
- **A pose that sees the moon.** `pose:basin:night-sky` (rig shot 12, `F3`) stands just off the east quay's north end and looks
  west-south-west across the water with the horizon 62 % of the way down; at 04:00 (`StoryPoses.NightSkyHour`) the moon stands
  18 degrees up in the middle of the frame, over its own glitter path. It is not a story pose.
- **9:16 HOME.** (-520, 260, 30) looking east at (0, 67.1, 10), vertical FOV 52: the horizon is 12 % of the way down
  (`PresentationMap.PortraitHorizon`), so the sky is in the frame, and so is the sunrise (the sun rises due east). All 37
  devices stand between 40 % and 90 % of the way down; above them are the eastern woods, the foothills and range in the
  haze, the sea horizon and the sky. The masks measure sky 19.5 % (it was 4.5 %) and water 25.3 %.
- **Rocky foreshore** (`CoastalTerrain.ShoreReach`, `ShoreBuilder`). The open coast is a straight line in the terrain's grid.
  A faceted granite foreshore now runs out from it, 6-60 m wide along most of the coast and up to 190 m at five named points,
  level with the land's own edge at the coast and 3 m under the datum at its seaward edge, so the tide draws the waterline; eight skerries stand off it; and the
  terrain's own coastal strip is turf and heath of uneven width instead of forest floor ruled off at the water. It starts
  290 m either side of the harbour's middle, clear of the breakwater roots and the depot, and nothing of it is inside the
  16:9 HOME frame, whose water share and masks are untouched.
- **Granite that reads as rock** (`TerrainBuilder.DrawOutcrop`, scenery code 0.9). Three values of warm grey chosen facet by
  facet, ledge tops lighter than the walls, every wall darkening to a shadowed foot, north-facing walls darker again, lichen,
  rust and moss on a few facets and a dark fissure across the crown. The scenery mesh casts and takes no shadow, so that
  shading is in the vertex colours. Close up the shader adds blotches, speckle, bedding cracks and lichen rosettes from the
  world position (no texture); they fade out by 220 m and 900 m. The outcrops' geometry, fit and count are U5's.
- **Lamp glow** (`Resources/HarborGlow.shader`). A lit lamp's halo is a camera-facing disc with a bright core and a soft skirt
  that reaches zero at its rim, added to what is behind it, instead of an emissive ball of 32 triangles. Its four vertices sit
  at the lamp and the corner is in the UV, so it faces whichever camera draws it (the water's reflection camera included). It
  is 1.7 m in radius close up and never smaller than 4 mrad, so a lit lamp still shows from HOME.
- **The truck's near level is a truck** (`NearVehicles.Truck`, 7.3 k vertices). It replaces the far boxes while it is shown,
  as the cars' near sets do (the far light bar stays: the platform's reports switch it): a cab with a raked windscreen and
  glass in body-coloured frames, a grille, lamps, bumper, mirrors and steps, external mudguards over tyres with rims and hubs,
  chassis rails, a tank and an exhaust, a service body of two side cabinets with compartment doors round an open bed carrying
  sandbags, a pump, a hose reel, cans and cones, a ladder rack with a ladder, and the fleet stripe.
- **Browser build.** `HarborBuild.BuildWebGLPlayer -hp-build-dir OUT` builds for WebGL. A browser has no command line, so the
  page address carries the preview's options: `index.html?hp-offline&hp-start-hour=17.3&hp-speed=4` (`Model/WebOptions`:
  `hp-offline`, `hp-portrait`, `hp-frame-stats`, `hp-start-hour`, `hp-speed`, `hp-quit-after-sim`; any other `hp-` key is
  refused, never ignored; nothing in a page address names a file or a runner). WebGL now defaults to the Laptop quality
  tier: the water needs the depth and opaque textures, which the Mobile tier's pipeline asset turns off, and on it the first
  browser run drew the whole harbour as foam. Run in Chrome on Windows (WebGL 2 through ANGLE on Direct3D 11) from the offline
  preview at four start hours, the sky, the cloud, the layered haze, the water, the rain, the lamp glow and the bloom all draw
  as they do in the Windows player. No other browser, no phone and no live link have been tried, and no frame time was taken
  there.
- **Stills.** A shot in `-hp-stills` may be a free camera, `hour:@x/y/z/lookX/lookY/lookZ/fov/name`, and `-hp-stills-hud 0`
  renders the scene alone under the burnt-in stamp. Shot indices are 0-12.

`U6DepthTests` pins: three northern ranges nearest first, each behind the last tree and inside the far clip; their crest
angles from HOME stepping up and crossing; no range above 3 degrees from a quay; the far ridge's U5 profile exactly; the haze
darker below the horizon at every reviewed hour and one monotone function of ray elevation; each range keeping more of
itself than the one behind at noon and in the storm; the sky held back while the town is lifted, with the horizon under white;
the low-sun key, its warmth and its side; the underlight at dawn, at a clear dusk and under the front; the moon's glow and the
night-sky pose framing the moon; the foreshore leaving the harbour and the 16:9 HOME frame alone while breaking the coast; the
truck's near set being a whole truck on the far wheels' places; and the page-address options, including every refusal.
`U5CameraTests` now pins the 9:16 HOME's horizon, its sky share and every device in the lower two thirds, and its sky helper
counts the sea horizon where no land stands in a column.

Measured with the capture rig (Unity 6000.5.3f1, Windows, D3D11): the HOME masks pass at both aspects (16:9 water 40.2 %, sky
19.1 %, 36 of 37 devices, the truck being in its depot; 9:16 water 25.3 %, sky 19.5 %, 37 of 37). The 16:9 sky was 19.6 % in U5:
the new ranges fill the ridge's saddles. The world is 901 renderers and 1.06 M vertices (U5: 892 and 1.05 M). Frame time at
1920x1080 over 120 s, the U5 and U6 players run alternately four times each: the mean of the 10 s windows was 5.27-5.99 ms for U5
(5.55 on average) and 5.65-5.91 ms for U6 (5.80 on average), so U6 is about a quarter of a millisecond higher, inside the spread
of U5's own runs. The player's captures of a day page (lamp work), a storm page and a night page (the feeder outage) were read
frame by frame.

## U7 light, crests, rock and crews

U6 left nine things short. This supersedes the U6 numbers for both HOME poses, the ambient light and the crews where they differ.

- **The storm's dusk** (`SkyState.HorizonBreak`). The scenario's storm (16:00-22:30) covers every sunset, and nothing here moves it:
  the beats, the cues and everything the rig judges are untouched. The front now stops short of the horizon towards the low sun. While
  the sun is between 15 degrees up and 6 degrees under, a band of clear lit sky shows under the deck, about 6 degrees tall towards the
  sun and tapering to nothing 110 degrees round from it (`SkyState.BreakReach`), gold while the sun is up and ember after. The 16:9
  HOME looks north, so it holds the band's northern taper: tall at the frame's western edge, gone before the eastern one. Once the sun
  is inside the band (under about 7 degrees) it reaches the town as a warm key again (`BreakKeyShare`: three quarters of what the storm
  took), the deck's bases above the band are lit, the rain between the eye and the band is lit from behind, the rain in the air turns
  from the WORLD cue's cool white towards gold (`HarborMaterials.SetRain`), and the water mirrors the band. The storm's cloud at dusk
  is slate; it was the brown a drained rose leaves.
- **Rain shafts** (`Shaders/HarborHaze.hlsl`). The haze a ray fades into is now one function in one file, included by the sky, the
  scenery and the water. In the rain it carries shafts, slanted bands up to 42 % darker, sampled round the compass and moved by the
  scenario clock, above the horizon and below it: the far land and sea under a storm are no longer one grey field.
- **A fair-weather look check.** `-hp-stills-weather fair` (and `HarborpulseApp.PreviewFairWeather`) draws the OFFLINE PREVIEW's sky,
  light, rain and sea for a dry day, so an hour the storm always covers can be judged as a clear one. It is a still tool: the stamp
  reads `FAIR-WEATHER LOOK CHECK, STORM NOT DRAWN`, the file name ends `-fair`, the preview's readings and cues are the scenario's
  own (a gauge still reads the storm's tide), a live scene refuses it, and any value but `fair` or `scenario` is refused.
- **Dawn and white balance.** At 06:24 the sun is two degrees up: a key, but a weak and grazing one, so the flat ground had only an
  ambient a third of the keyless hour's. The floor now carries a clear dawn and dusk as well (`TwilightFloorShare`), and once the sky
  is light the floor is near neutral instead of the night's blue (the night keeps its blue). By day the sky-coloured ambient keeps
  0.42 of the sky's colour (`DayAmbientKeep`), and less in the first and last hours: flat ground is lit by the whole sky, not the
  zenith. The scenery shader divides its direct light by pi and not its ambient, so on the quays that ambient outweighed the sun and
  turned concrete lavender. The grade's daytime temperature is +3 where it was -3. Measured on the 16:9 HOME stills, the west quay's
  top was (46, 32, 37) at 06:24 and is (83, 67, 61); (62, 80, 122) at 07:06 and is (75, 79, 90); (95, 102, 128) at 12:30 and is
  (102, 102, 107). The storm dusk's ambient has no violet left (blue was 1.6 times red at 18:18).
- **The wood on the ranges** (`CoastalTerrain.HillLayer.CrestTrees`, `RidgeBuilder`). Each of the seven backdrop layers carries a line
  of tree tops on its crest and two more down the face in front, 5,000 in all: flat cut-outs that face the harbour, one triangle a
  conifer and three a broadleaf, 8.5-17 m tall, six conifers to four broadleaves, in stands with gaps. The broadleaves' colour and the
  faces' own rust and gold follow a slow noise along the range, where a per-face hash scattered single faces. A crest's ground is
  still under 3 degrees from a quay; its trees add under half a degree.
- **16:9 HOME.** The look is raised from y = 4 to 7.5 (`PresentationMap.HomeLookY`), under half a degree: the horizon is 18.9 % down
  (17.6 %). The mask reads sky 20.1 % (U5 19.6 %, U6 19.1 %) with the ranges at their U6 height and their tree line on top, water 39.1 %.
- **9:16 HOME.** (-700, 235, -60) looking at (0, 22.2, -5), vertical FOV 44, horizon 12.5 % down. The devices stand between 46 % and
  66 % of the way down (U6: 49-83 %), the farthest drawn no smaller than U6 drew it; below them are the western woods and houses,
  above them the eastern woods, the uplands, the sea horizon and the sky. A backdrop layer seen from well above now lies in the same
  haze as the ground round it (it stood out of it as a dark lens), and the coastal turf strip is weighed at each corner of a facet
  and blended across it, where one weight a facet drew the grid as a staircase. The mask reads sky 19.4 %, water 15.6 %, 36 of 37
  devices: from this lower eye the Basin's second charger is behind the ferry terminal.
- **Granite** (`Outcrop.Blocks`). Every ledge of 10 m or more carries two to five loose blocks on its shoulders, and most ledges of
  22 m or more a tor: two or three slabs stacked on the crown, up to 8 m above the ledge's top. Each block is a prism whose bottom is
  half a metre under the drawn ground at every corner, inside the ledge's own rectangle: 192 blocks, 9 tors, about 7 k vertices in the
  terrain tiles. Walls darken to their foot and tops are light, so a block shows as a lit cap over a dark side.
- **Glass and the crews.** A vehicle's pane carries the sky in its upper part and a narrow glint (`NearPlan.Glazed`), cut from the
  pane's own outline. The WORLD crew is a utility van with a cab, a bonnet, side cabinets and the fleet stripe, six cones with
  collars and a two-board barrier; at a street light it has a bucket lift on outriggers with a technician at the lantern. It keeps the
  WORLD cue's cool palette. A crew is built when the scenario first sends one (there was a hidden van at every lamp and gauge): the
  world is 796 renderers and 1.06 M vertices (U6: 901 and 1.06 M).
- **The well's view and its tags.** A pump's `well` and `well-locked` poses now look at the well. They were aimed at the pump's
  anchor, and since the well moved 13 m along the quay to clear a lamp they framed the pump house's door with the well outside the
  frame, under the caption about the well filling. East Quay's well stands behind a charger bay, so there the eye is nearer and
  higher (9 m off, 13 m up) and looks over the canopy, which still covers the frame's lower left. The level tags are text, which the
  font's material draws through the deck; they are now drawn only for a camera that frames the well (`WellFootprint.TagsInView`:
  within 24 m and 20 degrees of the camera's axis), so a lamp or gauge shot beside a well no longer shows them at its edge. A still
  decides them again at render time, for the camera it finally has.

`U7LightTests` pins: the break opening only at a storm dusk, never in fair weather and never with a jump; the sun's key through it
only once the sun is inside the band; the band's reach across the 16:9 HOME and its absence from the 9:16 one; the dawn's ambient and
the ratio of blue to red in the ambient at the reviewed hours; no step in the ambient, the key or the exposure through either end of
the day; both HOME frames; every layer's crest trees standing on it, sunk into it, in the forest's mix, in stands with gaps and with
their colour in drifts; the tree line's cost in sky and its angle from a quay; every block under the drawn ground at every corner and
inside its ledge (a lifted block fails the same check); tors inside the HOME frame; the sky and the glint lying inside each pane; a
pump's well pose holding the well in the middle of the frame where the old aim did not; and the tags hidden from every lamp, gauge,
charger and HOME pose.

Measured with the capture rig (Unity 6000.5.3f1, Windows, D3D11): the HOME masks pass at both aspects (16:9 water 39.1 %, sky 20.1 %,
36 of 37 devices, the truck being in its depot; 9:16 water 15.6 %, sky 19.4 %, 36 of 37, the Basin's second charger being behind the
ferry terminal). The world is 796 renderers and 1.06 M vertices. Frame time at 1920x1080 over 120 s, the U6 and U7 players run
alternately four times each: the mean of the 10 s windows was 5.27-6.12 ms for U6 (5.56 on average) and 5.31-5.78 ms for U7 (5.55 on
average; three runs of the build before the well-view fix and one of the final build). The player's captures of the lamp-work page,
the storm page and the night page were read frame by frame. In Chrome on Windows (WebGL 2 through ANGLE on Direct3D 11) the browser build draws the
storm page and the night page as the Windows player does, and at 1920x1080 from HOME at noon it holds the display's 60 Hz (10 s
windows of 16.67 ms mean, 17-18 ms at the 95th percentile); with the frame-rate limit and vsync switched off in Chrome its windows
were 8.6-12.5 ms mean, on a Quadro P2200, by the scene's own `hp-frame-stats` log, which has a 1 ms clock in a browser. The first
frame took 13 s. No other browser, no phone and no live link have been tried.

## U8 the derate, live, and six things U7 left short

### Why the platform sent the command

Page 3b says "Running hot. The platform tells it to derate." A served command had no link to the alarm whose rule sent it, so the
claim could only bind through an `alarmToken` in the command's `metadata`, which nothing writes: in a live day the caption never
appeared, and the page showed its fallback, "Running hot. Alarm raised." A command sent by a rule now carries the cause the platform
recorded with it, and the director reads that.

- **The reads.** `DirectorCommands` selects `cause { kind ruleId ruleToken profileToken detectionKind detectedAt value alarmKey deviceId }`
  (and no longer `metadata`); `DirectorAlarms` selects `originatorId`.
- **The join** (`DirectorObserver.TryCauseCycle`). The cause holds what the rule knew when it sent the command: an alarm KEY, the device
  row and the detection's time, never an alarm token. For a cause of kind `ALARM` the alarm is the row with the command's device token
  and the cause's `alarmKey` whose `originatorId` equals the cause's `deviceId` (a device token can be reused by a later device). The
  cycle is named only when that row's `raisedTime` is at or before `detectedAt` and it was not cleared at or before it; an earlier
  cycle than the row's counts only if its own cached record shows a `clearedTime` after the detection. Anything else is emitted as a
  command with no alarm and a `causeUnresolved` reason (the cached lifecycles are not a complete ledger of an alarm's cycles), and a
  cause of kind `RULE`, or none, names no alarm at all. A complete alarm read older than the detection is followed by a newer one
  within a poll, so the command waits for that; with no complete alarm read at all it does not wait, and is emitted unresolved and
  tied to its alarm when the reads come back. A cause is resolved once per new alarm read, never per frame
  (`DirectorObserver.CauseResolutions`), a cause that cannot be read (no kind, no usable `detectedAt`) costs the link and is
  reported, never the command's own states, and a cycle once named is not moved by the alarm's later life. `detectedAt` carries up to
  nine fractional digits; it is cut (never rounded up) to the 100 ns this runtime holds, so it still meets an alarm stored at
  microseconds. The alarm is looked for in the run's alarm read, which is the read the page's trigger comes from: an alarm that
  read does not hold cannot start the page either.
- **What shows.** The derate caption needs the command (`derate`, `SUCCESSFUL`, `max_kw` 11) bound to the triggering alarm's identity
  and cycle, and "Cooler now. Alarm cleared." needs the same command; without the link both stay unmet and the page shows "Running
  hot. Alarm raised." Page 4's `start-pump sent` rail node binds the same way.
- **The page's timing on a real day.** The first live day with the join in place bound the command to its alarm and still showed
  only "Alarm raised." The rule averages a minute of readings, so the alarm is raised about forty seconds after the overheating
  starts at 14:00, and the scene sees it, and the command a moment later, some 45 s after the page is staged: in the page's second
  shot. The derate caption belongs to the first shot and was offered for 40 s from that shot's start, so its offer had run out four
  seconds before there was anything to say. The first shot now holds its offer for 80 s (`holdMaxS`). The last shot holds its own
  for 80 s too and the page's window runs to 16:00: the alarm clears on a minute's average at about 15:25 and the scene reads
  that some ten seconds after the platform's time for it, which on the second live day was 0.7 s after a 40 s offer had run out.
  "Running hot. Alarm raised." carries `unlessShown: derate`: a
  caption is no longer offered once the stronger one it stands in for has been shown in that presentation of the page
  (`StoryCaption.UnlessShown`, which must name another claim of the page). On a live day the page reads "Running hot. Alarm
  raised.", then "Running hot. The platform tells it to derate.", then "Cooler now. Alarm cleared."; without the link it keeps to
  the first. The authored timeline the other director tests run (alarm at 14:00 sharp) shows what it did.
- **Who can read a cause.** The platform serves a command's cause to a reader that holds `device:read` and `alarm:read`
  (`location:read` too for a rule that fires on position) and is not scoped to a customer; to any other reader it is `null`, and the
  scene falls back. The scene reads with the token the runner serves, a tenant-admin member, which is all of that.
  `hack/harborpulse-rig.sh run` now checks each sim's served token before the day starts and fails the run on one that could not
  read a cause: any `scp` claim, authorities that are not a list of strings, or a list without the three and without `*`. Its
  offline controls feed the check each of those, which it must refuse.
- **A control.** `-hp-control-no-cause` makes a live player read commands without their cause (`DirectorCommandsNoCause`). The banner
  turns to `TEST CONTROL: CONTROL command cause not read`, and every claim that needs the link must fall back.
- An instance older than the cause (before v1.9) refuses the command read's `cause` selection: the commands probe fails, which the
  banner reports, and the pump and derate claims stay unmet for the run.

`DirectorTestsCause` pins: the bound command's identity and cycle; nine ways an `ALARM` cause does not bind (another device record,
no originator, another key, no alarm, raised after the detection, cleared before it and at it, no device id, no alarm key), each
reported unresolved with the command still evidence; a `RULE` cause and no cause; the wait for an alarm read that reaches the
detection; an earlier cycle counting only when it was seen cleared after the detection; a bound cycle surviving a re-raise;
nanosecond instants; a command with no alarm read to resolve it against, emitted and later tied; a cause with no kind or no usable
time; one resolution per alarm read over thirty frames; and, from the observer's records through the storyboard's own predicates,
the derate caption holding with the link and "Running hot. Alarm raised." holding without it. Three more run page 3b through the
scheduler on a real day's timing: with the link the three captions in order (and the weaker one never after the stronger, when
both are read in one frame), without it "Alarm raised." alone, and under U7's 40 s the derate caption absent, as the live day
showed. `ControlsChangeOnlyDeclaredEmittedOutputs("command-cause-not-read")` runs a whole authored day without the link: only page
3b's two caption slots and page 4's `start-pump sent` node change.

### The six

- **The storm seen from the 9:16 HOME** (`Shaders/HarborHaze.hlsl`, `HarborSquall`). That frame looks east from 230 m up and held one
  grey field from the horizon to the near shore. Below the horizon the rain's haze now carries squalls: narrow streaks that lean with
  the wind, in ranks one behind another, up to a third darker than the haze between them, which is a little paler than it was. The
  eastern uplands keep a ghost of their shape in the rain when seen from above (`HarborScenery.shader`). Seen edge-on, from the 16:9
  HOME and the quays, the ranges fade as they did; the squalls show there too, in the band under the horizon.
- **37 of 37 from the 9:16 HOME.** The ferry terminal's hall was 56 m long and its south bay stood between that eye and the Basin's
  second charger, the one the derate page is about. It is 42 m (`NearLandmarks.TerminalW`) with its north gable where it was
  (`TerminalOffsetX`), so the charger shows over open quay. The meter stays inside its building.
- **A tor that is a landmark** (`Outcrop.Landmark`, `WorldPropPlan.PlaceLandmark`). At the back of the meadow on the town's east edge,
  830 m from the 16:9 HOME's eye: a main stack of six slabs 25 m high, a second of three and a third of two beside it, a table stone
  and clitter, on a ledge 40 m by 28 m. From HOME it is about 55 pixels tall at 1080 lines (U7's tallest tor was about 15). The lowest
  slab of a stack comes up through the ground like every other block; each one above starts 0.9 m inside the slab it stands on. A
  block's wall is now two quads that meet along a line, dark below and lit above: the lit band used to be laid 3 cm over the dark
  one, which on a tall twisted wall sank through it and at HOME's range was under the depth buffer's step. It is outside the 9:16
  HOME's frame, which looks along the coast.
- **The storm's deck** (`HarborSky.shader`). A raining deck's relief is two close slates: the lit tone, which stood in it as pale
  patches, is cut to a third of its strength by the rain.
- **The wells' view.** A charging bay is never placed in front of a wet well's cut-away (`WorldPropPlan.InWellView`: the strip from
  the well to where the view's eye stands). East Quay's first charger had its bay there, and its canopy stood between the eye and the
  well; the bay is now on the other side of its charger, and East Quay's well is seen from the same stand-off as the other two
  (16 m off, 10 m up). All three look at a point at the datum, under the deck (`PresentationMap.WellViewLookY`), so the well and its
  level tags stand higher in the frame: "low cut-off" is 34 % of the way up a 16:9 frame, above the caption's box (the lower 8-24 %),
  where it was 20 % up and written across the caption. The attribute rule's tag hangs 0.8 m under its own ring; at the low cut-off's
  level the two tags were one smear. In a 9:16 frame the caption's box is higher (23-37 %) and "low cut-off" is still inside it.
- **07:06** (`DayLighting.EarlySkyDrain`, `EarlyWallDrain`). Until the sun stands about 30 degrees up, the sky's light on flat ground
  gives up most of its colour and the horizon's light on the walls half of it. A quay's top in the 16:9 HOME still read (75, 82, 93)
  at 07:06 and reads (81, 80, 82); 06:24 and noon are unchanged at (82, 67, 62) and (102, 104, 110).
- **The derate page's own view.** A tide gauge's mast on the quay edge stood between the eye and the Basin's second charger in the
  page's close and insert shots; the eye steps 14 m towards the quay's north end (`PresentationMap.Sidestep`).

`U8Tests` pins the landmark's height, its place in the 16:9 HOME frame and its stacking; the sight lines from the 9:16 HOME to both
Basin chargers past the terminal (and that the same check sees U7's hall hide the second); no bay in any well's view, and the view's
strip being the line from the well to the eye; the level tags above the caption's box and the far rim under the banner (and that the
same check sees U7's overlap); and the ratio of blue to red in the light through the morning, without a step.

## U9 four things U8 left short, one it keeps, and the ground

### The five

- **The wet well in a 9:16 frame** (`PresentationMap.Tall`, `StoryPoses.TallSuffix`). A 9:16 frame writes its caption higher (the lower
  23-37 %) and runs the rail's nodes across the whole width from 60 % up. From the 16:9 eye "low cut-off" was written inside the
  caption's box, the attribute rule's tag ran off the frame's edge and the well stood behind the rail. A well's view now has a version
  of its own for a tall frame: the same line of sight from twice as far and twice as high (`WellViewTallScale`). The well and all four
  tags stand between the caption and the rail, the lowest line of text 2 % of the frame above the caption's box; the last 1.5 % of the
  well's far rim is under the rail's lower node. In a 1080 x 1920 frame the text is 0.89 of the size, in pixels, that the 16:9 view
  gives it in a 1920 x 1080 one. Whether the frame is tall is decided once (`HarborpulseApp.TallFrame`: `-hp-portrait`, or a frame
  drawn taller than wide) for the camera, the HUD and the tags; U8 asked the flag for the camera and the aspect or the flag for the
  HUD. `DirectorCamera` then takes the tall version, as it takes the 9:16 HOME; every other pose is its own tall version. The tags are text drawn over everything, so they are drawn only for a camera that frames the well:
  within 24 m, or, in a tall frame, within 42 m by a camera that looks down by 20 degrees or more (`WellFootprint.TagRangeTallM`,
  `TagTallDownward`). The HUD's caption box and rail rows are now named in `HudModel`, where the test reads them.
- **Two tors, and their joints** (`Outcrop.JointShare`, `WorldPropPlan.EastTor`, `WestTor`). U8 lit the upper 58 % of what shows of
  each slab and left the rest at half that value, and one slab in five was drawn dark and wet or mossed: from HOME the stack was a
  striped tower. A slab's joint is now a shadow a sixth of the slab high, at 0.64 of the rock's tone, and the wall above it one face
  that runs from 0.80 at its foot to 0.97 under its top; a landmark's walls keep to the mid and light greys. The 9:16 HOME looks east
  along the coast and never held that tor, so it has one of its own: a lesser sister 21 m high (`Outcrop.WestTorRiseM`) in the wood
  west of the Basin, 360 m in front of that eye, the same stacks laid out the other way round with a few low ledges at its foot. It is
  about 120 pixels tall of 1920 and stands under the caption's box. It is outside the 16:9 HOME's frame; from the Basin's quay it
  shows over the trees beside the terminal.
- **Squalls for an eye that stands high** (`HarborSquallEye` in `Shaders/HarborHaze.hlsl`, `SkyState.SquallEyeFromM`). From 235 m,
  the 9:16 HOME, the band the squalls lie in is a fifth of the frame and they read as ranks of rain. From the 16:9 HOME's 100 m and
  from a quay the same band is two degrees deep and holds the ranges, and the streaks were smudges across them. They are drawn in full
  for an eye above 190 m and not at all under 120 m (the eye's height, taken without its sign: the planar reflection's camera is the
  eye mirrored under the water). The 16:9 storm is what U7 drew under the horizon, with U8's even deck over it.
- **The derate page's wide view** (`PresentationMap.BasinWideSidestepM`). U8 stepped the page's eye 14 m north to clear a gauge's
  mast, which put the wide view's eye, 52 m off the wall, over the berthed ferry: its cabin roof and mast filled the lower third. The
  wide view alone now stands 34 m south of the chargers' line, off the ferry's stern, and looks north-west: both chargers in the
  middle with the terminal behind them, the quay's steps at the left and the berth's pontoon at the right. The close and insert views
  keep U8's step. The berth's plan is named once (`NearLandmarks.FerryOffM` and its neighbours) for the builder and the test.
- **The derate page's end on a day with no link: tried, and ruled out** (`Scheduler.Advance`). A caption's offer outlives its shot:
  page 3b's derate caption is offered for 80 s from the page's first shot and is usually read in the second. The page's last shot
  waits for every open offer, so on a day when the link never comes (the command refused, or its cause not read) it stands on "Alarm
  raised." until the first shot's offer runs out, about 15 s after a clean day has left. U9 first made the last shot wait only for
  the offers it made itself, which ended those days on the clean day's second, and a live day confirmed it. But once "Alarm raised."
  is up in the last shot, a day whose link is about to be read and a day whose link never comes look the same, so that rule also
  dropped the derate caption for a command read after the last shot's minimum. The derate caption is the page's headline claim and
  outranks the tidy: the scheduler is as U8 left it, the goldens are unchanged, and the 15 s stays.
  `ALinkReadAfterTheLastShotHasStoodItsTimeIsStillShown` pins the claim and
  `ADayWithNoLinkHoldsTheLastShotUntilTheFirstShotsOfferRunsOut` its price.

### The ground

What grows at a point (wood, meadow, yard, worn earth, a rock's skirt, the coast's turf) is weighed at each corner of the terrain's
grid and blended across the facet (`TerrainBuilder.Build`). U8 weighed it once per facet, and only the coast's turf at the corners:
wherever wood met meadow the grid showed as 20 and 30 m squares of one colour beside squares of another, plainest from above, which is
the 9:16 HOME's whole foreground. A facet still has one normal, so the knolls keep their facets. The forest floor is a shade lighter
((0.235, 0.265, 0.165) from (0.20, 0.23, 0.14)): where the wood is thin and seen from above it was a black field the trees stood in.

`U9Tests` pins the tall view (every tag's line between the caption and the rail, both ends of the attribute rule's tag inside the
frame, and that the same checks see the 16:9 eye fail all three in a tall frame); that only a well's view has a tall version and only
it draws the tags in a tall frame; the wide view (no point of the ferry in the frame, the pontoon right of both chargers, nothing on
the quay's edge in front of either, and that the same check sees U8's ferry); a derate command read late in the last shot still
captioned, and the no-link page's end with the first shot's offer; the west tor's place in the 9:16 HOME and out of the
16:9 one, its stacking and its being its sister's mirror; the joint's share and tones; and the squalls' eye heights against every pose.

## U10 the six that hold their own session

The simulator's S6 slice provisions six devices the scene did not draw: two chargers that hold an MQTT session under a device token,
two that hold it under a client certificate, and two lamps that ask the platform for their commands over HTTP. The presentation is
authored for an exact count of anchors (`PresentationMap.DeviceCount`, 37 until now) and refuses any other, so the simulator kept the
six out of the layout file and its test pinned them as provisioned but not drawn. They are drawn now: the layout carries 43 devices,
that test's undrawn set is gone, and the first page's caption reads "43 devices, one platform." The second tenant of the S7 slice is
another tenant's fleet and stays out of this scene.

### Three kinds of their own

The binder resolves one device type to a scene kind and requires that type's profile to define the metrics the kind reads. The six
are three device types whose profiles report less than the district's chargers and street lights do (a charging power; a dim level),
so the layout names three kinds, `mqtt-charger`, `cert-charger` and `pull-light` (`DeviceKind`, and `harborpulse_s6_scene.go` in the
simulator), and none of the six is read or counted as a district charger or a street light: the rollout still has six chargers and
the campaigns twelve lamps.

- **A charger with its own session** (`LinkedChargerBoard`) reports its charging power and nothing else. The scene says three things
  of it: reporting, silent (its last report is older than the platform's 45 s silence window), or never heard. A car stands in its
  bay only while it reports more than 0.5 kW. Why a silent one is silent is the platform's to say: the first charger on a token drops
  its session from 12:00 to 13:00 and the platform's connectivity rule raises `mqtt-charger-disconnected` on it, which the scene
  draws as that alarm's beacon; the second charger on a certificate is refused from 14:00 and simply falls silent.
- **A pull lamp** (`PullLampState`) reports its dim level and not whether it is lit. Four pips on its post show the level it last
  reported, one for each quarter the level reaches into (all four at 100 %, two at the 35 % it fetches at 10:00), by day and by night,
  and none once the report is stale. The lantern is the scene's own: it burns with the dusk, as a lamp on a photocell does, at the
  reported level and only while the report is current. The device card says which of the two is the platform's.

### Where they stand

All six are on North Quay, the area the manifest gives them, and the scene checks that against the platform's membership as it does
for every device (a membership that names a side quay moves the chargers to a row of their own behind that wall and the lamps to a
landing near its southern end).

- **The chargers continue North Quay's charging row** at its 9 m pitch (`PresentationMap.LinkedBay`): the district's two bays, the
  two on a token, one bay's width left to the fourth street light, then the two on a certificate.
- **The pull lamps flank a landing at the wall's east end** (`PullLampFromEndM`, `PullLampDepthM`), 3 m behind the wall and 12 m
  apart, east of every charger and street light on the quay.

Nothing that stood on the quay or in the town moved for them. Measured once against U9's tree: every registered footprint (430),
every prop with its place, turn and size (12 585, the forest's trees among them), every district anchor and every story pose hash to
the same SHA-256 in both. What the suite keeps is the reason it holds: `TheSixDisplaceNothingThatStoodOnTheQuayOrInTheTown` builds
the world again with the six standing somewhere else on the quay and compares every registered footprint, so no scenery is placed
round them, and its control stands them by the town, where they do move a house lot.

### Read apart at a glance

- **On a token** (`LinkedChargerBuilder`): a teal post under a small pitched hood, a whip aerial on the hood's ridge, three signal
  bars on its door, and a bar under the eave that is cyan while it reports.
- **On a certificate**: the same post in navy, its hood ridged in brass, a brass shield with a keyhole on its door, and a seal at
  the gable's apex that is gold while it reports.
- **Silent**, either shows violet on its lamp and its screen, the colour a silent street light has; never heard, both are dark.
- **A pull lamp** (`PullLampBuilder`): a galvanised post two thirds the height of a street light's column, its own solar panel
  tilted south above it, a battery box strapped to the post, the board of four pips facing the water, and a small lantern on a
  short arm.

The hood is also what both HOME frames hold. A bare post of this size is 7 pixels high from the 9:16 HOME's 780 m, under the 8 pixel
floor the HOME check counts from; with the hood each charger is 10 to 18.

### Ground laid for them

`QuayFittings` plans, from the same anchors the devices are drawn at, what is laid on the quay round them, and
`QuayFittingsBuilder` draws that plan and nothing else.

- **Under each pair of chargers**: a court of granite setts with a stone kerb, asphalt in each bay between its painted lines, a
  hatched panel between the two bays, and two short yellow posts in front of each pedestal.
- **Behind each pair**: a feeder cabinet banded in its chargers' colour (the pair on a token has an uplink mast beside it, the pair
  on a certificate a brass plate and hasp), a chequer-plate trench from the cabinet to each pedestal, and a feeder trench from the
  cabinet back to the kerb of the quay road. The cabinets are ground props and are registered as such; no trench runs under
  anything that stands on the quay.
- **At the pull lamps**: a landing of darker setts from the coping's inland edge, a ladder head on the wall between the two lamps
  with timber fenders down the wall's face, and a lifebuoy on a post. The wall's railing now stops at a post either side of a ladder
  head (`QuayFittings.RailOpeningHalfM`); the other quays' railings are the single runs they were. No trench is laid to these
  lamps: a lamp that asks for its commands is on no feeder and no signal cable, and the ground says so.

### HOME, measured

`HarborStills.HomeCheckFromCommandLine`, preview at 12:30: 16:9 water 39.05 %, sky 20.07 %, 42 of 43 devices (the truck is in its
depot, as before); 9:16 water 15.62 %, sky 19.41 %, 43 of 43. Water and sky are U9's to the second decimal. At 12:30 the first
charger on a token is inside its dropped hour, so its mask includes its alarm beacon.

### A rig's fixed eye

`-hp-eye x/y/z/lookX/lookY/lookZ/fov` holds a live or preview player's camera at one pose for the whole run, whatever shot the
director calls, while the director, its captions and its recording run as they would. It is for a rig that has to watch one place
through a day (U10's live day watched the six with `-hp-eye 72/20/20/72/3/88/50`). A malformed eye stops the scene with a message:
seven finite numbers, each coordinate within 100 km of the origin, the eye at least a centimetre from what it looks at, a field of
view of 5 to 120 degrees. A browser build's page address cannot carry the option.

`U10Tests` pins the six in the layout under three kinds and three device types; the row, the landing and both HOME frames holding
them; the world built with the six on either side quay; nothing displaced, with its control; each court's cabinet, trenches and
apron, and that no trench runs under a footprint (with a planted trench that does); the landing, its ladder clear of the wall's
bollards (with a planted ladder that is not), and no trench to it; what each kind reads and never reads; reporting, silent and
never heard on the 45 s window; the pips for every quarter; the preview's day for the six, hour by hour; and the fixed eye's
parsing with every malformed form refused.

### Their page in the guided tour

Page `3c` of `Resources/storyboard.json` is the tour's stop at these six. It is a live page: neither recorded cut uses it. Its
trigger is the platform clearing the `mqtt-charger-disconnected` alarm, which happens when the charger that dropped its MQTT
session at 12:00 reconnects at 13:00, so on a day when the session never drops the page is not shown. It uses poses that already
exist (`pose:charger-north:wide` for the two chargers on a token, three of the `pose:gauge-north` family for the lamps and the
chargers on a certificate), and a test projects each shot's subjects into a 16:9 frame.

| Shot | From | Says | On what |
| --- | --- | --- | --- |
| `3c-1` | the trigger | "At noon a charger's MQTT session dropped. The platform raised an alarm.", then "It is back. The alarm clears itself." | claims: the raise and the clear of the alarm that triggered the page |
| `3c-2` | 13:18 | "Two lamps here keep no connection. They ask the platform for orders.", then "The order to dim waited for them. Both now report the new level." | the second is a claim: both lamps' own `dim_pct` readings at the level the 10:00 command set |
| `3c-3` | 13:36 | "These chargers sign in with their own certificates. No shared secret.", "This morning their power limit was halved, as a setting, not a command.", "One applied it, one declined, and each reported which." | the scenario's word |
| `3c-4` | 13:54 | nothing | a wide view to wait on |
| `3c-5` | after page 3b | "One certificate was disabled at two. That charger alone is locked out." | the scenario's word |

Three of the five lines about the chargers on a certificate are the scenario's word and not claims, because the director has no
read that shows them: it reads a credential only for a recorded replacement, and it does not read a device's configuration. The
simulator's own verifier judges both (`config-applied`, `charger-x509-admitted`).

The certificate is disabled at 14:00, the beat on which page 3b (the hot connector) starts, and page 3b is in the five-minute
cut at the clean day's times. So page `3c` ranks below it and is waiting on a shot with no caption when 14:00 comes: page 3b takes
the camera on its own second with nothing cut off, and `3c-5` follows when it is done. By then the charger has stood silent for
longer than the 45 s window and is drawn so. `DirectorTestsStoryboard` pins this: every other page's shots and captions are the
same to the second with and without page `3c`, on a clean day and on the two days that keep page 3b up longest.

## U5b replay and cut rendering

The player renders the storyboard's authored cuts (`cuts.c90`, `cuts.c300` in `storyboard.json`) from a recorded live day, with no network:

```bash
Harborpulse.exe -hp-resolution 1920x1080 -hp-replay <recording directory or compact file> -hp-cut c90 \
  -hp-render-dir <frames> -hp-render-fps 30
ffmpeg -framerate 30 -i <frames>/frame-%06d.png -c:v libx264 -crf 17 -pix_fmt yuv420p -movflags +faststart harborpulse-c90.mp4
```

Rendering is offline: every frame is one step of the frame rate whatever the machine's speed (`Time.captureFramerate`), the show clock is
the frame's number, and the player quits after the last frame. Beside the PNGs it writes `render.json` (every segment, its recorded span
and its rate), `frame_check.json` (every caption the cut authored: shown or not, the recorded second, and the event keys of the records
behind each claim) and `ui_text.txt` (every distinct string drawn). Without `-hp-render-dir` the cut plays on screen.

`Model/Director/RecordedDay` loads a recording once (observations into one `ObservationStore`, `/status` samples as the clock);
`ReplaySource` is the scene's `IHarborSource` at one recorded instant; `CutPlayer` is the cut's director. It selects nothing: the cut
lists its shots and when each caption appears. It decides one thing per caption: whether the recording supports it, with the same
predicates the live director uses, read at the recorded instant the caption appears.

- A caption appears at its authored instant, or as soon after it as the recording supports it while its hold still fits in the shot.
- Captions authored for the same instant and slot are alternatives, strongest first.
- A claim the recording does not support is not shown. `frame_check.json` says so, and when (if ever) the day would have supported it,
  which is what a cut's spans are authored against.
- A segment whose rate is not 1 carries a rate chip; a shot borrowed out of order carries its disclosure.
- At each shot the observed model is rebuilt and led in over the previous half minute of the recording, so alarms, command history and
  pump start evidence stand as they did.

What a recording does not hold stays absent. A recording made before the `/status` samples carried them has no alarm severities (such
an alarm is drawn as the weakest one, one ring) and no truck fixes (the truck stands at the depot with "no fix"). The reported
`well_alarm_m` attribute is not recorded.

**A cut's spans are authored against one recorded day.** `Dotnet~/Tests/RecordedDayTool.cs` is the authoring aid:

```bash
cd demos/harborpulse-unity/Dotnet~
HARBORPULSE_RECORDING=<recording directory> HARBORPULSE_COMPACT=<day.ndjson> \
  dotnet test -c Release --filter FullyQualifiedName~RecordedDayTool --logger "console;verbosity=detailed"
```

It prints each caption's fate in both cuts and writes the day as one compact stream (a live recording writes about seven coverage rows
a frame; merged they are a few thousand rows, so the compact file is the one to hand the player). The committed spans fit the clean day recorded on 2026-11-25, whose reduced copy is `Dotnet~/Tests/Fixtures/day-20261125.ndjson.gz`;
`RecordedDayCutTests` pins both cuts' captions over it. (`day-20261124.ndjson.gz` is the first recorded day, made before a recording
sampled severities and truck fixes; it keeps the "absent stays absent" path under test.) A recording whose last row was cut off by a
crash loads without that row.

### Context lines, rain and the cleared tick

Each storyboard page may carry a `context` line ("North Quay · the storm drain pump and its well"), drawn under the top bar while the
page is up, live and in a cut. It says where the viewer is and what is in frame. It is orientation, never evidence: the loader refuses
a platform verb or a digit in it, as it does in a `pre` caption, so it cannot say what the platform did.

The rain is `Model/RainField`: every drop falls through the world at its own terminal speed (8 to 10 m/s), pushed by the storm's wind,
and is drawn as a streak one exposure long where it is inside a box around the eye. It is a pure function of the wave clock, so a
replay at any frame rate draws the same drop in the same place; a stronger rain draws more drops and never moves one. (The first rain
was one sheet of streaks that repeated every few metres and wrapped: in light rain it fell further per frame than its own repeat, and
read as standing still or climbing.)

A cleared alarm's green tick turns to the eye (`AlarmBeacons.TickYawDeg`), so it reads left to right from every side of its device.
