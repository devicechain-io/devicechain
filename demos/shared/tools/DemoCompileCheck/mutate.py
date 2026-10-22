# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
"""Breaks a copy of the demo sources in one way compile-check.sh exists to catch.

  mutate.py <case> <copy-root>

Used by `compile-check.sh --self-test`. Every case first checks that the text it is about to change is there,
and fails if it is not, so that a rename in the sources turns the case into an error instead of into a
mutation that changes nothing and a check that is then shown to "pass" on it.
"""
import os
import sys


def edit(path, old, new, what):
    with open(path, encoding="utf-8", newline="") as f:
        text = f.read()
    if old not in text:
        sys.exit("self-test fixture is stale (%s): %r is not in %s" % (what, old, path))
    with open(path, "w", encoding="utf-8", newline="") as f:
        f.write(text.replace(old, new, 1))


def write(path, text):
    with open(path, "w", encoding="utf-8", newline="") as f:
        f.write(text)


def main(case, root):
    sp = os.path.join(root, "demos/sitepulse-unity/Assets/Sitepulse")
    kit = os.path.join(root, "demos/shared/io.devicechain.demo-platform/Runtime")
    if case == "missing-reference":
        # Replay stops listing the recording assembly it uses types from: a compile error
        edit(os.path.join(sp, "Scripts/Replay/DeviceChain.Sitepulse.Replay.asmdef"),
             '"DeviceChain.Demos.Recording",', "", case)
    elif case == "replay-reaches-network":
        # compiles; only the boundary rule can see it
        edit(os.path.join(sp, "Scripts/Replay/DeviceChain.Sitepulse.Replay.asmdef"),
             '"DeviceChain.Sitepulse.Recording",', '"DeviceChain.Sitepulse.Recording",\n        "DeviceChain.Demos.Platform",', case)
    elif case == "shared-lists-demo":
        # compiles; only the boundary rule can see it
        edit(os.path.join(kit, "Overlay/DeviceChain.Demos.Overlay.asmdef"),
             '"references": []', '"references": ["DeviceChain.Sitepulse"]', case)
    elif case == "shared-reaches-demo":
        write(os.path.join(kit, "Platform/Leak.cs"),
              "namespace DeviceChain.Demos.Platform\n{\n"
              "    internal static class Leak { internal static object Of() => DeviceChain.Sitepulse.Domain.FreshnessRule.FreshWithin; }\n}\n")
    elif case == "left-behind-namespace":
        edit(os.path.join(sp, "Scripts/DevicePlane/DeviceFleet.cs"), "using DeviceChain.Demos.Core;", "", case)
    elif case == "test-missing-using":
        edit(os.path.join(sp, "Tests/EditMode/ObservedSourceTests.cs"), "using DeviceChain.Demos.Platform;", "", case)
    elif case == "unity6-shim-stale":
        # a Unity-6-only call that no longer exists in the source: the shim table must say so, not keep a dead entry
        edit(os.path.join(sp, "Scripts/Visuals/FrameTimeBenchmark.cs"), "timing[0].cpuMainThreadFrameTime", "timing[0].cpuFrameTime", case)
    else:
        sys.exit("unknown case " + case)


if __name__ == "__main__":
    if len(sys.argv) != 3:
        sys.exit(__doc__)
    main(sys.argv[1], sys.argv[2])
