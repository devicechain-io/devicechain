# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
"""Writes one dotnet project per Unity assembly definition, so Unity's assembly boundaries are compiled.

A single project over all the demo sources would compile a script that uses a type from an assembly it does
not reference, and the Editor would then refuse that very script. Moving code between assemblies is how that
mistake is made. So each runtime .asmdef becomes its own netstandard2.1 project, ProjectReference'd along
exactly the `references` the .asmdef lists, and a missing reference is a compile error here as it is there.

  gen.py [--tests] <out-dir> <asmdef-root>...

A .cs file belongs to the nearest .asmdef above it. Not compiled: assemblies restricted to the Editor (their
code is the Editor's own API, which reference assemblies for the player do not carry) and, unless --tests is
given, test assemblies. With --tests they are compiled too, against NUnit and a stub of the few UnityEditor and
Unity test-runner members they touch. Compiling is all this does: nothing is run.

What an assembly name stands for:
  DeviceChain.Sdk.dll                          the C# SDK project in sdks/csharp (precompiled in the Unity project)
  System.Text.Json.dll, Microsoft.Bcl.*.dll    their NuGet packages
  Unity.InputSystem, Unity.RenderPipelines.*   declaration-only stubs (stubs/*.cs): not in UnityEngine.Modules
  UnityEngine.TestRunner, UnityEditor.TestRunner, UnityEditor
                                               the same, for test assemblies (UnityEditor is implicit for an
                                               assembly restricted to the Editor, as in Unity)
  UnityEngine.UI                               a stub referenced by every assembly that references the engine:
                                               the demos use uGUI without listing it (it is part of the engine
                                               in the Unity version they target)
Any other name must be an .asmdef found under a root, or generation fails. A reference this script cannot
resolve is an error, never a skipped line.
"""
import glob
import json
import os
import sys

HERE = os.path.dirname(os.path.abspath(__file__))
SDK_PROJECT = os.path.normpath(os.path.join(HERE, "../../../../sdks/csharp/src/DeviceChain.Sdk/DeviceChain.Sdk.csproj"))

PRECOMPILED = {
    "DeviceChain.Sdk.dll": ("project", SDK_PROJECT),
    "System.Text.Json.dll": ("package", "System.Text.Json", "8.0.5"),
    "Microsoft.Bcl.AsyncInterfaces.dll": ("package", "Microsoft.Bcl.AsyncInterfaces", "8.0.0"),
    "nunit.framework.dll": ("package", "NUnit", "3.13.3"),
}
# asmdef name -> stub file (each becomes an assembly of that name)
STUBS = {
    "Unity.InputSystem": "stubs/InputSystem.cs",
    "Unity.RenderPipelines.Core.Runtime": "stubs/RenderPipelinesCore.cs",
    "Unity.RenderPipelines.Universal.Runtime": "stubs/RenderPipelinesUniversal.cs",
    "UnityEngine.UI": "stubs/UnityUI.cs",
    "UnityEngine.TestRunner": "stubs/TestRunner.cs",
    "UnityEditor.TestRunner": "stubs/EditorTestRunner.cs",
    "UnityEditor": "stubs/UnityEditor.cs",
}
IMPLICIT = ["UnityEngine.UI"]
IMPLICIT_EDITOR = ["UnityEditor"]

# The demos target Unity 6; the reference assemblies on NuGet stop at 2021.3 (UnityEngine.Modules has no later
# version). A handful of calls are Unity-6-only. Rather than leave their files out of the check, each is
# compiled from a copy with that one expression swapped for its 2021.3 spelling. A rewrite that no longer
# matches fails generation, so an entry cannot outlive the line it covers.
#   file name -> [(exact text, replacement)]
COMPAT = {
    "FrameTimeBenchmark.cs": [
        ("FindObjectsByType<IotOverlay>(FindObjectsInactive.Include)", "FindObjectsByType<IotOverlay>(FindObjectsSortMode.None)"),
        ("timing[0].cpuMainThreadFrameTime", "timing[0].cpuFrameTime"),
        ("FindObjectsByType<QuarryFleetPreview>()", "FindObjectsByType<QuarryFleetPreview>(FindObjectsSortMode.None)"),
    ],
    "IotOverlay.cs": [
        ("fenceLine.textureScale = new Vector2(", "_ = new Vector2("),
    ],
    "OverlayExtras.cs": [
        ("t.line.textureScale = new Vector2(", "_ = new Vector2("),
    ],
}


def fwd(p):
    return p.replace(os.sep, "/")


def nearest_owner(path, asmdir_list):
    best = None
    for d in asmdir_list:
        if path.startswith(d + os.sep) and (best is None or len(d) > len(best)):
            best = d
    return best


def rewrite(src, out):
    base = os.path.basename(src)
    with open(src, encoding="utf-8-sig", newline="") as f:
        text = f.read()
    for old, new in COMPAT[base]:
        if old not in text:
            sys.exit("COMPAT rewrite for %s no longer matches: %r (remove or update the entry)" % (base, old))
        text = text.replace(old, new)
    dest = os.path.join(out, "compat", base)
    os.makedirs(os.path.dirname(dest), exist_ok=True)
    with open(dest, "w", encoding="utf-8", newline="") as f:
        f.write(text)
    return dest


def load(path):
    with open(path, encoding="utf-8-sig") as f:
        return json.load(f)


def main(out, roots, tests):
    out = os.path.abspath(out)
    os.makedirs(out, exist_ok=True)
    asmdefs = {}
    for root in roots:
        for p in glob.glob(os.path.join(root, "**", "*.asmdef"), recursive=True):
            rel = fwd(os.path.relpath(p, root))
            if any(part.endswith("~") or part.startswith(".") for part in rel.split("/")):
                continue
            d = load(p)
            if d["name"] in asmdefs:
                sys.exit("duplicate assembly definition name %s: %s and %s" % (d["name"], asmdefs[d["name"]]["path"], p))
            asmdefs[d["name"]] = {"path": os.path.abspath(p), "dir": os.path.dirname(os.path.abspath(p)), "def": d}

    def is_test(a):
        return "UNITY_INCLUDE_TESTS" in a["def"].get("defineConstraints", [])

    def skipped(a):
        if is_test(a):
            return not tests
        return a["def"].get("includePlatforms") == ["Editor"]

    runtime = {n: a for n, a in asmdefs.items() if not skipped(a)}
    if not runtime:
        sys.exit("no runtime assembly definitions found under %s" % roots)

    # every other .asmdef directory below a directory is excluded from that directory's glob
    all_dirs = [a["dir"] for a in asmdefs.values()]
    projects = []
    used_compat = set()
    for name, a in sorted(runtime.items()):
        d = a["def"]
        refs = []
        engine = not d.get("noEngineReferences", False)
        for r in d.get("references", []):
            if r in runtime:
                refs.append(("project", os.path.join(out, r + ".csproj")))
            elif r in STUBS:
                refs.append(("project", os.path.join(out, "stub." + r + ".csproj")))
            elif r in asmdefs:
                sys.exit("%s references %s, an Editor-only or test assembly: not valid for a runtime assembly" % (name, r))
            else:
                sys.exit("%s references %s, which is no assembly definition under the roots and no known stub" % (name, r))
        for pr in d.get("precompiledReferences", []):
            if pr not in PRECOMPILED:
                sys.exit("%s lists precompiled reference %s, which this check does not know" % (name, pr))
            if PRECOMPILED[pr]:
                refs.append(PRECOMPILED[pr])
        excludes = [
            fwd(os.path.relpath(o, a["dir"])) + "/**"
            for o in all_dirs
            if o != a["dir"] and o.startswith(a["dir"] + os.sep)
        ]
        lines = [
            '<Project Sdk="Microsoft.NET.Sdk">',
            "  <PropertyGroup>",
            "    <TargetFramework>netstandard2.1</TargetFramework>",
            "    <LangVersion>9.0</LangVersion>",
            "    <Nullable>disable</Nullable>",
            "    <EnableDefaultCompileItems>false</EnableDefaultCompileItems>",
            "    <Configurations>Desktop;WebGL</Configurations>",
            "    <ProduceReferenceAssembly>false</ProduceReferenceAssembly>",
            "    <GenerateAssemblyInfo>false</GenerateAssemblyInfo>",
            "    <AssemblyName>%s</AssemblyName>" % name,
            "    <IsPackable>false</IsPackable>",
            "    <DcAssemblyProject>true</DcAssemblyProject>",
            "  </PropertyGroup>",
            "  <PropertyGroup Condition=\"'$(Configuration)' == 'WebGL'\">",
            "    <DefineConstants>$(DefineConstants);UNITY_WEBGL</DefineConstants>",
            "  </PropertyGroup>",
        ]
        # a define gated on a package version: every package such a define names is present in the demo projects
        defines = [v["define"] for v in d.get("versionDefines", [])]
        if defines:
            lines.append("  <PropertyGroup><DefineConstants>$(DefineConstants);%s</DefineConstants></PropertyGroup>" % ";".join(defines))
        lines.append("  <ItemGroup>")
        if engine:
            lines.append('    <PackageReference Include="UnityEngine.Modules" Version="2021.3.33" PrivateAssets="all" />')
            for imp in IMPLICIT + (IMPLICIT_EDITOR if d.get("includePlatforms") == ["Editor"] else []):
                lines.append('    <ProjectReference Include="%s" />' % fwd(os.path.join(out, "stub." + imp + ".csproj")))
        listed = ["UnityEngine.Modules"] if engine else []
        for r in refs:
            if r[0] == "project":
                lines.append('    <ProjectReference Include="%s" />' % fwd(r[1]))
            else:
                lines.append('    <PackageReference Include="%s" Version="%s" PrivateAssets="all" />' % (r[1], r[2]))
                listed.append(r[1])
        # the only packages this assembly compiles against (Directory.Build.targets drops the rest)
        for pkg in listed:
            lines.append('    <DcListedPackage Include="%s" NuGetPackageId="%s" />' % (pkg, pkg))
        inc =fwd(os.path.join(a["dir"], "**", "*.cs"))
        compat = []
        for src in glob.glob(os.path.join(a["dir"], "**", "*.cs"), recursive=True):
            base = os.path.basename(src)
            owner = nearest_owner(src, all_dirs)
            if base in COMPAT and owner == a["dir"]:
                compat.append(src)
                used_compat.add(base)
        exc_items = [fwd(os.path.join(a["dir"], e)) for e in excludes] + [fwd(c) for c in compat]
        exc = ";".join(exc_items)
        lines.append('    <Compile Include="%s"%s />' % (inc, (' Exclude="%s"' % exc) if exc else ""))
        for src in compat:
            lines.append('    <Compile Include="%s" />' % fwd(rewrite(src, out)))
        lines += ["  </ItemGroup>", "</Project>"]
        with open(os.path.join(out, name + ".csproj"), "w", newline="\n") as f:
            f.write("\n".join(lines) + "\n")
        projects.append(name + ".csproj")

    # every COMPAT entry names a Sitepulse source: when Sitepulse is among the roots each must have been applied
    stale = set(COMPAT) - used_compat
    if stale and any(n.startswith("DeviceChain.Sitepulse") for n in asmdefs):
        sys.exit("COMPAT entries for files that were not found in any compiled assembly: %s" % sorted(stale))
    used_stubs = set()
    for name in list(runtime):
        for r in runtime[name]["def"].get("references", []):
            if r in STUBS:
                used_stubs.add(r)
        if not runtime[name]["def"].get("noEngineReferences", False):
            used_stubs.update(IMPLICIT)
            if runtime[name]["def"].get("includePlatforms") == ["Editor"]:
                used_stubs.update(IMPLICIT_EDITOR)
    for s in sorted(used_stubs):
        proj = "stub.%s.csproj" % s
        lines = [
            '<Project Sdk="Microsoft.NET.Sdk">',
            "  <PropertyGroup>",
            "    <TargetFramework>netstandard2.1</TargetFramework>",
            "    <LangVersion>9.0</LangVersion>",
            "    <EnableDefaultCompileItems>false</EnableDefaultCompileItems>",
            "    <Configurations>Desktop;WebGL</Configurations>",
            "    <ProduceReferenceAssembly>false</ProduceReferenceAssembly>",
            "    <GenerateAssemblyInfo>false</GenerateAssemblyInfo>",
            "    <AssemblyName>%s</AssemblyName>" % s,
            "  </PropertyGroup>",
            "  <ItemGroup>",
            '    <PackageReference Include="UnityEngine.Modules" Version="2021.3.33" PrivateAssets="all" />',
            '    <Compile Include="%s" />' % fwd(os.path.join(HERE, STUBS[s])),
            "  </ItemGroup>",
            "</Project>",
        ]
        with open(os.path.join(out, proj), "w", newline="\n") as f:
            f.write("\n".join(lines) + "\n")

    # one obj/bin per project: they share a directory, and restore writes project.assets.json under obj/
    props = [
        "<Project>",
        "  <PropertyGroup>",
        # Unity compiles an assembly against the assemblies its .asmdef LISTS and no others: a reference is not
        # inherited from the assembly it names. MSBuild passes references on by default, which would let an
        # assembly use a type from an assembly only its neighbour lists. Neither a project reference nor a
        # package reference (PrivateAssets, on each) is passed on here.
        "    <DisableTransitiveProjectReferences>true</DisableTransitiveProjectReferences>",
        "    <BaseIntermediateOutputPath>$(MSBuildThisFileDirectory)obj/$(MSBuildProjectName)/</BaseIntermediateOutputPath>",
        "    <OutputPath>$(MSBuildThisFileDirectory)bin/$(Configuration)/$(MSBuildProjectName)/</OutputPath>",
        "  </PropertyGroup>",
        "</Project>",
    ]
    with open(os.path.join(out, "Directory.Build.props"), "w", newline="\n") as f:
        f.write("\n".join(props) + "\n")

    # The same rule for packages. The SDK is a ProjectReference to sdks/csharp, and NuGet hands a project the
    # packages of every project it references, PrivateAssets or not on this side: System.Text.Json,
    # Microsoft.Bcl.AsyncInterfaces, MQTTnet and the rest would reach an assembly whose .asmdef lists
    # DeviceChain.Sdk.dll alone, which would then compile here and fail in the Editor. So an assembly compiles
    # against the packages its .asmdef lists (precompiledReferences, and UnityEngine.Modules for the engine) and
    # no others. The stubs are not assemblies of the demos and keep what they restore.
    targets = [
        "<Project>",
        "  <Target Name=\"DcListedPackagesOnly\" AfterTargets=\"ResolvePackageAssets\" Condition=\"'$(DcAssemblyProject)' == 'true'\">",
        "    <ItemGroup>",
        "      <_DcUnlisted Include=\"@(ResolvedCompileFileDefinitions)\" />",
        "      <_DcUnlisted Remove=\"@(DcListedPackage)\" MatchOnMetadata=\"NuGetPackageId\" />",
        "      <ResolvedCompileFileDefinitions Remove=\"@(_DcUnlisted)\" />",
        "    </ItemGroup>",
        "  </Target>",
        "</Project>",
    ]
    with open(os.path.join(out, "Directory.Build.targets"), "w", newline="\n") as f:
        f.write("\n".join(targets) + "\n")

    build = ['<Project DefaultTargets="Build">', "  <ItemGroup>"]
    for p in projects:
        build.append('    <Asm Include="%s" />' % p)
    build += [
        "  </ItemGroup>",
        '  <Target Name="Build">',
        '    <MSBuild Projects="@(Asm)" Properties="Configuration=$(Configuration)" BuildInParallel="false" />',
        "  </Target>",
        "</Project>",
    ]
    with open(os.path.join(out, "build.proj"), "w", newline="\n") as f:
        f.write("\n".join(build) + "\n")
    print("generated %d assembly projects (+%d stubs): %s" % (len(projects), len(used_stubs), ", ".join(p[:-7] for p in projects)))


if __name__ == "__main__":
    args = sys.argv[1:]
    tests = "--tests" in args
    args = [a for a in args if a != "--tests"]
    if len(args) < 2:
        sys.exit(__doc__)
    main(args[0], args[1:], tests)
