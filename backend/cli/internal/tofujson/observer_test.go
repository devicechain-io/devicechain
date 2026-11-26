// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package tofujson

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The fixtures in testdata are real `tofu apply -json` streams, recorded with
// OpenTofu 1.10.6 against a configuration of built-in terraform_data resources (no
// provider to download), so every message shape is OpenTofu's own:
//
//	a  plain; b  input = "b-${var.gen}"; c  triggers_replace = [var.gen]
//	d  triggers_replace = [var.gen] with create_before_destroy
//	e  count = var.fail ? 1 : 0, with a local-exec provisioner that exits 1
//
//	create.jsonl          first apply: 4 to add
//	noop.jsonl            the same again: an empty plan
//	update-replace.jsonl  gen=2: b updated, c replaced destroy-first, d replaced
//	                      create-first (2 add, 1 change, 2 destroy)
//	delete.jsonl          every resource but a removed from the configuration
//	failed.jsonl          a fresh state with fail=true: 5 to add, 4 complete, e errors
//	retried.jsonl         the run after it with fail=false: the tainted e destroyed

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// observe feeds stream to a fresh observer in chunks of size n (0: whole), closes
// it, and returns every snapshot it reported plus what it wrote.
func observe(t *testing.T, stream []byte, n int) (snaps []Progress, o *Observer, human, diags string) {
	t.Helper()
	var h, d bytes.Buffer
	o = New(&h, &d, func(p Progress) { snaps = append(snaps, p) })
	if n <= 0 {
		n = len(stream) + 1
	}
	for len(stream) > 0 {
		k := min(n, len(stream))
		if w, err := o.Write(stream[:k]); w != k || err != nil {
			t.Fatalf("Write(%d bytes) = %d, %v: the command's stdout must always drain", k, w, err)
		}
		stream = stream[k:]
	}
	o.Close()
	return snaps, o, h.String(), d.String()
}

// checkMonotone is the in-attempt rule: the denominator, once known, never moves;
// the count never goes down and never passes the denominator; and a known total
// never goes back to unknown.
func checkMonotone(t *testing.T, snaps []Progress) {
	t.Helper()
	for i := 1; i < len(snaps); i++ {
		a, b := snaps[i-1], snaps[i]
		if b.Done < a.Done {
			t.Errorf("snapshot %d: done went back from %d to %d", i, a.Done, b.Done)
		}
		if a.Known && (!b.Known || b.Planned != a.Planned) {
			t.Errorf("snapshot %d: the denominator moved from %+v to %+v", i, a, b)
		}
	}
	for i, s := range snaps {
		if s.Known && s.Done > s.Planned {
			t.Errorf("snapshot %d: %d done of %d planned", i, s.Done, s.Planned)
		}
	}
}

func TestRecordedStreams(t *testing.T) {
	cases := []struct {
		file    string
		planned int64
		done    int64
	}{
		{"create.jsonl", 4, 4},
		{"noop.jsonl", 0, 0},
		{"update-replace.jsonl", 5, 5},
		{"delete.jsonl", 3, 3},
		// 🔴 A FAILED APPLY ENDS WHERE IT STOPPED. Four of five, not five of five.
		{"failed.jsonl", 5, 4},
		{"retried.jsonl", 1, 1},
	}
	for _, c := range cases {
		for _, chunk := range []int{0, 1, 7, 4096} {
			snaps, o, human, _ := observe(t, fixture(t, c.file), chunk)
			got := o.Progress()
			if !got.Known || got.Planned != c.planned || got.Done != c.done {
				t.Errorf("%s (chunks of %d): got %+v, want %d of %d", c.file, chunk, got, c.done, c.planned)
			}
			if len(snaps) == 0 || !snaps[0].Known || snaps[0].Done != 0 {
				t.Errorf("%s: the first snapshot is not the plan's 0 of %d: %+v", c.file, c.planned, snaps)
			}
			if int64(len(snaps)) != 1+c.done {
				t.Errorf("%s: %d snapshots, want one for the plan and one per completed action", c.file, len(snaps))
			}
			checkMonotone(t, snaps)
			if o.Malformed != 0 || o.Oversized != 0 {
				t.Errorf("%s: a recorded stream read as %d malformed, %d oversized", c.file, o.Malformed, o.Oversized)
			}
			if !strings.Contains(human, "Plan: ") {
				t.Errorf("%s: the plan's line did not reach the human output:\n%s", c.file, human)
			}
		}
	}
}

// TestTheApplysOwnSummaryIsNotProgress: a stream that announces a plan and then
// only the apply's closing summary (as a truncated or filtered stream would) has
// done nothing it reported. The closing summary says 4 added; the count stays 0.
func TestTheApplysOwnSummaryIsNotProgress(t *testing.T) {
	var keep []string
	for _, l := range strings.Split(string(fixture(t, "create.jsonl")), "\n") {
		if !strings.Contains(l, `"type":"apply_complete"`) {
			keep = append(keep, l)
		}
	}
	_, o, _, _ := observe(t, []byte(strings.Join(keep, "\n")), 0)
	if got := o.Progress(); got.Done != 0 || got.Planned != 4 {
		t.Fatalf("got %+v: the count moved on a summary rather than on completed actions", got)
	}
}

func TestAFailedApplyKeepsItsErrors(t *testing.T) {
	_, o, human, diags := observe(t, fixture(t, "failed.jsonl"), 0)
	if !strings.Contains(o.Errors(), "local-exec provisioner error") || !strings.Contains(o.Errors(), "exit status 1") {
		t.Errorf("the error diagnostic's summary and detail are not in Errors(): %q", o.Errors())
	}
	if !strings.Contains(diags, "Error: local-exec provisioner error") {
		t.Errorf("the diagnostic did not reach the diagnostics writer:\n%s", diags)
	}
	if strings.Contains(human, "provisioner error") {
		t.Errorf("a diagnostic went to the human output instead:\n%s", human)
	}
	if !strings.Contains(human, "terraform_data.e[0]: Creation errored after 0s") {
		t.Errorf("the errored action's line is missing:\n%s", human)
	}
}

// TestBrokenInput covers what a stream can be other than well formed: a line that
// is not JSON, JSON that is not a message, a line far over the cap, and a last line
// cut off mid-message. None of them may stop the lines after them being read, and
// none of them may move the count.
func TestBrokenInput(t *testing.T) {
	plan := `{"@message":"Plan: 2 to add, 0 to change, 0 to destroy.","type":"change_summary","changes":{"add":2,"change":0,"remove":0,"operation":"plan"}}`
	done := `{"@message":"x: Creation complete","type":"apply_complete","hook":{"action":"create"}}`
	huge := `{"@message":"` + strings.Repeat("z", MaxLine+10) + `","type":"apply_complete","hook":{"action":"create"}}`
	stream := strings.Join([]string{
		"not json at all",
		`{"no":"type"}`,
		plan,
		huge,
		done,
		`{"type":"apply_complete","hook":{"action":"read"}}`,
		`{"type":"apply_complete","hook":{"action":"noop"}}`,
		`{"@message":"y: Creation complete","type":"apply_complete","hook":{"act`, // truncated, no newline
	}, "\n")
	for _, chunk := range []int{0, 1, 3, 1000} {
		snaps, o, human, _ := observe(t, []byte(stream), chunk)
		if got := o.Progress(); got != (Progress{Done: 1, Planned: 2, Known: true}) {
			t.Errorf("chunks of %d: got %+v, want 1 of 2", chunk, got)
		}
		checkMonotone(t, snaps)
		if o.Malformed != 3 || o.Oversized != 1 {
			t.Errorf("chunks of %d: %d malformed, %d oversized; want 3 and 1", chunk, o.Malformed, o.Oversized)
		}
		if strings.Contains(human, "zzzz") {
			t.Errorf("chunks of %d: the oversized line was written out", chunk)
		}
		if !strings.Contains(human, "not json at all") {
			t.Errorf("chunks of %d: a non-message line from OpenTofu was hidden", chunk)
		}
	}
}

// TestTheDenominatorIsUnknownUntilThePlan: completions read before any plan
// summary count, but with no total; they never invent one.
func TestTheDenominatorIsUnknownUntilThePlan(t *testing.T) {
	snaps, o, _, _ := observe(t, []byte(`{"type":"apply_complete","hook":{"action":"delete"}}`+"\n"), 0)
	if len(snaps) != 1 || snaps[0].Known || snaps[0].Done != 1 {
		t.Fatalf("got %+v", snaps)
	}
	if o.Progress().Known {
		t.Fatal("a total was invented")
	}
}

// TestASecondPlanSummaryDoesNotMoveTheDenominator: one observer is one apply, and
// its denominator is set once.
func TestASecondPlanSummaryDoesNotMoveTheDenominator(t *testing.T) {
	s := fixture(t, "failed.jsonl")
	s = append(s, fixture(t, "retried.jsonl")...)
	snaps, o, _, _ := observe(t, s, 0)
	checkMonotone(t, snaps)
	if got := o.Progress(); got.Planned != 5 {
		t.Fatalf("got %+v", got)
	}
}
