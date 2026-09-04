#!/usr/bin/env bash
# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
#
# Every `actions/setup-go` in this repo must take its version from `go.work`.
#
# WHY THIS IS A GATE AND NOT A STYLE PREFERENCE.
#
# `setup-go` pins `GOTOOLCHAIN=local`, so whatever version it installs is the
# only one available — Go will not download a newer toolchain to satisfy a
# higher requirement, it fails. And a module's `go.mod` carries the LANGUAGE
# version that module's own code needs, which is a floor and is routinely lower
# than the workspace's: `go.work`'s directive must be >= every module's, and ours
# tracks whatever the embedded Bento library declares.
#
# So a workflow pointed at a module `go.mod` installs a toolchain that `go build`
# inside the workspace then refuses to use. It is not a warning — the build dies
# with `go.work requires go >= X (running go Y; GOTOOLCHAIN=local)`.
#
# That is not hypothetical. Of the 19 setup-go steps across 13 workflow files,
# `ko-base-image.yml` held the ONE pointed at `backend/core/go.mod`. When the Bento bump moved the workspace floor
# from 1.26.5 to 1.26.6, that workflow started failing every week and nothing
# said so: it is a scheduled job whose only consumer is a 40-day liveness check
# in `check-ko-base-pin.sh`, so the base-image pin was frozen for three weeks
# with seventeen days still to run before anything went red.
#
# 🔴 THE CHECK IS EXHAUSTIVE BY CONSTRUCTION, NOT BY LIST. It enumerates every
# `setup-go` step it can find and requires each to name `go.work`, so a workflow
# added tomorrow is covered without anyone remembering this file. A list of
# known workflows would pass forever while the twentieth drifted.
#
# 🔴 IT ASKS THE YAML PARSER, NOT A REGEX. A grep for `go-version-file:` cannot
# tell which `uses:` step the key belongs to, cannot see a `go-version:` literal
# used instead, and is defeated by any of YAML's several ways to write the same
# thing. This repo has already shipped one gate that lexed where it should have
# parsed.

set -euo pipefail

cd "$(git rev-parse --show-toplevel)"

WORKFLOW_DIR=".github/workflows"
ACTIONS_DIR=".github/actions"
REQUIRED_FILE="go.work"

usage() {
  echo "usage: $0 [--self-test]" >&2
  exit 2
}

# ---------------------------------------------------------------------------
# scan: print one `<file>\t<job>\t<verdict>\t<detail>` line per setup-go step
# found under the directory given as $1. Exits non-zero only on a parse error;
# a policy violation is reported as a BAD verdict so the caller decides.
# ---------------------------------------------------------------------------
scan() {
  python3 - "$1" "$2" "$REQUIRED_FILE" <<'PY'
import glob, os, sys

try:
    import yaml
except ImportError:
    print("::error::PyYAML is not available, so this guard cannot parse the "
          "workflows. Refusing to report a pass it did not earn.", file=sys.stderr)
    sys.exit(2)

wf_root, actions_root, required = sys.argv[1], sys.argv[2], sys.argv[3]

files = sorted(glob.glob(os.path.join(wf_root, "*.yml")) +
               glob.glob(os.path.join(wf_root, "*.yaml")))
if not files:
    print(f"::error::no workflow files under {wf_root}", file=sys.stderr)
    sys.exit(2)

# 🔴 Composite actions are scanned too, and this is not thoroughness for its own
# sake. Wrapping nineteen identical setup-go steps in `./.github/actions/setup`
# is the obvious next refactor, and it would move every one of them out of a
# workflow file — leaving this guard reporting a cheerful 0/0 over a repo whose
# Go version had silently drifted. A composite declares its steps under
# `runs.steps` rather than `jobs.*.steps`, so both shapes are walked.
files += sorted(glob.glob(os.path.join(actions_root, "*", "action.yml")) +
                glob.glob(os.path.join(actions_root, "*", "action.yaml")) +
                glob.glob(os.path.join(actions_root, "*", "*", "action.yml")) +
                glob.glob(os.path.join(actions_root, "*", "*", "action.yaml")))

def step_groups(doc):
    """Yield (container-name, steps) for both workflow jobs and composite actions."""
    jobs = doc.get("jobs")
    if isinstance(jobs, dict):
        for job_name, job in jobs.items():
            if isinstance(job, dict):
                yield job_name, job.get("steps") or []
    runs = doc.get("runs")
    if isinstance(runs, dict) and runs.get("using") == "composite":
        yield "runs (composite)", runs.get("steps") or []

found = 0
for path in files:
    with open(path) as fh:
        try:
            doc = yaml.safe_load(fh)
        except yaml.YAMLError as exc:
            print(f"::error::{path} is not parseable YAML: {exc}", file=sys.stderr)
            sys.exit(2)
    if not isinstance(doc, dict):
        continue
    for job_name, steps in step_groups(doc):
        for step in steps:
            if not isinstance(step, dict):
                continue
            uses = step.get("uses") or ""
            # Match the action by name, ignoring the SHA pin and any version
            # comment, so pinning (which is enforced elsewhere) cannot hide a
            # step from this check. Lowercased because GitHub resolves `uses:`
            # case-insensitively, so `Actions/Setup-Go` is the same action and
            # must not slip past by spelling.
            if not uses.split("@")[0].strip().lower().endswith("actions/setup-go"):
                continue
            found += 1
            with_ = step.get("with") or {}
            if not isinstance(with_, dict):
                with_ = {}
            vfile = with_.get("go-version-file")
            vlit = with_.get("go-version")
            # 🔴 `go-version` is checked FIRST and is fatal even alongside a
            # correct `go-version-file`. setup-go resolves the two by taking
            # go-version and warning ("Both go-version and go-version-file
            # inputs are specified, only go-version will be used"), so a step
            # carrying both installs the literal — and an earlier version of
            # this guard reported that step OK because it only looked at the
            # file. A rule that reads the input the action IGNORES is worse
            # than no rule: it certifies the wrong one.
            if vlit is not None:
                verdict, detail = "BAD", f"go-version: {vlit} (a literal; it WINS over go-version-file)"
            elif vfile == required:
                verdict, detail = "OK", required
            elif vfile is not None:
                verdict, detail = "BAD", f"go-version-file: {vfile}"
            else:
                verdict, detail = "BAD", "neither go-version-file nor go-version"
            print(f"{path}\t{job_name}\t{verdict}\t{detail}")

if found == 0:
    # A scan that matches nothing is the failure this whole family of guards
    # exists to avoid: it reads exactly like a clean pass.
    print(f"::error::found no actions/setup-go steps under {wf_root} or "
          f"{actions_root} — the check matched nothing, which is not the same "
          f"as everything being correct.", file=sys.stderr)
    sys.exit(2)
PY
}

# ---------------------------------------------------------------------------
# report: run scan over $1 and fail if anything came back BAD.
# ---------------------------------------------------------------------------
report() {
  local dir="$1" adir="$2" out bad rc=0

  # 🔴 Capture scan's status EXPLICITLY rather than leaning on `set -e`.
  # `set -e` is suppressed for the whole body of a function invoked in a
  # `cmd || handler` context, which is how the self-test calls this — so a
  # failing scan fell straight through to the success message below. The
  # self-test's "matched nothing" case caught exactly that, in this guard,
  # which is the argument for writing the control before trusting the check.
  out="$(scan "$dir" "$adir")" || rc=$?
  if [ "$rc" -ne 0 ]; then
    return "$rc"
  fi

  bad="$(printf '%s\n' "$out" | awk -F'\t' '$3 == "BAD"')"
  local total ok_count
  total="$(printf '%s\n' "$out" | grep -c . || true)"
  ok_count="$(printf '%s\n' "$out" | awk -F'\t' '$3 == "OK"' | grep -c . || true)"

  if [ -n "$bad" ]; then
    echo "::error::a setup-go step does not take its version from ${REQUIRED_FILE}."
    printf '%s\n' "$bad" | while IFS=$'\t' read -r f j _ d; do
      echo "  ${f} (job: ${j}) → ${d}"
    done
    echo
    echo "  setup-go pins GOTOOLCHAIN=local, so the installed toolchain is the only"
    echo "  one available. A module go.mod declares that module's LANGUAGE floor,"
    echo "  which is routinely lower than the workspace's, and \`go build\` inside"
    echo "  the workspace obeys ${REQUIRED_FILE} — so the build dies with"
    echo "  \"${REQUIRED_FILE} requires go >= X (running go Y; GOTOOLCHAIN=local)\"."
    echo "  Use: go-version-file: ${REQUIRED_FILE}"
    return 1
  fi

  echo "  ok: ${ok_count}/${total} setup-go steps take their version from ${REQUIRED_FILE}"
}

# ---------------------------------------------------------------------------
# self_test: the negative control. A gate is worth nothing until it has been
# shown to fail, and each case below breaks exactly one thing.
# ---------------------------------------------------------------------------
self_test() {
  local tmp rc
  tmp="$(mktemp -d)"
  # shellcheck disable=SC2064  # expand tmp now, deliberately
  trap "rm -rf '$tmp'" EXIT
  mkdir -p "$tmp/wf" "$tmp/actions" "$tmp/noactions"

  echo "self-test: case 1 — a correct workflow passes"
  cat > "$tmp/wf/good.yml" <<'EOF'
name: good
on: [push]
jobs:
  build:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v5
      - uses: actions/setup-go@abcdef1234567890abcdef1234567890abcdef12 # v7
        with:
          go-version-file: go.work
EOF
  if report "$tmp/wf" "$tmp/actions" >/dev/null; then
    echo "  ok: a correct workflow passes"
  else
    echo "  FAIL: a correct workflow was rejected"; return 1
  fi

  echo "self-test: case 2 — a module go.mod is rejected (the real defect)"
  cat > "$tmp/wf/bad.yml" <<'EOF'
name: bad
on: [push]
jobs:
  build:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/setup-go@abcdef1234567890abcdef1234567890abcdef12 # v7
        with:
          go-version-file: backend/core/go.mod
EOF
  rc=0; report "$tmp/wf" "$tmp/actions" >/dev/null 2>&1 || rc=$?
  if [ "$rc" -eq 1 ]; then
    echo "  ok: go-version-file pointing at a module go.mod is rejected"
  else
    echo "  FAIL: the exact defect this guard was written for was accepted"; return 1
  fi
  rm "$tmp/wf/bad.yml"

  echo "self-test: case 3 — a go-version literal is rejected"
  cat > "$tmp/wf/lit.yml" <<'EOF'
name: lit
on: [push]
jobs:
  build:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/setup-go@abcdef1234567890abcdef1234567890abcdef12 # v7
        with:
          go-version: '1.26.0'
EOF
  rc=0; report "$tmp/wf" "$tmp/actions" >/dev/null 2>&1 || rc=$?
  if [ "$rc" -eq 1 ]; then
    echo "  ok: a pinned literal version is rejected"
  else
    echo "  FAIL: a literal go-version was accepted"; return 1
  fi
  rm "$tmp/wf/lit.yml"

  echo "self-test: case 4 — setup-go with no version input at all is rejected"
  cat > "$tmp/wf/none.yml" <<'EOF'
name: none
on: [push]
jobs:
  build:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/setup-go@abcdef1234567890abcdef1234567890abcdef12 # v7
EOF
  rc=0; report "$tmp/wf" "$tmp/actions" >/dev/null 2>&1 || rc=$?
  if [ "$rc" -eq 1 ]; then
    echo "  ok: setup-go with no version input is rejected"
  else
    echo "  FAIL: a setup-go with no version input was accepted"; return 1
  fi
  rm "$tmp/wf/none.yml"

  echo "self-test: case 5 — a directory with no setup-go step FAILS, rather than"
  echo "                   reporting the clean pass that matching nothing looks like"
  mkdir -p "$tmp/empty"
  cat > "$tmp/empty/nothing.yml" <<'EOF'
name: nothing
on: [push]
jobs:
  build:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v5
EOF
  rc=0; report "$tmp/empty" "$tmp/noactions" >/dev/null 2>&1 || rc=$?
  # Exactly 2, not merely non-zero: "matched nothing" is a REFUSAL to report,
  # a different outcome from "found a violation" (1). Asserting rc != 0 would
  # be satisfied by a guard that crashed on every input, which is the shape
  # these controls exist to rule out.
  if [ "$rc" -eq 2 ]; then
    echo "  ok: a scan that matches nothing is an error, not a pass"
  else
    echo "  FAIL: matching zero steps reported success"; return 1
  fi

  echo "self-test: case 6 — a step carrying BOTH inputs is rejected, because"
  echo "                   setup-go takes go-version and IGNORES go-version-file"
  cat > "$tmp/wf/both.yml" <<'EOF'
name: both
on: [push]
jobs:
  build:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/setup-go@abcdef1234567890abcdef1234567890abcdef12 # v7
        with:
          go-version: '1.26.0'
          go-version-file: go.work
EOF
  rc=0; report "$tmp/wf" "$tmp/actions" >/dev/null 2>&1 || rc=$?
  if [ "$rc" -eq 1 ]; then
    echo "  ok: a correct go-version-file does not excuse a literal beside it"
  else
    echo "  FAIL: a step whose literal WINS was reported as compliant"; return 1
  fi
  rm "$tmp/wf/both.yml"

  echo "self-test: case 7 — a COMPOSITE action's steps are scanned too"
  mkdir -p "$tmp/actions/setup"
  cat > "$tmp/actions/setup/action.yml" <<'EOF'
name: setup
description: wraps setup-go
runs:
  using: composite
  steps:
    - uses: actions/setup-go@abcdef1234567890abcdef1234567890abcdef12 # v7
      with:
        go-version-file: backend/core/go.mod
EOF
  rc=0; report "$tmp/wf" "$tmp/actions" >/dev/null 2>&1 || rc=$?
  if [ "$rc" -eq 1 ]; then
    echo "  ok: hiding setup-go inside a composite action does not evade the guard"
  else
    echo "  FAIL: a composite action's setup-go was never examined"; return 1
  fi
  rm -rf "$tmp/actions/setup"

  echo "self-test: case 8 — a differently-CASED uses: is still setup-go"
  cat > "$tmp/wf/cased.yml" <<'EOF'
name: cased
on: [push]
jobs:
  build:
    runs-on: ubuntu-latest
    steps:
      - uses: Actions/Setup-Go@abcdef1234567890abcdef1234567890abcdef12 # v7
        with:
          go-version-file: backend/core/go.mod
EOF
  rc=0; report "$tmp/wf" "$tmp/actions" >/dev/null 2>&1 || rc=$?
  if [ "$rc" -eq 1 ]; then
    echo "  ok: GitHub resolves uses: case-insensitively and so does this guard"
  else
    echo "  FAIL: Actions/Setup-Go slipped the name match"; return 1
  fi
  rm "$tmp/wf/cased.yml"

  echo "self-test: all cases behaved as expected"
}

main() {
  case "${1:-}" in
    --self-test) self_test ;;
    "")          echo "checking setup-go version sources under ${WORKFLOW_DIR} and ${ACTIONS_DIR}"
                 report "$WORKFLOW_DIR" "$ACTIONS_DIR" ;;
    *)           usage ;;
  esac
}

main "$@"
