# DeviceChain Demo Platform

The scenario-agnostic half of a DeviceChain Unity demo. A demo supplies its own scene, simulation and
visuals; this package supplies everything between that scene and a DeviceChain instance, so a second demo
does not copy it from the first.

It is a source package, consumed through a `file:` path, like the other packages under `demos/shared`.

## What is in it

Four assemblies, split by what each is allowed to reach:

| Assembly | Namespace | Engine | SDK / network | Holds |
| --- | --- | --- | --- | --- |
| `DeviceChain.Demos.Core` | `DeviceChain.Demos.Platform` | none | no | secret redaction (`Redactor`), the command and provenance vocabulary (`CommandStatus`, `Provenance`, `Observation`), the demo's identity (`DemoIdentity`), and the device-side sample queue (`Sample`, `GeoPoint`, `OutboundRing`, `SamplePump`) |
| `DeviceChain.Demos.Recording` | `DeviceChain.Demos.Platform.Recording` | none | no | the run recording format: the line types of the three ndjson logs, `RunHeader`, `JsonIo`, `RecordingFormat` |
| `DeviceChain.Demos.Platform` | `DeviceChain.Demos.Platform` | yes | yes | the runner config and operator token broker, binding scene devices to platform devices (`DeviceBinder`, `SceneContract`), per-device credentials, the readiness board, the observer (`PlatformObserver`, `ObservedState`) and its queries |
| `DeviceChain.Demos.DevicePlane` | `DeviceChain.Demos.Platform.DevicePlane` | yes | yes | one device's MQTT session over the SDK (`IDeviceLink`, `SdkDeviceLink`), and the acknowledged-sample log |

The first two reach neither the engine nor the network, so a replay or an offline render can link them and
still be shown, by its assembly graph, to be unable to open a connection. Keep it that way: anything that
needs the SDK or a UnityEngine type belongs in `Platform` or `DevicePlane`.

## What a demo supplies

- **Its identity.** Call `DemoIdentity.Configure("my-demo")` before anything logs or binds a device. Log
  lines then read `[my-demo] ...` and the access-token credential created for each device is named
  `{deviceToken}-my-demo`.
- **Its scene contract.** `SceneContract.Kinds` maps each kind of device the scene draws (any string the demo
  chooses) to a `KindContract`: the device type it must resolve to, the numeric and boolean metrics and the
  commands its profile must define, and the command whose parameter schema lists areas. The binder checks the
  platform's answer against it and reports every mismatch by device.
- **Its simulation, visuals and device behaviour.** Not here. Session hosting and command handling are the
  demo's, over `IDeviceLink`.

## Using it

In the demo project's `Packages/manifest.json`:

```json
"io.devicechain.demo-platform": "file:../../shared/io.devicechain.demo-platform",
"io.devicechain.sdk": "file:../../../sdks/unity/io.devicechain.sdk"
```

and list the package under `testables` to run its tests from the demo project. The package needs the
DeviceChain Unity SDK package (`io.devicechain.sdk`); it cannot declare it as a dependency because that
package is not on a registry, so the demo project adds both.

## Tests

- `Tests/Editor` holds EditMode tests that need the engine (the token broker's coroutine refresh, the JSON
  options an IL2CPP player depends on).
- `Tests/Runtime` holds plain NUnit tests for `Core` and `Recording`.
- `Dotnet~/` compiles the `Core` and `Recording` sources, and the `Tests/Runtime` files, with the dotnet SDK
  at C# 9 for `netstandard2.1` and `net8.0`, warnings as errors, and runs the tests:

```bash
cd Dotnet~
dotnet restore --locked-mode
dotnet build --no-restore -c Release
dotnet test --no-build -c Release
```

The `Platform` and `DevicePlane` assemblies are compiled against the UnityEngine reference assemblies by
`sdks/unity/tools/UnityCompileCheck`.
