<!--
Copyright The DeviceChain Authors
SPDX-License-Identifier: Apache-2.0
-->

# sim-traffic-unity-host

A minimal Unity project (6000.5.3f1) whose only job is to load
[`io.devicechain.sim-traffic`](../io.devicechain.sim-traffic) and run its tests in the Unity Test Runner,
without opening a demo project and its full asset import. It has no assets of its own.

```
Unity.exe -batchmode -nographics -projectPath <repo>/demos/shared/sim-traffic-unity-host
          -runTests -testPlatform EditMode -testResults <file>.xml
```

Running it also writes the `.meta` files for any file added to the package; commit those with the change.
