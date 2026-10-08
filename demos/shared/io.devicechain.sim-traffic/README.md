<!--
Copyright The DeviceChain Authors
SPDX-License-Identifier: Apache-2.0
-->

# io.devicechain.sim-traffic

Plain C# building blocks for drawing plausible vehicle and machine traffic in DeviceChain simulations and
demos. It is shared by the Unity demos in this repository, and it builds and tests without Unity.

This package is at its start: today it holds only the 2D vector type
(`DeviceChain.Sim.Traffic.Geometry.Vec2`) and the build, test and CI scaffolding the rest is added to. It is
intended to grow to hold the shapes bodies occupy on the ground, the lanes they drive, and rules for moving
simulated bodies without overlap. None of that is here yet, and nothing here promises it: what those rules
guarantee, and the conditions under which they guarantee it, will be stated in this README when they are
added.

## What it is not

- It is a **simulation library for demos**. It is not for controlling real vehicles, machines or robots, and
  it makes **no safety claim** of any kind.
- It is not a published SDK. It is internal to this repository, consumed by `file:` path, and versioned in
  `package.json` only so a consumer can see that something changed.

## Conventions

- **Ground plane:** `X` is east and `Z` is north, seen from above (Unity's X/Z with Y up), in metres.
- **Headings** are degrees **clockwise from north** (+Z): heading `h` points along `(sin h, cos h)`, and
  `Vec2.Rotate(deg)` turns clockwise by the same convention.
- **Determinism:** same runtime, same inputs, same outputs. Nothing inside reads a clock or a random source.

## Layout

```
package.json
Runtime/                     DeviceChain.Sim.Traffic: no engine references, no dependencies
Testing/                     DeviceChain.Sim.Traffic.Testing: helpers for consumers' tests, no NUnit
                             (constrained to UNITY_INCLUDE_TESTS, so it never reaches a player build)
Tests/Runtime/               NUnit tests, run by BOTH the Unity Test Runner and dotnet
Dotnet~/                     the dotnet build of the same files (Unity ignores folders ending in ~)
```

## Using it from a Unity project

Add it to the project's `Packages/manifest.json` by relative path, and list it under `testables` to see its
tests in the Test Runner:

```json
"io.devicechain.sim-traffic": "file:../../shared/io.devicechain.sim-traffic"
```

Then reference `DeviceChain.Sim.Traffic` from the consuming assembly definition (it is not auto-referenced).

## Building and testing

With the .NET 8 SDK:

```bash
cd demos/shared/io.devicechain.sim-traffic/Dotnet~
dotnet build -c Release
dotnet test -c Release
```

The build compiles `Runtime/` and `Testing/` for `netstandard2.1` and `net8.0`, runs the tests on `net8.0`,
and compiles `Tests/Runtime/` again for `netstandard2.1` against the oldest NUnit 3.x with a .NET Standard
build (`Tests.Compat`), the closest public stand-in for the NUnit 3.5 fork the Unity Test Runner uses.

To run the same tests in Unity without opening another project, use the host project in
`demos/shared/sim-traffic-unity-host` (Unity 6000.5.3f1), in batch mode:

```
Unity.exe -batchmode -nographics -projectPath <repo>/demos/shared/sim-traffic-unity-host
          -runTests -testPlatform EditMode -testResults <file>.xml
```

## Rules for the code

These are what let one set of files compile for Unity (Mono in the Editor, IL2CPP in players) and for dotnet.
Those marked *(checked)* are enforced in CI; the rest are kept by review.

- **C# 9** *(checked)*. `Dotnet~/Directory.Build.props` pins `LangVersion 9`, as Unity compiles, so a newer
  construct fails the dotnet build.
- **No conditional compilation** (`#if`, `#elif`) in `Runtime/`, `Testing/` or `Tests/` *(checked by
  `hack/check-unity-shared-sources.sh`)*. One set of files means one behaviour on every runtime.
- **The NUnit subset both runners share** in `Tests/`: `[Test]`, `[TestCase]`, `Assert.That` with `Is.`/`Has.`
  constraints, `Assert.Ignore`, `[Category]`. Partly checked: async tests (an `async` method, or one returning
  `Task`) are refused by `hack/check-unity-shared-sources.sh`, and an NUnit API newer than 3.6, such as
  `[Timeout]`, fails the `Tests.Compat` compile. Anything else outside the subset that NUnit 3.6 has is kept
  out by review.
- **Every file and folder has a committed `.meta`** *(checked by `hack/check-unity-metas.sh`, which fails on a
  missing or orphaned one)*. Add files, run the host project once (above), and commit the `.meta` files Unity
  writes.
- **AOT-safe:** no reflection, no runtime code generation, no `dynamic`. The `net8.0` build turns on the
  trim and AOT analyzers, which catch some of this but not all of it (for example, a struct that relies on
  the default `ValueType.Equals`, which compares fields by reflection; `Vec2` implements its own), so the
  rest is kept by review.
