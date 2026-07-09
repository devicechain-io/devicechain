#!/usr/bin/env bash
# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
#
# Stage the DeviceChain C# SDK (and its managed dependency closure) into the Unity package's
# Runtime/Plugins/ folder so the Editor can compile against it. The DLLs are build artifacts and are
# gitignored — run this once after cloning (and after any SDK change) before opening the package in
# Unity. See io.devicechain.sdk/VERIFICATION.md for the IL2CPP/WebGL dependency caveats.
set -euo pipefail

here="$(cd "$(dirname "$0")" && pwd)"
sdk_proj="$here/../csharp/src/DeviceChain.Sdk/DeviceChain.Sdk.csproj"
plugins="$here/io.devicechain.sdk/Runtime/Plugins"
tfm="netstandard2.1"   # Unity 2021.3 consumes the .NET Standard 2.1 build

mkdir -p "$plugins"

# CopyLocalLockFileAssemblies forces the transitive NuGet closure (System.Text.Json, …) into bin.
dotnet build "$sdk_proj" -c Release -f "$tfm" -p:CopyLocalLockFileAssemblies=true

out="$here/../csharp/src/DeviceChain.Sdk/bin/Release/$tfm"

# Assemblies Unity already provides — copying them in causes duplicate-definition errors.
skip_re='^(netstandard|mscorlib|System\.Private\.CoreLib|System\.Runtime|System)\.dll$'

echo "Staging DLLs into $plugins"
shopt -s nullglob
copied=0
for dll in "$out"/*.dll; do
  base="$(basename "$dll")"
  if [[ "$base" =~ $skip_re ]]; then
    echo "  skip (Unity-provided): $base"
    continue
  fi
  cp -f "$dll" "$plugins/"
  echo "  + $base"
  copied=$((copied + 1))
done

echo "Staged $copied assemblies. If the Editor reports duplicate assemblies, remove the offending"
echo "DLL from Runtime/Plugins/ (Unity ships it) — see VERIFICATION.md."
