#!/usr/bin/env bash
# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
#
# Tier-1 check for the Unity demos and the packages they share, with no Unity Editor, licence or GUI:
#
#   1. the assembly graph is held to its boundaries (boundaries.py): a shared package never reaches a
#      demo, and the assemblies that must be able to run without a network (the replay, the recording
#      format) never reach one;
#   2. every assembly definition is compiled as its own project against real UnityEngine reference
#      assemblies (gen.py), tests included, in BOTH platform branches: the desktop branch, and UNITY_WEBGL.
#
#   compile-check.sh               check the demos
#   compile-check.sh --self-test   prove the check can fail: it runs the check over a clean copy of the
#                                  sources (which must pass) and over copies each broken in a way the check
#                                  exists to catch (each of which must fail)
#
# WHAT IT CATCHES. The sources the Editor would refuse: a call to an API that does not exist, a type
# used from an assembly its .asmdef does not reference (the mistake moving code between assemblies
# makes, and the one a single project over all the sources cannot see), a namespace left behind by a
# move, a test that lost its using. And what the Editor would accept and should not: a boundary crossed.
#
# WHAT IT DOES NOT. It compiles; it runs nothing. Whether a scene or prefab still binds its scripts,
# whether play mode works, and what IL2CPP makes of the code are the Editor's and a player's. The
# stubs for the packages UnityEngine.Modules does not carry (uGUI, the Input System, the Universal
# Render Pipeline, the test framework) declare only what the demos call, so they prove names and
# shapes, not behaviour.
set -euo pipefail

# Paths are handed to python and to dotnet, which on Windows (Git Bash) do not read /c/... paths: use the
# mixed form there. Elsewhere this is the identity.
native() { if command -v cygpath >/dev/null 2>&1; then cygpath -m "$1"; else printf '%s' "$1"; fi; }

here="$(native "$(cd "$(dirname "$0")" && pwd)")"
repo="$(native "$(cd "$here/../../../.." && pwd)")"

# Paths from the repository root. One entry per demo (its Assets folder); a new demo adds its own here.
# The shared packages are the ones several demos use, and the SDK they are built on.
demo_rel=(
  demos/sitepulse-unity/Assets/Sitepulse
)
shared_rel=(
  demos/shared/io.devicechain.demo-intro
  demos/shared/io.devicechain.demo-platform
  sdks/unity/io.devicechain.sdk
)

py() {
  if command -v python3 >/dev/null 2>&1 && python3 -c 'import sys' >/dev/null 2>&1; then python3 "$@"; else python "$@"; fi
}

# check <out-dir> <repo-root>: hold the assembly graph to its boundaries, then compile every assembly in
# both branches. <repo-root> is the repository, or a copy of the sources in the same layout (the self-test).
# Non-zero if any step fails.
check() {
  local out="$1" base="$2" r cfg p
  local demo=() shared=()
  for r in "${demo_rel[@]}"; do demo+=("$base/$r"); done
  for r in "${shared_rel[@]}"; do shared+=("$base/$r"); done
  # Every step ends in `|| return 1`: the self-test calls this inside an `if`, where bash switches `set -e`
  # off for the whole function, so without it only the LAST command's status would decide the result.
  py "$here/boundaries.py" --shared "${shared[@]}" --demo "${demo[@]}" || return 1
  py "$here/gen.py" --tests "$out" "${demo[@]}" "${shared[@]}" || return 1
  # restore is per project: they share one directory but not one obj/
  for p in "$out"/*.csproj; do
    dotnet restore "$p" -nologo -v q || return 1
  done
  for cfg in Desktop WebGL; do
    echo "== compile: $cfg branch =="
    dotnet build "$out/build.proj" -c "$cfg" -nologo -v q --no-restore -clp:ErrorsOnly || return 1
  done
}

self_test() {
  local tmp failures=0 r c
  tmp="$(native "$(mktemp -d)")"
  trap 'rm -rf "$tmp"' RETURN

  # fresh <name>: a copy of every source the check reads (C# and assembly definitions only: the demos'
  # art is large and irrelevant to a compile); prints its root
  fresh() {
    local dest="$tmp/$1" src f rel
    for r in "${demo_rel[@]}" "${shared_rel[@]}"; do
      src="$repo/$r"
      while IFS= read -r f; do
        rel="${f#"$repo"/}"
        mkdir -p "$dest/$(dirname "$rel")"
        cp "$f" "$dest/$rel"
      done < <(find "$src" \( -name '*.cs' -o -name '*.asmdef' \) -not -path '*~/*' -not -path '*/obj/*' -not -path '*/bin/*')
    done
    echo "$dest"
  }

  # expect pass|fail <description> <copy-root> [reason-regex]
  # A failure only counts when the log says why, in the regex's words: a check that fails for any other
  # reason (a restore that broke, a fixture that no longer applies) has not shown it can see the defect.
  expect() {
    local want="$1" what="$2" copy="$3" reason="${4:-}" got
    if (check "$tmp/out-$RANDOM" "$copy") >"$tmp/log" 2>&1; then got=pass; else got=fail; fi
    if [ "$got" != "$want" ]; then
      echo "FAIL: $what: expected $want, got $got" >&2
      tail -15 "$tmp/log" >&2
      failures=$((failures + 1))
    elif [ "$got" = fail ] && ! grep -Eq "$reason" "$tmp/log"; then
      echo "FAIL: $what: it failed, but not for the reason it should (no match for /$reason/)" >&2
      tail -15 "$tmp/log" >&2
      failures=$((failures + 1))
    else
      echo "  ok: $what ($got)"
    fi
  }

  # broken <case> <reason-regex> <description>: a copy broken by `mutate.py <case>` must fail, for that reason
  broken() {
    c="$(fresh "$1")"
    py "$here/mutate.py" "$1" "$c"
    expect fail "$3" "$c" "$2"
  }

  c="$(fresh clean)"
  expect pass "the sources as committed (the counterweight: a check that fails everything proves nothing)" "$c"

  broken missing-reference 'error CS0(234|246)' "an assembly that uses a type from an assembly it no longer references"
  broken left-behind-namespace 'DeviceFleet.cs.*error CS0(103|246)' "a file that lost the using for a namespace it moved to"
  broken test-missing-using 'ObservedSourceTests.cs.*error CS0(103|246)' "a test that lost the using for a type it uses"
  broken shared-reaches-demo 'Leak.cs.*error CS0234' "a shared assembly that uses a demo's type"
  broken replay-reaches-network 'boundary: DeviceChain.Sitepulse.Replay reaches DeviceChain.Demos.Platform' "the replay assembly gaining a reference that reaches the network (it compiles; only the boundary rule sees it)"
  broken shared-lists-demo 'boundary: DeviceChain.Demos.Overlay \(shared\) reaches DeviceChain.Sitepulse ' "a shared assembly listing a demo's assembly as a reference (it compiles; only the boundary rule sees it)"
  broken unity6-shim-stale 'COMPAT rewrite for FrameTimeBenchmark.cs no longer matches' "a Unity-6 shim entry whose line is gone"

  if [ "$failures" -ne 0 ]; then
    echo "self-test FAILED ($failures case(s))" >&2
    return 1
  fi
  echo "self-test passed"
}

case "${1:-}" in
  --self-test)
    self_test
    ;;
  "")
    out="$(native "$(mktemp -d)")"
    trap 'rm -rf "$out"' EXIT
    check "$out" "$repo"
    echo "demo check passed (boundaries hold; both platform branches compile)."
    ;;
  *)
    echo "usage: $0 [--self-test]" >&2
    exit 2
    ;;
esac
