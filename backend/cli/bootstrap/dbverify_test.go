// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"slices"
	"strings"
	"testing"
)

// TestClassifyJob pins the verdict for every background-job state, including the
// precedence between them.
//
// This exists because the switch it covers is the entire judgement of the
// background-job axis, and until it was extracted the only way to reach it was to
// stand up a PostgreSQL cluster, install TimescaleDB, create a continuous
// aggregate and then corrupt its scheduler state by hand. Each of those mutations
// was run once against a real cluster, which proves the checks fire; none of them
// is repeatable in CI, which is what this is for.
func TestClassifyJob(t *testing.T) {
	cases := []struct {
		name    string
		state   jobState
		settled bool
		want    string // expected check id, "" for healthy
		// contains is a phrase the reason MUST carry. Asserted because two of
		// these states share a check id, so the id alone cannot tell them apart —
		// and a message that names the wrong cause sends an operator to the wrong
		// place at the worst time.
		contains string
	}{
		{
			name:  "healthy scheduled job",
			state: jobState{Scheduled: true, LastRunStatus: "Success"},
			want:  "",
		},
		{
			name:  "healthy job that has never run carries no status",
			state: jobState{Scheduled: true, LastRunStatus: ""},
			want:  "",
		},
		{
			// The upstream timescale#9360 shape.
			name:     "next_start -infinity is a stuck job",
			state:    jobState{Scheduled: true, NextStartPast: true},
			want:     "B10",
			contains: "-infinity",
		},
		{
			// alter_job(next_start => 'infinity'). A different value, the same
			// consequence, and invisible to a check written only for -infinity.
			name:     "next_start +infinity is a parked job",
			state:    jobState{Scheduled: true, NextStartNever: true},
			want:     "B10",
			contains: "+infinity",
		},
		{
			// alter_job(scheduled => false). Reports no error, runs nothing.
			name:     "an unscheduled job is not running regardless of anything else",
			state:    jobState{Scheduled: false, LastRunStatus: "Success"},
			want:     "B11",
			contains: "NOT scheduled",
		},
		{
			name:     "scheduled with no next_start, settle window elapsed",
			state:    jobState{Scheduled: true, NextStartUnset: true},
			settled:  true,
			want:     "B11",
			contains: "after the settle window",
		},
		{
			// 🔴 The same state must NOT claim a settle window elapsed when none
			// did. This is the message an operator reads at --settle 0, seconds
			// after a promotion, when the value clears on its own.
			name:     "scheduled with no next_start and NO settle window says so",
			state:    jobState{Scheduled: true, NextStartUnset: true},
			settled:  false,
			want:     "B11",
			contains: "--settle 0",
		},
		{
			name:     "a failed last run",
			state:    jobState{Scheduled: true, LastRunStatus: "Failed"},
			want:     "B12",
			contains: "last run FAILED",
		},
		{
			// Precedence. A stuck job whose last run also failed is stuck first:
			// reporting only the failure would send someone to look at the job's
			// SQL rather than at its scheduling.
			name:     "-infinity outranks a failed last run",
			state:    jobState{Scheduled: true, NextStartPast: true, LastRunStatus: "Failed"},
			want:     "B10",
			contains: "-infinity",
		},
		{
			// A paused job has no next_start either. It must report as paused,
			// which is actionable, rather than as an unassigned next run, which
			// would send an operator to wait out a settle window that will never
			// change anything.
			name:     "unscheduled outranks a missing next_start",
			state:    jobState{Scheduled: false, NextStartUnset: true},
			want:     "B11",
			contains: "NOT scheduled",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			check, reason := classifyJob(tc.state, tc.settled)
			if check != tc.want {
				t.Fatalf("check = %q, want %q (reason: %s)", check, tc.want, reason)
			}
			if tc.want == "" {
				if reason != "" {
					t.Fatalf("a healthy job produced a reason: %q", reason)
				}
				return
			}
			if reason == "" {
				t.Fatalf("check %s produced no reason; a finding with no explanation is "+
					"not actionable", check)
			}
			if tc.contains != "" && !strings.Contains(reason, tc.contains) {
				t.Fatalf("reason does not name the cause.\n  want it to contain: %q\n  got: %s",
					tc.contains, reason)
			}
		})
	}
}

// TestClassifyJobHealthyStateIsReachable is the counterweight.
//
// Every case above asserts that some state is a finding. Without this, a
// classifier that returned a finding for EVERYTHING would pass all of them that
// matter and make the whole axis permanently red — which, per this workstream's
// own recurring lesson, is the state in which a check gets switched off rather
// than fixed.
func TestClassifyJobHealthyStateIsReachable(t *testing.T) {
	healthy := jobState{Scheduled: true, LastRunStatus: "Success"}
	if check, reason := classifyJob(healthy, true); check != "" {
		t.Fatalf("the ordinary healthy job was reported as %s: %s", check, reason)
	}
	if check, reason := classifyJob(healthy, false); check != "" {
		t.Fatalf("the ordinary healthy job was reported as %s without a settle window: %s",
			check, reason)
	}
}

// TestLegacyDbRemovalHatchReachesOpenTofu pins the escape hatch the cutover
// guards name in their own error messages.
//
// Without this the hatch was UNREACHABLE through the supported path, which an
// adversarial review found: dcctl re-extracts the OpenTofu root into the
// instance's working directory on every run and passes a fixed set of -var
// flags, so an operator told to "set allow_legacy_tsdb_removal = true" had
// nowhere to set it. On a local cluster the other branch works — destroy and
// rebuild — but on a real one there was no route past the guard at all.
//
// Asserted on the vars the apply ACTUALLY passes (infraVars, the production
// renderer) rather than on the flag, because the flag being wired to a struct
// field is not the property that matters.
func TestLegacyDbRemovalHatchReachesOpenTofu(t *testing.T) {
	both := []string{"allow_legacy_rdb_removal=true", "allow_legacy_tsdb_removal=true"}

	t.Run("off by default", func(t *testing.T) {
		vars := infraVars(&State{KubeContext: "kind-test"})
		for _, want := range both {
			if slices.Contains(vars, want) {
				t.Errorf("%q is passed by DEFAULT. This variable authorises destroying a "+
					"database and replacing it with an empty one; it must never be on "+
					"unless asked for", want)
			}
		}
	})

	t.Run("set emits BOTH stores", func(t *testing.T) {
		vars := infraVars(&State{KubeContext: "kind-test", AllowLegacyDbRemoval: true})
		for _, want := range both {
			if !slices.Contains(vars, want) {
				t.Errorf("infraVars did not emit %q, so the guard for that store cannot be "+
					"passed through dcctl and its error message names a variable the "+
					"operator has no way to set.\ngot: %v", want, vars)
			}
		}
	})
}
