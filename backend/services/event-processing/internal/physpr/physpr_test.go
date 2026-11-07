// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package physpr

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/devicechain-io/dc-event-processing/internal/preview"
)

var t0 = time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)

func fire(series string, raise bool, at time.Duration) preview.Firing {
	return preview.Firing{OccurredAt: t0.Add(at), Series: series, Raise: raise}
}

func res(fs ...preview.Firing) preview.Result {
	return preview.Result{Firings: fs, Stats: preview.Stats{EventsScanned: 100, FiringCount: len(fs)}}
}

// The negative control: the same replay on both sides is an empty diff, whatever the shift window.
func TestIdenticalReplaysDiffEmpty(t *testing.T) {
	r := res(fire("p1", true, 0), fire("p1", false, time.Minute), fire("p2", true, time.Hour))
	for _, w := range []time.Duration{0, time.Second, 24 * time.Hour} {
		d := DiffFirings(r, r, w)
		if !d.Empty() || d.Raises.Unchanged != 2 || d.Resolves.Unchanged != 1 || len(d.Changes) != 0 || len(d.Series) != 0 {
			t.Fatalf("window %v: want an empty diff with 2/1 unchanged, got %+v", w, d)
		}
	}
	if out := RenderDiff(DiffFirings(r, r, 0), Ref{Label: "a"}, Ref{Label: "b"}, "w"); !strings.Contains(out, "No behavioural change") {
		t.Fatalf("an empty diff must say so:\n%s", out)
	}
}

func TestDiffClassifiesAddedRemovedShifted(t *testing.T) {
	base := res(
		fire("pump-17", true, 10*time.Minute), // will fire 41 minutes earlier
		fire("pump-3", true, 5*time.Minute),   // suppressed by the head
		fire("pump-9", true, 20*time.Minute),  // unchanged
		fire("pump-9", false, 30*time.Minute), // unchanged resolve
	)
	head := res(
		fire("pump-17", true, 10*time.Minute-41*time.Minute),
		fire("pump-9", true, 20*time.Minute),
		fire("pump-9", false, 30*time.Minute),
		fire("pump-4", true, 2*time.Hour), // new
		fire("pump-4", true, 5*time.Hour), // new
	)
	d := DiffFirings(base, head, 0)
	if d.Empty() {
		t.Fatal("diff must not be empty")
	}
	if got := d.Raises; got != (Counts{Unchanged: 1, Added: 2, Removed: 1, Shifted: 1}) {
		t.Fatalf("raise counts = %+v", got)
	}
	if d.Resolves != (Counts{Unchanged: 1}) {
		t.Fatalf("resolve counts = %+v", d.Resolves)
	}
	var shifted *Change
	for i := range d.Changes {
		if d.Changes[i].Kind == Shifted {
			shifted = &d.Changes[i]
		}
	}
	if shifted == nil || shifted.Series != "pump-17" || shifted.Shift != -41*time.Minute {
		t.Fatalf("want pump-17 shifted by -41m, got %+v", shifted)
	}
	if len(d.Series) != 3 || d.Series[0].Series != "pump-4" {
		t.Fatalf("series must be ordered most-affected first, got %+v", d.Series)
	}
	out := RenderDiff(d, Ref{Label: "v3"}, Ref{Label: "v4", Author: "agent:ops-bot"}, "w")
	for _, want := range []string{"+2 raised", "-1 suppressed", "1 moved", "pump-17", "41 minutes earlier", "v4 (agent:ops-bot)"} {
		if !strings.Contains(out, want) {
			t.Fatalf("report lacks %q:\n%s", want, out)
		}
	}
}

// A shift larger than the window is a removal plus an addition, not a move: the window is the
// reviewer's definition of "the same alarm".
func TestShiftWindowSeparatesMovedFromReplaced(t *testing.T) {
	base, head := res(fire("a", true, 0)), res(fire("a", true, 3*time.Hour))
	if d := DiffFirings(base, head, time.Hour); d.Raises != (Counts{Added: 1, Removed: 1}) {
		t.Fatalf("outside the window want added+removed, got %+v", d.Raises)
	}
	if d := DiffFirings(base, head, 4*time.Hour); d.Raises != (Counts{Shifted: 1}) {
		t.Fatalf("inside the window want one shift, got %+v", d.Raises)
	}
}

func TestDiffIsDeterministic(t *testing.T) {
	var bs, hs []preview.Firing
	for i := 0; i < 50; i++ {
		s := fmt.Sprintf("d%02d", i%7)
		bs = append(bs, fire(s, true, time.Duration(i)*time.Minute))
		hs = append(hs, fire(s, true, time.Duration(i)*time.Minute+time.Duration(i%3)*time.Minute))
	}
	first := fmt.Sprintf("%+v", DiffFirings(res(bs...), res(hs...), 0))
	for i := 0; i < 20; i++ {
		if got := fmt.Sprintf("%+v", DiffFirings(res(bs...), res(hs...), 0)); got != first {
			t.Fatal("the diff of the same two replays must be identical every time (map order must not leak)")
		}
	}
}

func TestDiffSurfacesEvaluationHealthAndPartialReplays(t *testing.T) {
	base, head := res(fire("a", true, 0)), res(fire("a", true, 0))
	head.Stats.CalculatedSkips = 2
	head.Degraded = "the window spans more history than the preview read limit"
	out := RenderDiff(DiffFirings(base, head, 0), Ref{Label: "b"}, Ref{Label: "h"}, "w")
	for _, want := range []string{"calculated-metric skips: 0 -> 2", "head replay is partial"} {
		if !strings.Contains(out, want) {
			t.Fatalf("report lacks %q:\n%s", want, out)
		}
	}
}

// Every position of the planted flip in every history length is found, in at most
// ceil(log2 n)+2 probes, and the version before it still behaves like the oldest.
func TestBisectFindsEveryPlantedFlip(t *testing.T) {
	for n := 2; n <= MaxVersions; n++ {
		for k := 1; k < n; k++ {
			probes := 0
			r, err := Bisect(context.Background(), n, func(_ context.Context, i int) (bool, error) {
				probes++
				return i >= k, nil
			})
			if err != nil {
				t.Fatalf("n=%d k=%d: %v", n, k, err)
			}
			if r.First != k || r.OldestHolds {
				t.Fatalf("n=%d k=%d: got %+v", n, k, r)
			}
			limit := 2
			for 1<<limit < n {
				limit++
			}
			if probes > limit+2 {
				t.Fatalf("n=%d k=%d: %d probes, want at most %d", n, k, probes, limit+2)
			}
		}
	}
}

func TestBisectNamesADisappearance(t *testing.T) {
	r, err := Bisect(context.Background(), 9, func(_ context.Context, i int) (bool, error) { return i < 6, nil })
	if err != nil || r.First != 6 || !r.OldestHolds {
		t.Fatalf("got %+v, %v", r, err)
	}
	commits := make([]Commit, 9)
	for i := range commits {
		commits[i] = Commit{ID: fmt.Sprintf("v%d", i), Author: "alice", At: t0}
	}
	commits[6].Author, commits[6].AuthorKind, commits[6].Message = "agent:tuner", "agent", "raise the cutoff"
	out := RenderBisect(r, commits, "q")
	for _, want := range []string{"disappears at v6", "agent:tuner [agent]", "raise the cutoff", "previous: v5"} {
		if !strings.Contains(out, want) {
			t.Fatalf("report lacks %q:\n%s", want, out)
		}
	}
}

func TestBisectNoChangeIsAFindingNotAFailure(t *testing.T) {
	_, err := Bisect(context.Background(), 5, func(context.Context, int) (bool, error) { return true, nil })
	if !errors.Is(err, ErrNoChange) {
		t.Fatalf("want ErrNoChange, got %v", err)
	}
}

func TestBisectRejectsBadInputAndPropagatesProbeErrors(t *testing.T) {
	ok := func(context.Context, int) (bool, error) { return true, nil }
	if _, err := Bisect(context.Background(), 1, ok); err == nil {
		t.Fatal("one version cannot be bisected")
	}
	if _, err := Bisect(context.Background(), MaxVersions+1, ok); err == nil {
		t.Fatal("history above the cap must be refused")
	}
	boom := errors.New("boom")
	if _, err := Bisect(context.Background(), 4, func(context.Context, int) (bool, error) { return false, boom }); !errors.Is(err, boom) {
		t.Fatalf("probe errors must surface, got %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Bisect(ctx, 4, ok); !errors.Is(err, context.Canceled) {
		t.Fatalf("a cancelled bisect must stop, got %v", err)
	}
}
