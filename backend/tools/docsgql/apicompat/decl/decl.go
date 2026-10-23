// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package decl is the part of the API-stability gate every non-GraphQL surface shares:
// what a change is, how an intended break is declared against a baseline release, and
// how a set of changes is judged against the declarations.
//
// It carries the same rules the GraphQL gate (package sdlcompat) applies, in a form the
// other surfaces (the dcctl command tree, the device wire contract, the device topic
// layout) reuse without each growing its own parser:
//
//   - a declaration is `<baseline-tag> <surface> <coordinate> <kind> <reason...>`, and
//     covers exactly one (surface, coordinate, kind);
//   - it is LIVE only while its baseline tag is the baseline being compared against, so
//     it expires by itself when the next release is tagged, and an expired line never
//     fails (a tag push must not turn main red) and never hides a new break;
//   - a live declaration that matches no change is STALE and fails, so the list cannot
//     over-cover.
package decl

import (
	"bufio"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strings"
)

// Severity classifies a change from the point of view of something written against
// the baseline.
type Severity int

const (
	// Safe changes are additions. Nothing written against the baseline notices.
	Safe Severity = iota
	// Dangerous changes are reported and never fail: the policy tells readers to
	// tolerate them (a new enum value) or the tool cannot tell whether they hurt (a
	// changed default, a changed pattern).
	Dangerous
	// Breaking changes fail unless declared.
	Breaking
)

func (s Severity) String() string {
	switch s {
	case Breaking:
		return "breaking"
	case Dangerous:
		return "dangerous"
	default:
		return "safe"
	}
}

// Change is one difference between the baseline and the working tree.
type Change struct {
	// Surface names what changed at the granularity a declaration is written for: for
	// dcctl the constant "dcctl", for the wire contract the schema file, for the topic
	// layout "topics".
	Surface string
	// Coord is the element, with no whitespace: a command path, a command's flag, a
	// JSON-Schema path, a topic.
	Coord string
	// Kind is the change kind, a short hyphenated word the failure prints and a
	// declaration repeats.
	Kind     string
	Severity Severity
	// Detail is a human sentence about the difference.
	Detail string
}

func (c Change) key() string { return c.Surface + "\x00" + c.Coord + "\x00" + c.Kind }

func (c Change) String() string {
	s := fmt.Sprintf("%s: %s %s", c.Surface, c.Coord, c.Kind)
	if c.Detail != "" {
		s += " (" + c.Detail + ")"
	}
	return s
}

// Declaration is one intended breaking change.
type Declaration struct {
	Baseline string
	Surface  string
	Coord    string
	Kind     string
	Reason   string
	Line     int
}

func (d Declaration) key() string { return d.Surface + "\x00" + d.Coord + "\x00" + d.Kind }

var stableTag = regexp.MustCompile(`^v\d+\.\d+\.\d+$`)

// Parse reads a declarations file. Blank lines and lines starting with '#' are
// ignored. kinds maps every change kind the surface can emit to its severity; a line
// naming a kind that is unknown, or that is not breaking, is returned as a problem
// rather than skipped: a malformed declaration silently dropped turns a declared break
// back into a failure nobody can explain, and one silently accepted could cover a
// change it does not name.
func Parse(r io.Reader, file string, kinds map[string]Severity) ([]Declaration, []string) {
	var decls []Declaration
	var bad []string
	seen := map[string]int{}
	sc := bufio.NewScanner(r)
	n := 0
	for sc.Scan() {
		n++
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		where := fmt.Sprintf("%s:%d", file, n)
		f := strings.Fields(line)
		if len(f) < 5 {
			bad = append(bad, where+": a declaration is `<baseline-tag> <surface> <coordinate> <kind> <reason>`, and the reason is required")
			continue
		}
		d := Declaration{Baseline: f[0], Surface: f[1], Coord: f[2], Kind: f[3], Reason: strings.Join(f[4:], " "), Line: n}
		if !stableTag.MatchString(d.Baseline) {
			bad = append(bad, fmt.Sprintf("%s: baseline %q is not a stable release tag (vX.Y.Z)", where, d.Baseline))
			continue
		}
		sev, ok := kinds[d.Kind]
		switch {
		case !ok:
			bad = append(bad, fmt.Sprintf("%s: %q is not a change kind of this surface; the failure that asked for this declaration names the kind to use", where, d.Kind))
			continue
		case sev != Breaking:
			bad = append(bad, fmt.Sprintf("%s: %s is %s, not breaking, and never fails the check; there is nothing to declare", where, d.Kind, sev))
			continue
		}
		k := d.Baseline + "\x00" + d.key()
		if prev, dup := seen[k]; dup {
			bad = append(bad, fmt.Sprintf("%s: declared twice (first on line %d)", where, prev))
			continue
		}
		seen[k] = n
		decls = append(decls, d)
	}
	if err := sc.Err(); err != nil {
		bad = append(bad, file+": "+err.Error())
	}
	return decls, bad
}

// DeclaredChange pairs a breaking change with the declaration that covers it.
type DeclaredChange struct {
	Change      Change
	Declaration Declaration
}

// Result is the verdict over one comparison.
type Result struct {
	Undeclared []Change
	Declared   []DeclaredChange
	Stale      []Declaration
	Expired    []Declaration
	Dangerous  []Change
	Safe       []Change
}

// Failed reports whether the check fails.
func (r Result) Failed() bool { return len(r.Undeclared) > 0 || len(r.Stale) > 0 }

// Evaluate judges changes against declarations for the given baseline.
func Evaluate(baseline string, changes []Change, decls []Declaration) Result {
	var r Result
	live := map[string]Declaration{}
	used := map[string]bool{}
	for _, d := range decls {
		if d.Baseline != baseline {
			r.Expired = append(r.Expired, d)
			continue
		}
		live[d.key()] = d
	}
	for _, c := range changes {
		switch c.Severity {
		case Breaking:
			if d, ok := live[c.key()]; ok {
				used[c.key()] = true
				r.Declared = append(r.Declared, DeclaredChange{c, d})
			} else {
				r.Undeclared = append(r.Undeclared, c)
			}
		case Dangerous:
			r.Dangerous = append(r.Dangerous, c)
		default:
			r.Safe = append(r.Safe, c)
		}
	}
	for k, d := range live {
		if !used[k] {
			r.Stale = append(r.Stale, d)
		}
	}
	sort.Slice(r.Stale, func(i, j int) bool { return r.Stale[i].Line < r.Stale[j].Line })
	sort.Slice(r.Expired, func(i, j int) bool { return r.Expired[i].Line < r.Expired[j].Line })
	return r
}

// DeclarationFor is the line that would declare a change against baseline, with the
// reason left for a person to write.
func DeclarationFor(baseline string, c Change) string {
	return fmt.Sprintf("%s %s %s %s <why, and what a user does instead>", baseline, c.Surface, c.Coord, c.Kind)
}

// Sort orders changes deterministically: by surface, coordinate, kind.
func Sort(cs []Change) {
	sort.SliceStable(cs, func(i, j int) bool {
		a, b := cs[i], cs[j]
		if a.Surface != b.Surface {
			return a.Surface < b.Surface
		}
		if a.Coord != b.Coord {
			return a.Coord < b.Coord
		}
		return a.Kind < b.Kind
	})
}

// Print writes the verdict the way every surface of the gate prints it and returns the
// number of failures. what names the surface in the prose ("dcctl command tree"),
// breaksFile is the repo-relative declarations file the failure tells a person to edit.
func Print(w io.Writer, what, baseline, breaksFile string, bad []string, res Result, verbose bool) int {
	failed := 0
	for _, b := range bad {
		failed++
		fmt.Fprintf(w, "FAIL invalid declaration %s\n", b)
	}
	for _, c := range res.Undeclared {
		failed++
		fmt.Fprintf(w, "FAIL breaking change against %s: %s\n", baseline, c)
		fmt.Fprintf(w, "     Something written against %s breaks. Make the change additively (keep the old\n", baseline)
		fmt.Fprintf(w, "     element working and add the new one) or, if the break is intended, declare it in\n")
		fmt.Fprintf(w, "     %s:\n", breaksFile)
		fmt.Fprintf(w, "       %s\n", DeclarationFor(baseline, c))
	}
	for _, d := range res.Stale {
		failed++
		fmt.Fprintf(w, "FAIL %s:%d declares %s: %s %s against %s, but that change is not in the %s; delete the line\n",
			breaksFile, d.Line, d.Surface, d.Coord, d.Kind, d.Baseline, what)
	}
	for _, dc := range res.Declared {
		fmt.Fprintf(w, "declared  %s\n          reason: %s\n", dc.Change, dc.Declaration.Reason)
	}
	for _, c := range res.Dangerous {
		fmt.Fprintf(w, "dangerous %s\n", c)
	}
	for _, d := range res.Expired {
		fmt.Fprintf(w, "expired   %s:%d (%s %s %s) was declared against %s; the baseline is now %s, so it covers nothing - delete it\n",
			breaksFile, d.Line, d.Surface, d.Coord, d.Kind, d.Baseline, baseline)
	}
	if verbose {
		for _, c := range res.Safe {
			fmt.Fprintf(w, "safe      %s\n", c)
		}
	}
	return failed
}
