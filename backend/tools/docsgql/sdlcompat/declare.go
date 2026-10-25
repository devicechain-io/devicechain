// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package sdlcompat

import (
	"bufio"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strings"
)

// Declaration is one intended breaking change, written in breaks.txt as
//
//	<baseline-tag> <schema> <coordinate> <kind> <reason…>
//
// for example
//
//	v0.24.0 device-management Query.devicesByToken field-removed Replaced by devices(tokens:), which pages.
type Declaration struct {
	Baseline string
	Schema   string
	Coord    string
	Kind     string
	Reason   string
	Line     int
}

func (d Declaration) key() string { return d.Schema + "\x00" + d.Coord + "\x00" + d.Kind }

var stableTag = regexp.MustCompile(`^v\d+\.\d+\.\d+$`)

// ParseDeclarations reads a declarations file. Blank lines and lines starting with '#'
// are ignored. A line that is not a valid declaration is returned as a Finding rather
// than skipped: a malformed declaration that is silently dropped turns a declared break
// back into a failure nobody can explain, and one that is silently accepted could cover
// a change it does not name.
func ParseDeclarations(r io.Reader, file string) ([]Declaration, []Finding) {
	var decls []Declaration
	var bad []Finding
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
			bad = append(bad, Finding{where, "", "a declaration is `<baseline-tag> <schema> <coordinate> <kind> <reason>`, and the reason is required"})
			continue
		}
		d := Declaration{Baseline: f[0], Schema: f[1], Coord: f[2], Kind: f[3], Reason: strings.Join(f[4:], " "), Line: n}
		if !stableTag.MatchString(d.Baseline) {
			bad = append(bad, Finding{where, d.Coord, fmt.Sprintf("baseline %q is not a stable release tag (vX.Y.Z)", d.Baseline)})
			continue
		}
		sev, ok := severities[d.Kind]
		switch {
		case !ok:
			bad = append(bad, Finding{where, d.Coord, fmt.Sprintf("%q is not a change kind; the failure that asked for this declaration names the kind to use", d.Kind)})
			continue
		case sev != Breaking:
			bad = append(bad, Finding{where, d.Coord, fmt.Sprintf("%s is %s, not breaking, and never fails the check; there is nothing to declare", d.Kind, sev)})
			continue
		}
		if prev, dup := seen[d.Baseline+"\x00"+d.key()]; dup {
			bad = append(bad, Finding{where, d.Coord, fmt.Sprintf("declared twice (first on line %d)", prev)})
			continue
		}
		seen[d.Baseline+"\x00"+d.key()] = n
		decls = append(decls, d)
	}
	if err := sc.Err(); err != nil {
		bad = append(bad, Finding{file, "", err.Error()})
	}
	return decls, bad
}

// Result is the verdict over one comparison.
type Result struct {
	// Undeclared are breaking changes no live declaration covers. Any one fails.
	Undeclared []Change
	// Declared are breaking changes a live declaration covers, with that declaration.
	Declared []DeclaredChange
	// Stale are live declarations (for this baseline) that match no change. They fail:
	// a declaration that covers nothing is either a typo — in which case the break it
	// meant is in Undeclared — or a break that was reverted and left its excuse behind.
	Stale []Declaration
	// Expired are declarations written against an earlier baseline. Once that release
	// is no longer the baseline the break they excused is part of the baseline itself,
	// so they cover nothing and are reported for deletion. They never fail: the release
	// tag is what expires them, and a tag push must not turn the main branch red.
	Expired []Declaration
	// Dangerous changes are reported, never failing.
	Dangerous []Change
	// Safe counts the safe changes.
	Safe int
}

// Failed reports whether the check fails.
func (r Result) Failed() bool { return len(r.Undeclared) > 0 || len(r.Stale) > 0 }

// DeclaredChange pairs a breaking change with the declaration that covers it.
type DeclaredChange struct {
	Change      Change
	Declaration Declaration
}

// Evaluate decides the check. A declaration is LIVE only while its baseline is the
// baseline being compared against; it covers exactly one (schema, coordinate, kind).
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
			k := c.Schema + "\x00" + c.Coord + "\x00" + c.Kind
			if d, ok := live[k]; ok {
				used[k] = true
				r.Declared = append(r.Declared, DeclaredChange{c, d})
			} else {
				r.Undeclared = append(r.Undeclared, c)
			}
		case Dangerous:
			r.Dangerous = append(r.Dangerous, c)
		default:
			r.Safe++
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
	return fmt.Sprintf("%s %s %s %s <why, and what a client uses instead>", baseline, c.Schema, c.Coord, c.Kind)
}
