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
devices (37 of 37) and the measurement and alarm streams. Within a few seconds each quay's tide line shows
its gauges' median and how many contributed, and the lights switch with the platform's reports (off by day,
on from 18:00, and East Quay's go silent through its feeder outage, 19:45 to 21:15).

With the director off (`D`), presets: `0` overview, `1`-`3` the quays, `4` an East Quay gauge, `5` the promenade, `6` North Quay pump,
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
east-facing Basin, mapped truck road and fence rings, depot, breakwater lights, town terraces and ridge.
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
six ridge terraces at heights `22 + 28*i` m and z=`265 + 48*i` m; church spire top 123 m and clock
roof top 117 m. Verify rig occlusion and staff separation, cut-away apertures, outfalls, the mapped road
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
