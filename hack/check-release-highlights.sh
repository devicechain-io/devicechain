#!/usr/bin/env bash
# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
#
# Is .github/release-highlights.json valid for the tag being released?
#
#   hack/check-release-highlights.sh <tag> [highlights.json]
#   hack/check-release-highlights.sh --publish-safety [highlights.json]
#   hack/check-release-highlights.sh --self-test
#
# The highlights are the one part of the published release manifest a machine cannot
# derive — generated from commit subjects they would read like `fix(events): give
# payload rows and anchors identities of their own`, which is a developer's sentence,
# not a feature bullet. So a human writes them, and the failure mode of a human-written
# file in a release pipeline is that it is FORGOTTEN, publishing the previous release's
# highlights against this release's version. Confidently wrong is worse than absent.
#
# 🔴 WHY THIS IS A SCRIPT RATHER THAN TWO INLINE CHECKS. It had been written twice — once
# in the release workflow's `guard` job and once in build-release-manifest.sh — with a
# comment on the second saying the repetition was deliberate, so that running the script
# by hand could not quietly produce a mismatched manifest. That reasoning was right and
# the duplication still cost a release run: relaxing the rule to accept a release
# CANDIDATE was applied to the workflow copy only, so `v0.11.0-rc.1` passed the first job,
# built every image, passed the load gate, and then died in publish-manifest against the
# copy nobody had changed. Two guards that must agree are one guard with two callers.
#
# THE RULE. A release candidate carries the same highlights as the release it is a
# candidate for, so `v0.11.0-rc.1` is satisfied by highlights written for `v0.11.0`.
# Demanding an exact match would force the file to be edited to the rc version and then
# edited back, and the failure mode of that dance is shipping the STABLE release with
# `-rc.N` still in the manifest — the precise outcome this guard exists to prevent. The
# BASE version must still match exactly, so a forgotten update is caught on the rc, which
# is the earliest anyone could catch it.
#
# THE THEME. `theme` is the phrase that names the release; it leads the GitHub release
# notes and it reaches the published manifest. It is checked here for the same reason the
# highlights are: it is hand-written, so it is forgettable, and an absent one degrades into
# a release notes header with an empty title line rather than into an error. The cap is a
# character count and nothing more — a guard can tell a phrase from a paragraph, and cannot
# tell a good phrase from a bad one. That part is a human's job and the file says so.
#
# 🔴 NO ADR REFERENCES IN `theme` OR `highlights[]`. Both are PUBLISHED — verbatim into the
# GitHub release body and into devicechain.io/releases.json — and ADRs live in a private
# repo, so a citation there is a dead reference dressed as a source for every reader who
# gets it. hack/check-docs-adr-refs.sh draws that line for the docs site, npm, Artifact Hub
# and nuget; this file is the same line on a surface that guard was never pointed at, and
# its header names this one so the inventory stays readable from either end. `_comment` is
# exempt and is the reason the rule lives here rather than in a `grep -r` over the file:
# the block is maintainer prose, it is not published, and it cites ADRs on purpose.
#
# WHY THIS RULE HAS A SECOND ENTRY POINT. Everything else here is answerable only against a
# TAG, so the guard proper runs in the release workflow and on PRs only as --self-test. The
# ADR rule needs no tag, and catching a citation when the highlights are WRITTEN beats
# catching it when they are RELEASED, so `--publish-safety` runs the published-text rules
# alone and CI calls it on every PR. It is the same function in both paths, deliberately:
# two guards that must agree are one guard with two callers, which is the lesson the header
# above already paid for once.
set -euo pipefail

# The width of a git commit subject. A title that does not fit in one is a sentence, and a
# sentence about what changed belongs in `highlights`, where there is already a list of them.
THEME_MAX_CHARS=72

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

# Prints one `field: text` line per published field citing an ADR; empty output passes.
#
# The match is jq's, case-insensitive, on a word boundary: `adr-080` in a hastily lowercased
# sentence is the same dead citation as `ADR-080`, while `\b` keeps a word that merely ENDS
# in those letters from tripping it. Only .theme and .highlights[] are read — ._comment is
# maintainer prose that never leaves the repo.
#
# 🔴 IT FAILS CLOSED. jq's own failure is REPORTED rather than swallowed: `2>/dev/null ||
# true` around a scan turns a typo'd program into a clean bill of health, which is the one
# outcome a guard must never produce. A jq error prints a line here, and any line at all is
# a finding to every caller.
adr_refs() { # <highlights.json>
  jq -r '
      [{field: "theme", text: (.theme // "")}]
    + ((.highlights // []) | to_entries
       | map({field: ("highlights[" + (.key | tostring) + "]"), text: .value}))
    | .[]
    | select(.text | test("\\bADR-"; "i"))
    | .field + ": " + .text
  ' "$1" || echo "could not read $1 for ADR references"
}

# Prints one line per problem; empty output is the passing case.
check() {
  local tag="$1" hl="$2"
  local base="${tag%%-*}"

  if [ ! -f "$hl" ]; then
    echo "highlights file not found: $hl"
    return 0
  fi

  local version
  version="$(jq -r '.version // empty' "$hl" 2>/dev/null || true)"
  if [ -z "$version" ]; then
    echo "$hl has no .version"
    return 0
  fi
  if [ "$version" != "$tag" ] && [ "$version" != "$base" ]; then
    echo "$hl is for '$version' but this release is '$tag'"
    return 0
  fi

  # SHAPE BEFORE CONTENT. `highlights` is published as a JSON array of strings under
  # schemaVersion 1, and every rule below assumes that: `length` answers for an object and
  # for a string, so an object of highlights counts as 1 and sails past the emptiness check
  # into the manifest, and a non-string element makes the ADR scan fail with a message that
  # names the wrong problem. Asked here, once, it gets its own diagnosis instead.
  local shape
  shape="$(jq -r '
      if (.highlights | type) != "array" then
        "has .highlights of type " + (.highlights | type) + ", not an array"
      elif ((.highlights | map(type != "string") | any)) then
        "has a non-string entry in .highlights"
      else empty end
    ' "$hl" || echo "could not be read as JSON")"
  if [ -n "$shape" ]; then
    echo "$hl $shape"
    return 0
  fi

  # A file carrying the right version and no content is the same forgotten-update
  # failure wearing a different hat.
  local count
  count="$(jq -r '.highlights | length' "$hl" 2>/dev/null || echo 0)"
  if [ "$count" -eq 0 ]; then
    echo "$hl carries no highlights for $tag"
    return 0
  fi

  # Trimmed in jq, so a theme of three spaces is the empty one it actually is rather than a
  # non-empty string that renders as a blank heading.
  #
  # The LENGTH is jq's too, deliberately. `${#theme}` counts characters in a UTF-8 locale and
  # BYTES in the C locale, so a theme with an em-dash in it would measure three longer on a
  # runner with LC_ALL=C than on the laptop it was written on — a gate whose verdict depends
  # on the caller's environment. jq counts codepoints wherever it runs.
  local theme len
  theme="$(jq -r '(.theme // "") | gsub("^\\s+|\\s+$";"")' "$hl" 2>/dev/null || true)"
  if [ -z "$theme" ]; then
    echo "$hl has no .theme — the one phrase that names $tag"
    return 0
  fi
  # A theme is ONE LINE OF TEXT, and the two ways it stops being that are both invisible in
  # the file. A newline renders its tail as a body paragraph under the heading rather than as
  # part of the title; three zero-width spaces render as the blank heading the emptiness
  # check above exists to prevent, because `\s` trims NBSP but not ZWSP and `length` counts
  # them as three perfectly good characters. \p{Cc} is the first (measured: it matches \n,
  # \r and \t) and \p{Cf} the second (U+200B, U+200D). This also refuses an emoji joined
  # with ZWJ, which is intended — a release title is text.
  if jq -e '(.theme // "") | test("\\p{Cc}|\\p{Cf}")' "$hl" >/dev/null 2>&1; then
    echo "$hl has a .theme containing a control or invisible character; a theme is one line of plain text"
    return 0
  fi
  len="$(jq -r '(.theme // "") | gsub("^\\s+|\\s+$";"") | length' "$hl" 2>/dev/null || echo 0)"
  if [ "$len" -gt "$THEME_MAX_CHARS" ]; then
    echo "$hl has a .theme of $len characters (cap $THEME_MAX_CHARS): '$theme'"
    return 0
  fi

  # Joined onto one line: this ends up in a ::error:: annotation, and a multi-line one is
  # rendered as its first line plus silence.
  local adr
  adr="$(adr_refs "$hl" | paste -sd';' -)"
  if [ -n "$adr" ]; then
    echo "$hl cites an ADR in text that is published: $adr"
    return 0
  fi
}

if [ "${1:-}" = "--publish-safety" ]; then
  HL="${2:-$ROOT/.github/release-highlights.json}"
  [ -f "$HL" ] || { echo "::error::highlights file not found: $HL" >&2; exit 1; }
  refs="$(adr_refs "$HL" | paste -sd';' -)"
  if [ -n "$refs" ]; then
    echo "::error::$HL cites an ADR in text that is published to the release notes and to devicechain.io: $refs. Say the thing itself, or link to a public page." >&2
    exit 1
  fi
  echo "ok: nothing published from $(basename "$HL") cites an ADR"
  exit 0
fi

if [ "${1:-}" = "--self-test" ]; then
  echo "==> Self-test: the guard must accept a candidate and still reject a stale file"
  tmp="$(mktemp -d)"
  trap 'rm -rf "$tmp"' EXIT
  hl="$tmp/hl.json"

  # <version> [highlights] [theme] — a theme is written by default so every case below
  # tests the rule it names and not the one added last.
  write() {
    printf '{"version":"%s","breaking":false,"highlights":%s,"theme":"%s"}\n' \
      "$1" "${2:-[\"a\"]}" "${3-a theme}" > "$hl"
  }
  expect() { # <label> <tag> <want: ok|fail>
    local out; out="$(check "$2" "$hl")"
    if [ "$3" = "ok" ] && [ -n "$out" ]; then
      echo "  FAIL: $1 — expected acceptance, got: $out" >&2; exit 1
    fi
    if [ "$3" = "fail" ] && [ -z "$out" ]; then
      echo "  FAIL: $1 — expected rejection, got silence" >&2; exit 1
    fi
    echo "  ok: $1"
  }

  write v0.11.0
  expect "a stable tag matching its highlights"            v0.11.0      ok
  expect "an rc satisfied by the release's highlights"     v0.11.0-rc.1 ok
  expect "a DIFFERENT release is rejected"                 v0.12.0      fail
  # A patch inheriting the previous minor's highlights is the forgotten update, and the
  # base-version rule must not launder it: v0.11.1's base is v0.11.1, not v0.11.0.
  expect "a patch cannot inherit stale highlights"         v0.11.1      fail

  write v0.11.0-rc.1
  expect "an rc file matching its own rc tag"              v0.11.0-rc.1 ok
  # 🔴 The dangerous direction, and the whole reason the rule is base-aware rather than
  # prefix-loose: the STABLE release must not ship a manifest that still says -rc.N.
  expect "a stable tag REJECTS leftover rc highlights"     v0.11.0      fail

  write v0.11.0 '[]'
  expect "the right version with no highlights"            v0.11.0      fail

  # The theme rules. An absent one and a blank one are the same failure — the release notes
  # would lead with an empty line — so both have to be rejected, and the key being present
  # must not be enough on its own.
  printf '{"version":"v0.11.0","breaking":false,"highlights":["a"]}\n' > "$hl"
  expect "no .theme key at all"                            v0.11.0      fail

  write v0.11.0 '["a"]' ''
  expect "an empty theme"                                  v0.11.0      fail

  write v0.11.0 '["a"]' '   '
  expect "a whitespace-only theme"                         v0.11.0      fail

  # The cap, from both sides. Rejecting the long one proves the bound exists; accepting the
  # one that is exactly at it proves the bound is where it says it is and not one short.
  # 72 is written out rather than read from $THEME_MAX_CHARS on purpose: a fixture built
  # from the constant moves with it, so it would pass just as happily if the cap were
  # silently changed to 10 — which is the edit this pair exists to catch.
  at_cap="$(head -c 72 /dev/zero | tr '\0' x)"
  write v0.11.0 '["a"]' "$at_cap"
  expect "a theme exactly at the cap"                      v0.11.0      ok
  write v0.11.0 '["a"]' "${at_cap}x"
  expect "a theme one character past the cap"              v0.11.0      fail

  # The published-text rule. An ADR citation in either published field is refused; the same
  # citation in _comment is not, and that pair is the whole rule — a guard that rejected
  # both would make the maintainer prose unwritable, and one that rejected neither would be
  # a grep that never fires.
  write v0.11.0 '["a"]' 'named by ADR-080'
  expect "an ADR reference in the theme"                   v0.11.0      fail

  write v0.11.0 '["fine","see ADR-025 for why"]'
  expect "an ADR reference in a highlight"                 v0.11.0      fail

  write v0.11.0 '["fine","see adr-025 for why"]'
  expect "a lowercased ADR reference in a highlight"       v0.11.0      fail

  printf '{"_comment":["Secured at the broker (ADR-025)."],"version":"v0.11.0","breaking":false,"highlights":["a"],"theme":"a theme"}\n' > "$hl"
  expect "an ADR reference in _comment, which is NOT published" v0.11.0 ok

  # The word-boundary half of the pattern: a word that merely ends in those letters is not
  # a citation, and a guard that says it is gets switched off by whoever hits it first.
  write v0.11.0 '["the quadr-encoder is supported"]'
  expect "a word ending in adr- is not a citation"         v0.11.0      ok

  # Shape. An object of highlights has a `length` of 1, so without this it clears the
  # emptiness check and is published as an object under a schema that promises an array.
  printf '{"version":"v0.11.0","breaking":false,"highlights":{"a":"clean"},"theme":"a theme"}\n' > "$hl"
  expect "highlights as an object, not an array"           v0.11.0      fail
  printf '{"version":"v0.11.0","breaking":false,"highlights":["ok",7],"theme":"a theme"}\n' > "$hl"
  expect "a non-string entry in highlights"                v0.11.0      fail

  # One line of plain text. Both of these are invisible in the file and visible only in the
  # rendered heading: the newline's tail becomes a paragraph, the zero-width spaces become
  # the blank title the emptiness rule exists to prevent.
  write v0.11.0 '["a"]' 'line one\nline two'
  expect "a theme containing a newline"                    v0.11.0      fail
  write v0.11.0 '["a"]' 'a\tb'
  expect "a theme containing a tab"                        v0.11.0      fail
  write v0.11.0 '["a"]' 'a\rb'
  expect "a theme containing a carriage return"            v0.11.0      fail
  printf '{"version":"v0.11.0","breaking":false,"highlights":["a"],"theme":"\\u200b\\u200b\\u200b"}\n' > "$hl"
  expect "a theme of three zero-width spaces"              v0.11.0      fail
  # THE COUNTERWEIGHT. The characters a real theme is made of must survive the rule, or it
  # gets relaxed by the first person who writes one with an em dash in it.
  write v0.11.0 '["a"]' 'devices must name the dispatch they are answering'
  expect "the theme this release actually ships"           v0.11.0      ok
  write v0.11.0 '["a"]' 'an em — dash and デバイス'
  expect "an em dash and non-Latin text"                   v0.11.0      ok

  rm -f "$hl"
  expect "a missing file"                                  v0.11.0      fail

  # 🔴 THE SCAN MUST FAIL CLOSED, and this is the only place that can prove it. adr_refs
  # reports jq's own failure instead of swallowing it — `2>/dev/null || true` around a scan
  # turns a broken program into a clean bill of health, which is the one verdict a guard may
  # never invent. Reverting that one line leaves every other case green, because the shape
  # rule above now refuses a non-string highlight BEFORE the scan is reached: through
  # check() the jq-error path is unreachable by construction, which is the right structure
  # and also the reason the property has to be asserted directly on the function.
  printf '{ not json at all\n' > "$hl"
  if [ -z "$(adr_refs "$hl" 2>/dev/null)" ]; then
    echo "  FAIL: adr_refs reported nothing on a file jq cannot read — errors are being swallowed" >&2; exit 1
  fi
  printf '{"version":"v0.11.0","breaking":false,"highlights":[1],"theme":"a theme"}\n' > "$hl"
  if [ -z "$(adr_refs "$hl" 2>/dev/null)" ]; then
    echo "  FAIL: adr_refs reported nothing on a highlight it cannot scan as text" >&2; exit 1
  fi
  echo "  ok: the scan reports what it could not read instead of passing it"

  # The second entry point, exercised as a PROCESS rather than as a function: CI calls it
  # this way, and an entry point that parses its arguments wrongly fails open by printing
  # `ok` on a file it never read. It runs the scan ALONE — no shape rule stands in front of
  # it there — so it is also where the fail-closed property is observable end to end.
  printf '{"version":"v0.11.0","breaking":false,"highlights":["clean"],"theme":"a theme"}\n' > "$hl"
  if ! "${BASH_SOURCE[0]}" --publish-safety "$hl" >/dev/null 2>&1; then
    echo "  FAIL: --publish-safety rejected a clean file" >&2; exit 1
  fi
  printf '{"version":"v0.11.0","breaking":false,"highlights":["clean"],"theme":"named by ADR-080"}\n' > "$hl"
  if "${BASH_SOURCE[0]}" --publish-safety "$hl" >/dev/null 2>&1; then
    echo "  FAIL: --publish-safety accepted a published ADR citation" >&2; exit 1
  fi
  printf '{"version":"v0.11.0","breaking":false,"highlights":[1],"theme":"a theme"}\n' > "$hl"
  if "${BASH_SOURCE[0]}" --publish-safety "$hl" >/dev/null 2>&1; then
    echo "  FAIL: --publish-safety accepted a highlight it could not scan as text" >&2; exit 1
  fi
  printf '{ not json at all\n' > "$hl"
  if "${BASH_SOURCE[0]}" --publish-safety "$hl" >/dev/null 2>&1; then
    echo "  FAIL: --publish-safety accepted a file jq cannot read" >&2; exit 1
  fi
  echo "  ok: --publish-safety accepts a clean file and rejects a cited, an unscannable and an unreadable one"

  echo "==> Self-test passed"
  exit 0
fi

TAG="${1:?usage: check-release-highlights.sh <tag> [highlights.json]}"
HL="${2:-$ROOT/.github/release-highlights.json}"

findings="$(check "$TAG" "$HL")"
if [ -n "$findings" ]; then
  echo "::error::$findings. Update .github/release-highlights.json (theme + highlights + breaking) before tagging." >&2
  exit 1
fi

echo "ok: release highlights are valid for $TAG"
