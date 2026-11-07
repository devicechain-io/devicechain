// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package perturb

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/devicechain-io/dc-event-processing/internal/preview"
)

// TestDemoTranscript prints the fork the way an operator reads it: reality, the fork, and the
// divergence ribbon. Run it with -v; it asserts the headline so the transcript cannot rot:
// the heatwave raises alarms that reality never had, and a chiller lost at 12:30 erases the one
// reality did.
//
//	go test ./internal/perturb -run TestDemoTranscript -v
func TestDemoTranscript(t *testing.T) {
	src := plant(t) // three chillers, 12:00-12:50, one real excursion (chiller-3 at 12:40)
	scenarios := []struct {
		title string
		specs []Spec
	}{
		{"Heatwave: every sensor reads 5 degrees hotter", []Spec{{Op: OpMetricOffset, Metric: "temperature", Offset: 5}}},
		{"Chiller-3 lost at 12:30", []Spec{{Op: OpDropDevice, Device: "chiller-3", From: base.Add(30 * time.Minute)}}},
		{"Gateway dark 12:10-12:30 (chiller-2), then a heatwave", []Spec{
			{Op: OpBlackout, Device: "chiller-2", From: base.Add(10 * time.Minute), To: base.Add(30 * time.Minute)},
			{Op: OpMetricOffset, Metric: "temperature", Offset: 5},
		}},
	}
	reality := run(t, src, nil)
	var out strings.Builder
	fmt.Fprintf(&out, "\nREALITY  %s\n", ribbon(reality.Firings))
	headline := map[string]int{}
	for _, sc := range scenarios {
		fork := run(t, src, sc.specs)
		div := Diff(reality.Firings, fork.Firings, 0)
		fmt.Fprintf(&out, "\nFORK  %s\n", sc.title)
		for _, s := range sc.specs {
			fmt.Fprintf(&out, "  perturb  %s\n", s.Describe())
		}
		fmt.Fprintf(&out, "  fork     %s\n", ribbon(fork.Firings))
		for _, d := range div {
			sig := map[bool]string{true: "RAISE", false: "RESOLVE"}[d.Raise]
			extra := ""
			if d.Kind == Shifted {
				extra = fmt.Sprintf("  (%+v vs reality %s)", d.Delta, d.Baseline.Format("15:04"))
			}
			fmt.Fprintf(&out, "  ribbon   %s  %-8s %-9s %-7s%s\n", d.At.Format("15:04"), d.Kind, d.Series, sig, extra)
			headline[sc.title+d.Kind]++
		}
		fmt.Fprintf(&out, "  summary  %d diverged\n", len(div))
	}
	fmt.Print(out.String())

	if headline[scenarios[0].title+Added] < 2 {
		t.Fatalf("the heatwave should add alarms reality never had: %v", headline)
	}
	if headline[scenarios[1].title+Removed] != 2 {
		t.Fatalf("losing chiller-3 should erase its one real alarm (raise and resolve): %v", headline)
	}
}

func ribbon(fs []preview.Firing) string {
	if len(fs) == 0 {
		return "calm (no alarms)"
	}
	parts := make([]string, 0, len(fs))
	for _, f := range fs {
		sig := map[bool]string{true: "RAISE", false: "RESOLVE"}[f.Raise]
		parts = append(parts, fmt.Sprintf("%s %s@%s", sig, f.Series, f.OccurredAt.Format("15:04")))
	}
	return strings.Join(parts, ", ")
}

// TestNoOpForkHasZeroDivergence is the negative control: a perturbation that changes nothing
// (offset 0, scale 1; a blackout over a window holding no events; a drop of a device that never
// reported) must replay to exactly reality, through the real detector. A diff that reported
// divergence here would be measuring itself, not the perturbation.
func TestNoOpForkHasZeroDivergence(t *testing.T) {
	src := plant(t)
	reality := run(t, src, nil)
	if len(reality.Firings) == 0 {
		t.Fatal("the control needs a reality that fires, or an empty diff proves nothing")
	}
	one := 1.0
	for name, specs := range map[string][]Spec{
		"offset 0, scale 1": {{Op: OpMetricOffset, Metric: "temperature", Offset: 0, Scale: &one}},
		"empty blackout":    {{Op: OpBlackout, Device: "chiller-1", From: base.Add(24 * time.Hour), To: base.Add(25 * time.Hour)}},
		"unknown device":    {{Op: OpDropDevice, Device: "chiller-99"}},
	} {
		fork := run(t, src, specs)
		if div := Diff(reality.Firings, fork.Firings, 0); len(div) != 0 {
			t.Errorf("%s: a no-op diverged: %+v", name, div)
		}
	}
}
