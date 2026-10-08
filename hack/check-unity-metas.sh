#!/usr/bin/env bash
# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
#
# Asserts that a Unity package directory has a COMMITTED .meta for every file and
# folder Unity imports, and no .meta left behind by a file or folder that is gone.
#
#   hack/check-unity-metas.sh <package-dir>     # e.g. demos/shared/io.devicechain.sim-traffic
#   hack/check-unity-metas.sh --self-test       # prove the check can fail
#
# WHY. A .meta carries the asset's GUID, and a source package consumed through a
# `file:` path is imported with whatever .meta files are on disk. A file committed
# without one gets a fresh GUID on every machine that imports it, so references to
# it break between checkouts; an orphan .meta makes the Editor warn on every
# import. Both are made by editing outside the Editor (from a shell, a script or
# another tool), and neither fails any build: dotnet ignores .meta files entirely, and the
# Editor that would have written the .meta was never opened. This makes them fail.
#
# It reads `git ls-files`, not the disk, on purpose: what matters is what a fresh
# clone gets. A .meta the Editor wrote locally but nobody committed counts as
# missing, which is exactly the omission this exists to catch.
#
# What Unity does NOT import, and so needs no .meta (mirrored below): any path
# component that starts with "." (hidden), ends with "~" (e.g. Dotnet~/), or is
# named "cvs"; and files ending in ".tmp". The package directory itself has no
# .meta: it is the package root, not an asset.

set -euo pipefail

usage() {
  echo "usage: $0 <package-dir> | --self-test" >&2
  exit 2
}

# ---------------------------------------------------------------------------
# check <repo-root> <package-dir relative to the root>
# ---------------------------------------------------------------------------
check() {
  local root="$1" dir="${2%/}"
  local listing
  if ! listing="$(git -C "$root" ls-files -- "$dir")"; then
    echo "::error::git ls-files failed for $dir; nothing was checked" >&2
    return 1
  fi
  # An empty enumeration is a wrong path or an untracked package, never a pass.
  if [ -z "$listing" ]; then
    echo "::error::no tracked files under '$dir'; the check examined nothing (wrong path, or nothing committed)" >&2
    return 1
  fi

  local report
  report="$(awk -v dir="$dir" '
    function ignored(rel,   n, parts, i, p) {
      n = split(rel, parts, "/")
      for (i = 1; i <= n; i++) {
        p = parts[i]
        if (p ~ /^\./ || p ~ /~$/ || tolower(p) == "cvs") return 1
      }
      return (rel ~ /\.tmp$/)
    }
    {
      if (index($0, dir "/") != 1) next
      rel = substr($0, length(dir) + 2)
      if (rel == "") next
      # Every enclosing folder below the package root that Unity imports is an asset too,
      # even when the file that puts it in git is one Unity ignores: a folder tracked only
      # through a .gitkeep is imported, and gets a .meta, all the same.
      path = rel
      while ((i = match(path, /\/[^\/]*$/)) > 0) {
        path = substr(path, 1, i - 1)
        if (!ignored(path)) want[path ".meta"] = 1
      }
      if (ignored(rel)) next
      if (rel ~ /\.meta$/) { have[rel] = 1; next }
      want[rel ".meta"] = 1
    }
    END {
      for (m in want) if (!(m in have)) print "missing\t" dir "/" m
      for (m in have) if (!(m in want)) print "orphan\t" dir "/" m
    }
  ' <<<"$listing" | sort)"

  if [ -n "$report" ]; then
    echo "::error::Unity .meta files are out of step with the tracked files in $dir:" >&2
    printf '%s\n' "$report" >&2
    echo "  missing: open the package in the Unity Editor (or a batch-mode run of a project that loads it)" >&2
    echo "           and commit the .meta it writes; orphan: delete the .meta of the file or folder that is gone." >&2
    return 1
  fi
  local count
  count="$(awk 'END { print NR }' <<<"$listing")"
  echo "every Unity asset under $dir has a committed .meta, and no .meta is orphaned ($count tracked files)"
}

# ---------------------------------------------------------------------------
# Self-test. A guard is worth nothing until it has been shown to FAIL, so each
# case below builds a throwaway git repository and asserts the verdict, in both
# directions: a well-formed package passes (the counterweight, so "it failed" is
# not satisfied by a check that fails everything), and every defect is caught.
# ---------------------------------------------------------------------------
self_test() {
  local tmp
  tmp="$(mktemp -d)"
  trap 'rm -rf "$tmp"' RETURN

  local failures=0

  # fixture <name>: a fresh repo holding a well-formed package at pkg/
  fixture() {
    local r="$tmp/$1"
    mkdir -p "$r/pkg/Runtime/Geometry" "$r/pkg/Dotnet~/Proj" "$r/pkg/.hidden"
    : >"$r/pkg/package.json"; : >"$r/pkg/package.json.meta"
    : >"$r/pkg/Runtime.meta"
    : >"$r/pkg/Runtime/Geometry.meta"
    : >"$r/pkg/Runtime/Geometry/Vec2.cs"; : >"$r/pkg/Runtime/Geometry/Vec2.cs.meta"
    : >"$r/pkg/Dotnet~/Proj/Proj.csproj"                   # Unity ignores ~ folders: no .meta wanted
    : >"$r/pkg/.hidden/notes"                              # nor hidden ones
    : >"$r/pkg/.gitattributes"
    mkdir -p "$r/pkg/CVS" "$r/pkg/Runtime/Keep"
    : >"$r/pkg/CVS/Entries"                                # nor a folder named cvs (any case)
    : >"$r/pkg/Runtime/scratch.tmp"                        # nor a .tmp file
    : >"$r/pkg/Runtime/Keep/.gitkeep"                      # a folder tracked only by an ignored file...
    : >"$r/pkg/Runtime/Keep.meta"                          # ...is still imported, so it has a .meta
    git -C "$r" init -q
    git -C "$r" add -A
    echo "$r"
  }

  expect() { # expect pass|fail <description> <repo>
    local want="$1" what="$2" r="$3" got
    if check "$r" pkg >/dev/null 2>&1; then got=pass; else got=fail; fi
    if [ "$got" = "$want" ]; then
      echo "  ok: $what ($got)"
    else
      echo "FAIL: $what: expected $want, got $got" >&2
      failures=$((failures + 1))
    fi
  }

  local r
  r="$(fixture clean)"
  expect pass "a package with every .meta (and ~, hidden, cvs and .tmp content without one)" "$r"

  r="$(fixture missing-file)"
  git -C "$r" rm -q --cached pkg/Runtime/Geometry/Vec2.cs.meta
  expect fail "a file whose .meta is not committed" "$r"

  r="$(fixture missing-folder)"
  git -C "$r" rm -q --cached pkg/Runtime/Geometry.meta
  expect fail "a folder whose .meta is not committed" "$r"

  r="$(fixture missing-kept-folder)"
  git -C "$r" rm -q --cached pkg/Runtime/Keep.meta
  expect fail "a folder holding only an ignored file, whose .meta is not committed" "$r"

  r="$(fixture orphan)"
  git -C "$r" rm -q --cached pkg/Runtime/Geometry/Vec2.cs
  expect fail "a .meta whose file is gone (orphan)" "$r"

  r="$(fixture orphan-folder)"
  : >"$r/pkg/Gone.meta"
  git -C "$r" add pkg/Gone.meta
  expect fail "a folder .meta with no folder (orphan)" "$r"

  r="$(fixture wrong-path)"
  if check "$r" no-such-dir >/dev/null 2>&1; then
    echo "FAIL: a path with no tracked files was reported clean" >&2
    failures=$((failures + 1))
  else
    echo "  ok: a path with no tracked files is refused, not passed"
  fi

  if [ "$failures" -ne 0 ]; then
    echo "self-test FAILED ($failures case(s))" >&2
    return 1
  fi
  echo "self-test passed"
}

[ "$#" -eq 1 ] || usage
case "$1" in
  --self-test) self_test ;;
  -*) usage ;;
  *) check "$(git rev-parse --show-toplevel)" "$1" ;;
esac
