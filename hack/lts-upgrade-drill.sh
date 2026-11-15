#!/usr/bin/env bash
# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
#
# THE LTS-TO-LTS UPGRADE DRILL -- does an instance on the long-term-support line keep its data when
# its operator skips the intermediate minors?
#
#   hack/lts-upgrade-drill.sh plan FROM TO       check the hop is a drillable LTS hop; needs no cluster
#   hack/lts-upgrade-drill.sh run FROM TO        the drill: seed on FROM, upgrade to this checkout, read back
#   hack/lts-upgrade-drill.sh control FROM TO    the same drill on a tree with a PLANTED defect; it must FAIL
#   hack/lts-upgrade-drill.sh both FROM TO       run, then control -- the only form worth quoting
#   hack/lts-upgrade-drill.sh --self-test
#
#   FROM  a stable release tag ON the designated LTS line (hack/lts-policy), e.g. v1.2.4
#   TO    the release this checkout is the candidate for, e.g. v1.5.0-rc.1. It names the hop; the images
#         under test are this checkout's (or, with --pull, the published TO tag).
#
# WHY A SEPARATE DRILL. hack/upgrade-rig.sh derives its baseline as the highest stable tag below the target,
# so the gate every release runs only ever drills ADJACENT releases. An operator on the LTS line does not
# upgrade one minor at a time -- that is the point of a long-term line -- and the claim an LTS makes is that
# the jump works. Every migration of every skipped minor runs in one go, in order, on a database that has
# never seen the code that wrote its neighbours. This drill is the rig, pointed at that hop: it adds no
# new way of seeding or reading back, so it cannot disagree with the gate about what survival means.
#
# 🔴 A PASS IS WORTH NOTHING UNTIL THE DRILL HAS FAILED ON PURPOSE. `control` copies this tree, plants one
# migration that quietly rewrites every device's name (hack/lts-drill/planted_defect_migration.go.txt), and runs
# the same drill. It must fail, and it must fail for the RIGHT reason: the rig's own read-back reporting a field
# that CHANGED. A rig that dies for any other reason (a cluster that would not start, a setup error) proves
# nothing about the defect and is reported as INCONCLUSIVE, not as the control holding.
#
# Exit codes: 0 ok; 1 the drill (or plan) failed; 3 the control FAILED to fail (the drill is blind to the
# planted defect); 4 the control was INCONCLUSIVE.

set -euo pipefail

repo_root="${DC_LTS_REPO:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
this_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=lts.sh
source "$this_dir/lts.sh"
# lts.sh derives its own repo_root; the drill's may be redirected (self-test), so pin ours again.
repo_root="${DC_LTS_REPO:-$(cd "$this_dir/.." && pwd)}"

rig_rel="hack/upgrade-rig.sh"
planted_src="$this_dir/lts-drill/planted_defect_migration.go.txt"
planted_dest_rel="backend/services/device-management/schema/zz_planted_lts_drill.go"

exit_control_blind=3
exit_control_inconclusive=4

say() { printf '\n\033[1;36m==> %s\033[0m\n' "$*"; }
fail() { printf '\n\033[1;31mFAIL: %s\033[0m\n' "$*" >&2; exit 1; }

# plan FROM TO: refuse a hop this drill is not for.
cmd_plan() {
  local from="${1:-}" to="${2:-}" lts
  [[ -n "$from" && -n "$to" ]] || fail "usage: lts-upgrade-drill.sh plan FROM TO"
  lts="$(cmd_policy)" || fail "the LTS policy could not be read"
  [[ "$lts" != none ]] || fail "no LTS line is designated (hack/lts-policy says current-lts: none), so there is no LTS hop to drill. Designate a line first."
  local from_line
  from_line="$(line_of "$from")" || fail "FROM is not a release tag"
  [[ "$from_line" == "$lts" ]] ||
    fail "FROM $from is on line $from_line, but the designated LTS line is $lts. This drill is for the hop that LEAVES the LTS line."
  cmd_skip_minor "$from" "$to" || fail "$from -> $to is not a skip-minor hop"
  git -C "$repo_root" rev-parse --verify -q "refs/tags/$from^{commit}" >/dev/null ||
    fail "tag $from is not in this checkout (the rig builds the baseline's own dcctl from it); fetch tags with full history"
  say "plan: $from -> $to leaves LTS line $lts and skips $(( $(minor_of "$to") - $(minor_of "$from") - 1 )) whole minor(s)"
}

minor_of() { [[ "$1" =~ $any_re ]] && printf '%s\n' "${BASH_REMATCH[2]}"; }

# rig_env_for [--pull TO]: the environment the rig reads, as KEY=VALUE words.
pull_flag=0
rig_env() {
  local from="$1" to="$2"
  printf 'DC_BASELINE_TAG=%s\n' "$from"
  if ((pull_flag)); then
    printf 'DC_UPGRADE_IMAGES=pull\nDC_UPGRADE_TAG=%s\n' "$to"
  fi
}

# run_rig TREE FROM TO LOGFILE: run `rig all` in TREE with the hop's environment; return the rig's status.
run_rig() {
  local tree="$1" from="$2" to="$3" log="$4" rc=0
  local -a envs
  mapfile -t envs < <(rig_env "$from" "$to")
  # tee, with the rig's status taken from PIPESTATUS so the log never decides the verdict.
  env "${envs[@]}" "$tree/$rig_rel" all 2>&1 | tee "$log" || rc="${PIPESTATUS[0]}"
  return "$rc"
}

rig_down() {
  local tree="$1" from="$2" to="$3"
  local -a envs
  mapfile -t envs < <(rig_env "$from" "$to")
  env "${envs[@]}" "$tree/$rig_rel" down >/dev/null 2>&1 || true
}

cmd_run() {
  cmd_plan "$@"
  local from="$1" to="$2" log rc=0
  log="$(mktemp)"
  say "THE DRILL: seed on $from, upgrade straight to $to, read every row back"
  run_rig "$repo_root" "$from" "$to" "$log" || rc=$?
  rm -f "$log"
  rig_down "$repo_root" "$from" "$to"
  ((rc == 0)) || fail "the LTS upgrade drill FAILED ($from -> $to, rig exit $rc); read the rig output above"
  say "LTS DRILL PASSED: rows written on $from read back unchanged after a direct upgrade to $to"
}

# judge_planted RC LOGFILE: classify the planted run. Prints a verdict line; returns
#   0 the control HELD     (the rig failed, because a field CHANGED)
#   1 the control FAILED   (the rig passed over a planted defect: the drill is blind)
#   2 INCONCLUSIVE         (the rig failed for some other reason, or said something unrecognised)
judge_planted() {
  local rc="$1" log="$2" text got legend
  if ((rc == 0)); then
    echo "CONTROL FAILED: the drill PASSED on a tree that rewrites every device name. It cannot see this class of defect."
    return 1
  fi
  # Strip terminal colour so the matches below see the words.
  text="$(sed 's/\x1b\[[0-9;]*m//g' "$log")"
  # The rig prints "(apiprobe exit N)" in its did-not-survive headline and, under it, a legend that names
  # which code means which. The headline's N must be the code the legend calls "a field CHANGED". Reading
  # the number from the rig's own legend (rather than hard-coding 3) is what stops a renumbering from
  # turning this into a check of the wrong thing.
  if grep -q 'THE DRILL COULD NOT RUN' <<<"$text"; then
    echo "CONTROL INCONCLUSIVE: the drill could not run (setup failure). That says nothing about the planted defect."
    return 2
  fi
  got="$(sed -n 's/.*did NOT survive the upgrade (apiprobe exit \([0-9][0-9]*\)).*/\1/p' <<<"$text" | head -1)"
  legend="$(sed -n 's/^[[:space:]]*\([0-9][0-9]*\)[[:space:]]*a field CHANGED.*/\1/p' <<<"$text" | head -1)"
  if [[ -z "$got" || -z "$legend" ]]; then
    echo "CONTROL INCONCLUSIVE: the rig failed (exit $rc) without the read-back's did-not-survive headline, so it did not fail on the data."
    return 2
  fi
  if [[ "$got" != "$legend" ]]; then
    echo "CONTROL INCONCLUSIVE: the read-back failed with code $got, not the 'a field CHANGED' code $legend the planted defect should produce."
    return 2
  fi
  # The failure must be the PLANTED rewrite, not any corruption: apiprobe prints the row it read back, and the
  # planted migration is the only thing that appends this suffix to a device name.
  if ! grep -Eq '^[[:space:]]*read:.*[(]planted[)]' <<<"$text"; then
    echo "CONTROL INCONCLUSIVE: a field CHANGED, but the read-back does not show the planted rewrite, so it cannot be credited to the planted migration."
    return 2
  fi
  echo "CONTROL HELD: the read-back failed with code $got -- a field CHANGED, and it is the planted rewrite -- on the tree with the planted migration."
  return 0
}

cmd_control() {
  cmd_plan "$@"
  local from="$1" to="$2" scratch log rc=0 verdict jr=0
  # The planted defect lives in this checkout's SOURCE, so the control always builds it, whatever the run did.
  pull_flag=0
  [[ -f "$planted_src" ]] || fail "the planted-defect source $planted_src is missing"
  scratch="$(mktemp -d)"
  log="$(mktemp)"
  # A throwaway checkout of HEAD: the planted file must never land in the real tree.
  git -C "$repo_root" worktree add -q --detach "$scratch/tree" HEAD
  cleanup_control() {
    rig_down "$scratch/tree" "$from" "$to"
    git -C "$repo_root" worktree remove --force "$scratch/tree" >/dev/null 2>&1 || true
    rm -rf "$scratch" "$log"
  }
  trap cleanup_control EXIT
  [[ -d "$scratch/tree/$(dirname "$planted_dest_rel")" ]] ||
    fail "the schema directory the planted migration belongs in is gone from this tree; update planted_dest_rel"
  cp "$planted_src" "$scratch/tree/$planted_dest_rel"
  say "THE CONTROL: the same drill on a tree with a migration that rewrites every device name"
  run_rig "$scratch/tree" "$from" "$to" "$log" || rc=$?
  verdict="$(judge_planted "$rc" "$log")" || jr=$?
  printf '\n%s\n' "$verdict"
  trap - EXIT
  cleanup_control
  case "$jr" in
  0) ;;
  1) exit "$exit_control_blind" ;;
  *) exit "$exit_control_inconclusive" ;;
  esac
}

cmd_both() {
  cmd_run "$@"
  cmd_control "$@"
  say "BOTH HALVES HOLD: the drill passed on the candidate and failed on the planted defect."
}

self_test() {
  local fails=0 tmp
  tmp="$(mktemp -d)"
  expect() {
    local name="$1" want="$2" rc=0
    shift 2
    ("$@") >/dev/null 2>&1 || rc=$?
    if [[ "$rc" == "$want" ]]; then printf 'ok   %s\n' "$name"; else
      printf 'FAIL %s (exit %s, wanted %s)\n' "$name" "$rc" "$want" >&2
      fails=$((fails + 1))
    fi
  }

  # --- judge_planted: the verdict is read from the rig's words, not from "it failed"
  local held="$tmp/held" setup="$tmp/setup" other="$tmp/other" wrongcode="$tmp/wrong" nolegend="$tmp/nolegend" noplant="$tmp/noplant"
  cat >"$held" <<'EOF'
device "apiprobe-token" CHANGED across the upgrade:
   written: {"name":"apiprobe device"}
   read:    {"name":"apiprobe device (planted)"}
FAIL: rows written on v1.2.4 did NOT survive the upgrade (apiprobe exit 3).
Read apiprobe's output above, and note that the code says which KIND of defect:
  2  a row is GONE — a migration dropped data, and a schema-only diff cannot see it
  3 a field CHANGED — a migration rewrote data, which is the failure this drill exists for
  4  the QUERY was rejected — the schema moved; the row may be perfectly intact
EOF
  sed 's/exit 3/exit 2/' "$held" >"$wrongcode"
  grep -v '(planted)' "$held" >"$noplant"
  grep '^FAIL' "$held" >"$nolegend"
  printf 'FAIL: THE DRILL COULD NOT RUN, and this says NOTHING about the upgrade either way\n' >"$setup"
  printf 'kind: could not create cluster\n' >"$other"
  expect "judge: failed on a changed field -> held" 0 judge_planted 1 "$held"
  expect "judge: colour codes are stripped" 0 judge_planted 1 <(sed 's/^FAIL/\x1b[1;31mFAIL/' "$held")
  expect "judge: a PASS over the defect -> control failed" 1 judge_planted 0 "$held"
  expect "judge: setup failure -> inconclusive" 2 judge_planted 1 "$setup"
  expect "judge: unrelated failure -> inconclusive" 2 judge_planted 1 "$other"
  expect "judge: a row GONE is not the planted defect -> inconclusive" 2 judge_planted 1 "$wrongcode"
  expect "judge: headline without the legend -> inconclusive" 2 judge_planted 1 "$nolegend"
  expect "judge: a changed field that is not the planted rewrite -> inconclusive" 2 judge_planted 1 "$noplant"

  # --- the planted file still fits the tree it will be copied into
  expect "planted source is present" 0 test -f "$planted_src"
  expect "planted source declares package schema" 0 grep -q '^package schema$' "$planted_src"
  expect "planted source appends to Migrations" 0 grep -q 'Migrations = append(Migrations' "$planted_src"
  expect "planted source rewrites devices" 0 grep -q '"device-management"."devices"' "$planted_src"
  expect "the schema dir it lands in exists" 0 test -d "$repo_root/$(dirname "$planted_dest_rel")"
  expect "the real rig prints the headline the judge reads" 0 grep -q 'did NOT survive the upgrade (apiprobe exit' "$repo_root/$rig_rel"
  expect "the real rig prints the legend the judge reads" 0 grep -q 'a field CHANGED' "$repo_root/$rig_rel"
  expect "the real rig prints the setup headline the judge reads" 0 grep -q 'THE DRILL COULD NOT RUN' "$repo_root/$rig_rel"

  # --- plan, against a throwaway repository and policy
  local r="$tmp/repo" pol="$tmp/pol" polnone="$tmp/polnone"
  git init -q "$r"
  git -C "$r" -c user.name=t -c user.email=t@t -c commit.gpgsign=false commit -q --allow-empty -m base
  git -C "$r" -c user.name=t -c user.email=t@t -c tag.gpgsign=false tag v1.2.4
  # a real tag on the WRONG line, so "FROM not on the LTS line" is refused for that reason and not for a missing tag
  git -C "$r" -c user.name=t -c user.email=t@t -c tag.gpgsign=false tag v1.3.0
  printf 'current-lts: 1.2\n' >"$pol"
  printf 'current-lts: none\n' >"$polnone"
  plan_in() { (repo_root="$r"; policy_file="$1"; shift; cmd_plan "$@"); }
  expect "plan: an LTS hop that skips a minor" 0 plan_in "$pol" v1.2.4 v1.4.0
  expect "plan: a candidate target" 0 plan_in "$pol" v1.2.4 v1.5.0-rc.1
  expect "plan: no LTS designated" 1 plan_in "$polnone" v1.2.4 v1.4.0
  expect "plan: FROM not on the LTS line" 1 plan_in "$pol" v1.3.0 v1.5.0
  expect "plan: adjacent minor skips nothing" 1 plan_in "$pol" v1.2.4 v1.3.0
  expect "plan: FROM tag missing from the checkout" 1 plan_in "$pol" v1.2.9 v1.5.0

  # --- the whole control, end to end, against a stub rig in a throwaway repository
  # The stub PASSES unless the planted migration is present in its tree, and fails like the real rig's
  # read-back when it is. So the control holds only if the plant really reaches the tree the rig runs in
  # -- and a control that forgot to plant (mutant) sees the stub pass and must exit 3.
  local c="$tmp/ctl"
  git init -q "$c"
  mkdir -p "$c/hack/lts-drill" "$c/backend/services/device-management/schema"
  cp "$planted_src" "$c/hack/lts-drill/planted_defect_migration.go.txt"
  cp "$held" "$c/held.txt"
  cat >"$c/hack/upgrade-rig.sh" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
here="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
[[ "${1:-}" == down ]] && exit 0
if [[ -f "$here/backend/services/device-management/schema/zz_planted_lts_drill.go" ]]; then
  cat "$here/held.txt"
  exit 1
fi
echo "DRILL PASSED"
EOF
  chmod +x "$c/hack/upgrade-rig.sh"
  touch "$c/backend/services/device-management/schema/doc.go"
  git -C "$c" add -A
  git -C "$c" -c user.name=t -c user.email=t@t -c commit.gpgsign=false commit -q -m tree
  git -C "$c" -c user.name=t -c user.email=t@t -c tag.gpgsign=false tag v1.2.4
  ctl_in() { (repo_root="$c"; policy_file="$pol"; planted_src="$c/hack/lts-drill/planted_defect_migration.go.txt"; cmd_control "$@"); }
  expect "control: holds when the plant reaches the rig's tree" 0 ctl_in v1.2.4 v1.4.0
  expect "control: leaves no worktree behind" 0 test "$(git -C "$c" worktree list | wc -l)" -eq 1
  expect "control: the real tree is not planted" 1 test -e "$c/backend/services/device-management/schema/zz_planted_lts_drill.go"
  # the blind-drill case: a rig that passes regardless of the plant
  printf '#!/usr/bin/env bash\n[[ "${1:-}" == down ]] && exit 0\necho "DRILL PASSED"\n' >"$c/hack/upgrade-rig.sh"
  git -C "$c" add -A
  git -C "$c" -c user.name=t -c user.email=t@t -c commit.gpgsign=false commit -q -m blind
  expect "control: a blind drill exits 3" "$exit_control_blind" ctl_in v1.2.4 v1.4.0
  printf '#!/usr/bin/env bash\n[[ "${1:-}" == down ]] && exit 0\necho "cluster would not start"\nexit 1\n' >"$c/hack/upgrade-rig.sh"
  git -C "$c" add -A
  git -C "$c" -c user.name=t -c user.email=t@t -c commit.gpgsign=false commit -q -m broken
  expect "control: a rig that dies elsewhere exits 4" "$exit_control_inconclusive" ctl_in v1.2.4 v1.4.0

  rm -rf "$tmp"
  if ((fails > 0)); then
    printf 'lts-upgrade-drill self-test: %d failed\n' "$fails" >&2
    return 1
  fi
  echo "lts-upgrade-drill self-test: all passed"
}

main() {
  if [[ "${1:-}" == --pull ]]; then
    pull_flag=1
    shift
  fi
  case "${1:-}" in
  plan) shift; cmd_plan "$@" ;;
  run) shift; cmd_run "$@" ;;
  control) shift; cmd_control "$@" ;;
  both) shift; cmd_both "$@" ;;
  --self-test) self_test ;;
  *) fail "usage: lts-upgrade-drill.sh [--pull] plan|run|control|both FROM TO | --self-test" ;;
  esac
}

main "$@"
