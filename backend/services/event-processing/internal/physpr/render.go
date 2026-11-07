// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package physpr

import (
	"fmt"
	"strings"
	"time"
)

// maxListed bounds how many individual changes the report prints; the counts above it are always
// complete, so a truncated list never reads as a complete one.
const maxListed = 25

// Ref names one side of a comparison in the report.
type Ref struct {
	Label  string // "base" / "head", or a commit id
	Author string // the principal that published it, when known
}

func (r Ref) String() string {
	if r.Author == "" {
		return r.Label
	}
	return fmt.Sprintf("%s (%s)", r.Label, r.Author)
}

// RenderDiff writes the pull-request-style report: the headline a reviewer decides on, the
// devices it touches, and the individual changes.
func RenderDiff(d Diff, base, head Ref, window string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Physical PR: %s -> %s\n", base, head)
	fmt.Fprintf(&b, "Replay window: %s\n", window)
	fmt.Fprintf(&b, "Events replayed: base %d, head %d\n\n", d.Base.EventsScanned, d.Head.EventsScanned)

	if d.Empty() {
		fmt.Fprintf(&b, "No behavioural change. The head raises and resolves exactly what the base does over this window (%d alarms, %d resolutions).\n",
			d.Raises.Unchanged, d.Resolves.Unchanged)
	} else {
		fmt.Fprintf(&b, "Alarms:      %s\n", headline(d.Raises, "raised", "suppressed"))
		fmt.Fprintf(&b, "Resolutions: %s\n", headline(d.Resolves, "added", "dropped"))
		if n := len(d.Series); n > 0 {
			fmt.Fprintf(&b, "Devices affected: %d\n", n)
		}
	}

	if d.Head.CalculatedSkips != d.Base.CalculatedSkips || d.Head.EvalErrors != d.Base.EvalErrors {
		fmt.Fprintf(&b, "\nEvaluation health:\n")
		if d.Head.CalculatedSkips != d.Base.CalculatedSkips {
			fmt.Fprintf(&b, "  calculated-metric skips: %d -> %d\n", d.Base.CalculatedSkips, d.Head.CalculatedSkips)
		}
		if d.Head.EvalErrors != d.Base.EvalErrors {
			fmt.Fprintf(&b, "  predicate errors:        %d -> %d\n", d.Base.EvalErrors, d.Head.EvalErrors)
		}
	}

	if len(d.Series) > 0 {
		fmt.Fprintf(&b, "\nMost affected devices:\n")
		for i, s := range d.Series {
			if i == 10 {
				fmt.Fprintf(&b, "  ... and %d more\n", len(d.Series)-i)
				break
			}
			fmt.Fprintf(&b, "  %-24s %s\n", s.Series, seriesLine(s))
		}
	}

	if len(d.Changes) > 0 {
		fmt.Fprintf(&b, "\nChanges:\n")
		for i, c := range d.Changes {
			if i == maxListed {
				fmt.Fprintf(&b, "  ... and %d more\n", len(d.Changes)-i)
				break
			}
			fmt.Fprintf(&b, "  %s\n", changeLine(c))
		}
	}

	var notes []string
	if d.BaseDegraded != "" {
		notes = append(notes, "base replay is partial: "+d.BaseDegraded)
	}
	if d.HeadDegraded != "" {
		notes = append(notes, "head replay is partial: "+d.HeadDegraded)
	}
	if len(notes) > 0 {
		fmt.Fprintf(&b, "\nCaution: %s. This diff is evidence over what was replayed, not proof about the rest.\n", strings.Join(notes, "; "))
	}
	return b.String()
}

func headline(c Counts, added, removed string) string {
	if c.Changed() == 0 {
		return fmt.Sprintf("unchanged (%d)", c.Unchanged)
	}
	var parts []string
	if c.Added > 0 {
		parts = append(parts, fmt.Sprintf("+%d "+added, c.Added))
	}
	if c.Removed > 0 {
		parts = append(parts, fmt.Sprintf("-%d "+removed, c.Removed))
	}
	if c.Shifted > 0 {
		parts = append(parts, fmt.Sprintf("%d moved", c.Shifted))
	}
	parts = append(parts, fmt.Sprintf("%d unchanged", c.Unchanged))
	return strings.Join(parts, ", ")
}

func seriesLine(s SeriesDelta) string {
	var parts []string
	if s.Added > 0 {
		parts = append(parts, fmt.Sprintf("+%d %s", s.Added, plural(s.Added, "alarm")))
	}
	if s.Removed > 0 {
		parts = append(parts, fmt.Sprintf("-%d %s", s.Removed, plural(s.Removed, "alarm")))
	}
	if s.Shifted > 0 {
		switch {
		case s.Earliest < 0 && s.Latest > 0:
			parts = append(parts, fmt.Sprintf("%d moved (up to %s earlier, up to %s later)", s.Shifted, human(-s.Earliest), human(s.Latest)))
		case s.Earliest < 0:
			parts = append(parts, fmt.Sprintf("%d fire up to %s earlier", s.Shifted, human(-s.Earliest)))
		default:
			parts = append(parts, fmt.Sprintf("%d fire up to %s later", s.Shifted, human(s.Latest)))
		}
	}
	return strings.Join(parts, ", ")
}

func changeLine(c Change) string {
	what := "alarm"
	if c.Edge == Resolve {
		what = "resolution"
	}
	switch c.Kind {
	case Added:
		return fmt.Sprintf("+ %s on %s at %s", what, c.Series, ts(c.Head))
	case Removed:
		return fmt.Sprintf("- %s on %s at %s", what, c.Series, ts(c.Base))
	default:
		dir := "later"
		if c.Shift < 0 {
			dir = "earlier"
		}
		return fmt.Sprintf("~ %s on %s now at %s, %s %s (was %s)", what, c.Series, ts(c.Head), human(absDur(c.Shift)), dir, ts(c.Base))
	}
}

func plural(n int, noun string) string {
	if n == 1 {
		return noun
	}
	return noun + "s"
}

func ts(t time.Time) string { return t.UTC().Format("2006-01-02 15:04:05Z") }

func human(d time.Duration) string {
	d = d.Round(time.Second)
	if d >= time.Hour {
		return fmt.Sprintf("%dh%02dm", int(d.Hours()), int(d.Minutes())%60)
	}
	if d >= time.Minute {
		return fmt.Sprintf("%d minutes", int(d.Minutes()))
	}
	return fmt.Sprintf("%d seconds", int(d.Seconds()))
}

// Commit is one entry of a rule's published history.
type Commit struct {
	ID         string
	Author     string // the principal that published it
	AuthorKind string // "human" or "agent"
	Message    string
	At         time.Time
}

// RenderBisect writes the verdict: the commit that changed the outcome and who published it.
func RenderBisect(r BisectResult, commits []Commit, question string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Bisect: %s\n", question)
	fmt.Fprintf(&b, "History: %d versions, %d replays\n\n", len(commits), len(r.Trail))
	for _, s := range r.Trail {
		state := "no"
		if s.Holds {
			state = "yes"
		}
		fmt.Fprintf(&b, "  %-12s %s\n", commits[s.Version].ID, state)
	}
	c := commits[r.First]
	prev := commits[r.First-1]
	to := "appears"
	if r.OldestHolds {
		to = "disappears"
	}
	fmt.Fprintf(&b, "\nThe outcome %s at %s.\n", to, c.ID)
	fmt.Fprintf(&b, "  commit:    %s (previous: %s)\n", c.ID, prev.ID)
	kind := ""
	if c.AuthorKind != "" {
		kind = " [" + c.AuthorKind + "]"
	}
	fmt.Fprintf(&b, "  published: %s by %s%s\n", ts(c.At), c.Author, kind)
	if c.Message != "" {
		fmt.Fprintf(&b, "  message:   %s\n", c.Message)
	}
	return b.String()
}
