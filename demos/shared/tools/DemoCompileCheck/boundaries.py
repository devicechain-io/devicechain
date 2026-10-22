# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
"""Holds the assembly graph of the demos to the boundaries they are built on.

A reference an assembly definition adds is a compile success: the compiler is happy to let a shared
assembly see a demo, or a replay assembly see the network. These are the rules that make such a reference
fail, read from the same .asmdef files Unity reads. A rule is checked on the TRANSITIVE closure, as the
Editor tests check the built assemblies' (the tests of `ReplayAssemblyTests` walk what an assembly reaches),
so a reference smuggled in through a middle assembly is found too.

  boundaries.py --shared <root>... --demo <root>...

  shared  roots hold packages many demos use; their assemblies may reach each other and the SDK, never a demo
  demo    roots hold one demo's assemblies

Exit status 1 and one line per broken rule if any rule is broken; 0 and a summary if none is. A rule that
names an assembly this graph does not contain is itself an error: a rename must not turn a rule into one
that checks nothing.
"""
import glob
import json
import os
import sys

# what reaching the network means: the SDK (and, through it, the MQTT client) and the assemblies built on it
SDK = ["DeviceChain.Sdk.Unity", "DeviceChain.Sdk.dll"]
SHARED_CONNECTION = ["DeviceChain.Demos.Platform", "DeviceChain.Demos.DevicePlane"]

# assembly -> assemblies it must not reach (transitively). Assemblies the rule names but the graph lacks are an error.
FORBIDDEN = {
    # the shared package's layers: the types every layer is made of, the recording format, and the overlay's
    # maths hold nothing that can touch an instance
    "DeviceChain.Demos.Core": SDK + SHARED_CONNECTION,
    "DeviceChain.Demos.Recording": SDK + SHARED_CONNECTION,
    "DeviceChain.Demos.Overlay": SDK + SHARED_CONNECTION,
    # the connection sits above Core and below the device plane
    "DeviceChain.Demos.Platform": ["DeviceChain.Demos.DevicePlane"],
    # Sitepulse: the simulation and domain base, the recording and the replay hold no network object, so
    # a replay can run where no instance exists (the Editor tests walk the same closure)
    "DeviceChain.Sitepulse": SDK + SHARED_CONNECTION + ["DeviceChain.Sitepulse.Platform", "DeviceChain.Sitepulse.DevicePlane", "DeviceChain.Sitepulse.App"],
    "DeviceChain.Sitepulse.Recording": SDK + SHARED_CONNECTION + ["DeviceChain.Sitepulse.Platform", "DeviceChain.Sitepulse.DevicePlane", "DeviceChain.Sitepulse.App"],
    "DeviceChain.Sitepulse.Replay": SDK + SHARED_CONNECTION + ["DeviceChain.Sitepulse.Platform", "DeviceChain.Sitepulse.DevicePlane", "DeviceChain.Sitepulse.App"],
}


def load(path):
    with open(path, encoding="utf-8-sig") as f:
        return json.load(f)


def graph(roots):
    g = {}
    for root in roots:
        for p in glob.glob(os.path.join(root, "**", "*.asmdef"), recursive=True):
            parts = os.path.relpath(p, root).replace(os.sep, "/").split("/")
            if any(x.endswith("~") or x.startswith(".") for x in parts):
                continue
            d = load(p)
            g[d["name"]] = d
    return g


def reach(g, name):
    """Every assembly name reachable from `name` by asmdef references, plus the precompiled ones along the way."""
    seen, stack = set(), [name]
    while stack:
        n = stack.pop()
        d = g.get(n)
        if d is None:
            continue
        for r in d.get("references", []) + d.get("precompiledReferences", []):
            if r not in seen:
                seen.add(r)
                stack.append(r)
    return seen


def main(argv):
    shared, demo, mode = [], [], None
    for a in argv:
        if a in ("--shared", "--demo"):
            mode = a
        elif mode == "--shared":
            shared.append(a)
        elif mode == "--demo":
            demo.append(a)
        else:
            sys.exit(__doc__)
    if not shared or not demo:
        sys.exit(__doc__)

    gs, gd = graph(shared), graph(demo)
    both = set(gs) & set(gd)
    if both:
        sys.exit("assembly definition names in both a shared package and a demo: %s" % sorted(both))
    g = dict(gs)
    g.update(gd)
    problems = []

    # a shared assembly must not reach a demo's
    for name in sorted(gs):
        for r in sorted(reach(g, name) & set(gd)):
            problems.append("%s (shared) reaches %s (a demo's)" % (name, r))

    # the named boundaries
    known = set(g) | {"DeviceChain.Sdk.dll"}
    for name, bad in sorted(FORBIDDEN.items()):
        if name not in g:
            problems.append("a boundary names %s, which no assembly definition under the roots declares" % name)
            continue
        got = reach(g, name)
        for b in bad:
            if b not in known:
                problems.append("the boundary for %s names %s, which is in no graph: a renamed assembly would leave this rule checking nothing" % (name, b))
            elif b in got:
                problems.append("%s reaches %s, which it must not" % (name, b))

    if problems:
        for p in problems:
            print("::error::boundary: " + p, file=sys.stderr)
        return 1
    print("assembly boundaries hold: %d shared and %d demo assemblies, %d rules" % (len(gs), len(gd), len(FORBIDDEN)))
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
