// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package ota

import (
	"math/rand"
	"reflect"
	"testing"
	"time"
)

var (
	digestA = "sha256:" + repeat('a', 64)
	digestB = "sha256:" + repeat('b', 64)
	t0      = time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)

	testPolicy = Policy{
		Acknowledge: time.Minute, Download: 10 * time.Minute, Verify: 2 * time.Minute,
		Install: 5 * time.Minute, Confirm: 3 * time.Minute,
	}
)

func repeat(c byte, n int) string {
	b := make([]byte, n)
	for i := range b {
		b[i] = c
	}
	return string(b)
}

// attemptIn builds an attempt directly in a state (struct literal, not NewAttempt, so a test's
// setup cannot fail for a reason unrelated to what the test is about). Reboot-requiring, boot "boot-a".
func attemptIn(s State, seq uint64) Attempt {
	a := Attempt{
		AttemptID: "att-1", AssignmentID: "asg-1", Component: "firmware",
		Target: Target{Version: "2.0.0", Digest: digestA, RequiresReboot: true},
		State:  s, LastSeq: seq, LastReportAt: t0,
	}
	if s != StateQueued {
		a.BootIDAtStart = "boot-a"
	}
	return a
}

func progressReport(stage ReportStage, seq uint64) Report {
	return Report{
		V: 1, Kind: KindProgress, AttemptID: "att-1", AssignmentID: "asg-1", Component: "firmware",
		ArtifactDigest: digestA, Seq: seq, Stage: stage, BootID: "boot-a",
	}
}

func runningReport(seq uint64, bootID string) Report {
	r := progressReport(StageRunning, seq)
	r.BootID = bootID
	r.Running = &Running{Version: "2.0.0", Digest: digestA}
	return r
}

func failedReport(seq uint64) Report {
	r := progressReport(StageFailed, seq)
	r.Result = &Result{Code: "FLASH_ERROR", Detail: "bad block"}
	return r
}

func inventoryReport(version, bootID string) Report {
	return Report{V: 1, Kind: KindInventory, Component: "firmware", BootID: bootID, Running: &Running{Version: version}}
}

func newRand(seed int64) *rand.Rand { return rand.New(rand.NewSource(seed)) }

// cloneForTest deep-copies a report's pointers so purity can be checked against a snapshot.
func (r Report) cloneForTest() Report {
	if r.Progress != nil {
		p := *r.Progress
		r.Progress = &p
	}
	if r.Result != nil {
		x := *r.Result
		r.Result = &x
	}
	if r.Running != nil {
		x := *r.Running
		r.Running = &x
	}
	return r
}

// mustApply applies and asserts the verdict; rejections must leave the attempt deep-equal.
func mustApply(t *testing.T, a Attempt, r Report, want Verdict) Attempt {
	t.Helper()
	before := a.clone()
	out, v := Apply(a, r, t0.Add(time.Second))
	if v != want {
		t.Fatalf("Apply(%s, %s seq %d) verdict = %q, want %q", a.State, r.Stage, r.Seq, v, want)
	}
	if want != VerdictApplied && !reflect.DeepEqual(out, a) {
		t.Fatalf("rejected report (%s) changed the attempt: %+v -> %+v", want, a, out)
	}
	if !reflect.DeepEqual(a, before) {
		t.Fatalf("Apply mutated its input")
	}
	return out
}
