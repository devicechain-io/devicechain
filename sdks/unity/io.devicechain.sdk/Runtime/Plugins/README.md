<!--
Copyright The DeviceChain Authors
SPDX-License-Identifier: Apache-2.0
-->

# Plugins — staged SDK assemblies (not committed)

This folder holds `DeviceChain.Sdk.dll` and its managed dependency closure
(`System.Text.Json`, `System.Threading.Channels`, …). They are **build artifacts**, produced by the
staging script, and are gitignored — do not commit binaries.

Populate them once after cloning (and after any change to `sdks/csharp`):

```bash
sdks/unity/stage-sdk.sh
```

The `DeviceChain.Sdk.Unity` assembly definition references `DeviceChain.Sdk.dll` by name
(`overrideReferences` + `precompiledReferences`), so the Editor needs these present to compile the
Runtime scripts. See the package `VERIFICATION.md` for the IL2CPP/WebGL dependency caveats (some
assemblies Unity already ships and must be pruned).
