<!--
Copyright The DeviceChain Authors
SPDX-License-Identifier: Apache-2.0
-->

# io.devicechain.demo-platform

The code every DeviceChain Unity demo needs that does not depend on what the demo shows: talking to a
DeviceChain instance, being a device on it, recording a run, and the maths of an overlay of cards. It was
taken out of the Sitepulse demo so that a second demo does not start from a copy of it.

It holds no scene, no art, no simulation and no vocabulary of any one demo. A demo keeps those and hands the
package what it needs as data.

## Using it in a demo

Add it to the demo's `Packages/manifest.json` as a local package (the path is relative to the `Packages`
folder), beside the Unity SDK it is built on:

```json
"io.devicechain.demo-platform": "file:../../shared/io.devicechain.demo-platform",
"io.devicechain.sdk": "file:../../../sdks/unity/io.devicechain.sdk"
```

The SDK is a local package too, so Unity cannot pull it in for you: a project that adds this package adds
the SDK itself. The assemblies below reference `DeviceChain.Sdk.Unity` and the precompiled
`DeviceChain.Sdk.dll`, `System.Text.Json.dll` and `Microsoft.Bcl.AsyncInterfaces.dll` that come with it.

## Layout

```
package.json
Runtime/
  Core/          DeviceChain.Demos.Core          namespace DeviceChain.Demos.Core
  Platform/      DeviceChain.Demos.Platform      namespace DeviceChain.Demos.Platform
  DevicePlane/   DeviceChain.Demos.DevicePlane   namespace DeviceChain.Demos.DevicePlane
  Recording/     DeviceChain.Demos.Recording     namespace DeviceChain.Demos.Recording
  Overlay/       DeviceChain.Demos.Overlay       namespace DeviceChain.Demos.Overlay
```

None depends on a demo. `Core`, `Recording` and `Overlay` cannot see the connection to an instance (none of them
reaches the SDK), which is what lets a demo build its replay from them and from nothing that can touch a network;
`Platform` builds on `Core`, and `DevicePlane` on both.

| Assembly | What is in it |
| --- | --- |
| `Core` | The few types every other layer is made of and none of them may drag the SDK in for: `Redactor` (so a credential or token never reaches a log, a recording or the screen), `CommandStatus` and `CommandState`, the `Sample` a device says (measurements or a location) with `SampleKind` and `GeoPoint`, and `LinkState`. No engine reference. |
| `Platform` | Connecting to an instance: the runner's config and the sign-in token (`RunnerConfig`, `TokenBroker`, `Jwt`, `HandedToken`), resolving credentials (`DeviceCredentials`, `CredentialResolver`), the operator's GraphQL queries (`QueryFn`, `OperatorQueries`, `LastStateQuery`, `ZoneNamesQuery`), observing what the platform recorded (`PlatformObserver`, `ObservedState`, `StreamRunner`, `CommandTracker`, `FreshRun`), and the one log every line of it goes through (`PlatformLog`, via `Redactor`). |
| `DevicePlane` | A device's side: `IDeviceLink` and the SDK-backed `SdkDeviceLink` (and its factory), and `EmittedSampleLog`. |
| `Recording` | The line format of a recorded run: the run header and its clock (`RunHeader`), the observed, device and presenter lines (`RecordLines`), `JsonIo` and the file names and version (`RecordingFormat`). |
| `Overlay` | The maths of an overlay of cards over a 3D scene: where a card sits and how it settles (`CardMotion`: slots, safe area, picking, label placement), the drawer and chip layout (`CalmLayout`), and the layers and zone names it draws (`OverlayLayers`). |

## What a demo supplies

- `PlatformLog.Prefix`: the tag at the start of each log line. The default is `[demo]`; Sitepulse sets
  `[sitepulse]` once, at player start.
- The scene's vocabulary. The package never names a measurement, a command or a device kind. The
  demo's composition root builds what the binder needs from its own vocabulary, so that a key renamed in
  the demo is renamed there by the compiler and not by a second list.
- Its own tests of its own behaviour. The tests of this package's code have not moved here yet: they run
  in the Sitepulse test assembly, which is why `AssemblyInfo.cs` in `Platform` and `DevicePlane` name it
  in `InternalsVisibleTo`. A demo with tests of its own adds its assembly name there.

## What is deliberately not here yet

These stay in Sitepulse because they are written around its two kinds of device (equipment and the
crusher plant) or its machine-pose recording. Making them general is the work of the second demo, which
knows what shape they need to take, and not of the move:

- the device binder and the readiness board (`SceneKind`, `SceneContract`), which check a profile against
  equipment-shaped and plant-shaped contracts;
- the recorder and the replay (`RunRecorder`, `sim.bin`, `ReplayRoot`), which record and replay machine poses;
- the device session host, fleet and inbox, which drive machines through the task layer;
- the overlay's cards themselves (`IotOverlay`), which draw Sitepulse's measurements.

## Conventions

- Plain C# where it can be: in `Platform`, only the log and the runner-config client touch the engine; `DevicePlane`
  uses none; `Core` and `Recording` reference no engine assembly at all; `Overlay` is engine maths (vectors,
  rays, rects) and draws nothing.
- A change that adds a reference from one of these assemblies to a demo, or to a later assembly in the table,
  is a change that cannot be made here.

## Checking it without the Editor

`demos/shared/tools/DemoCompileCheck` compiles every assembly of this package, and every demo assembly that
uses it (tests included), one project per assembly definition, against real UnityEngine reference
assemblies, in both platform branches. A reference an assembly definition is missing is a compile error
there as it is in the Editor. It compiles; it does not run. Opening the demo in the Editor is still the
check that the scenes and prefabs bind.
