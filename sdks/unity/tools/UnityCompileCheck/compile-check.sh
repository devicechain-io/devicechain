#!/usr/bin/env bash
# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
#
# Tier-1 verification for the Unity package: compile its Runtime + Sample C# against real UnityEngine
# reference assemblies, in BOTH platform branches — the desktop (`#else`, ClientWebSocket) branch and
# the WebGL (`#if UNITY_WEBGL`, .jslib P/Invoke) branch. Catches wrong-API calls and branch typos
# without a Unity Editor, license, or GUI. It does NOT run the code — Editor/WebGL behavior is the
# game-ci + browser-vs-cluster job (see io.devicechain.sdk/VERIFICATION.md).
set -euo pipefail

here="$(cd "$(dirname "$0")" && pwd)"
proj="$here/UnityCompileCheck.csproj"

echo "== tier-1: desktop branch (#else / ClientWebSocket) =="
dotnet build "$proj" -c Desktop

echo "== tier-1: WebGL branch (#if UNITY_WEBGL / .jslib P/Invoke) =="
dotnet build "$proj" -c WebGL

echo "tier-1 compile check passed (both platform branches)."
