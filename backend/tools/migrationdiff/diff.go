// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"fmt"
	"sort"
	"strings"
)

// statementDiff compares two normalized statement lists and returns a human-readable
// description of how they differ, or "" when they are equivalent.
//
// 🔴 IT COMPARES WHOLE STATEMENTS, AS A MULTISET, AND BOTH HALVES OF THAT MATTER.
//
// This replaced a set difference over LINES, which could not fail for the most likely
// flatten mistake there is. A pg_dump's CREATE TABLE spans one line per column, so a
// line-level comparison never knows WHICH TABLE a column line belongs to, and a set
// comparison never knows HOW MANY tables have it. Drop `description` from one table of
// four that have a `description character varying(1024),` line and the line is still
// present in the other three, so the set difference is empty and the harness reports
// "matches golden".
//
// That was measured, not reasoned about: removing the column from user-management's
// baseline produced a schema a plain `diff` showed to be one line short of the golden,
// and `verify` exited 0. Between 23% and 54% of the lines in every golden are
// duplicated somewhere else in the same schema and were individually invisible the same
// way — worst in device-management, which is both the largest chain and the hardest
// flatten. The whole correctness of the GA migration squash rests on this comparison,
// and until this change it could not see a missing column.
//
// A statement carries its own object name, so comparing statements fixes the "which
// table" half. The multiset fixes the "how many" half: it is cheap, and it means an
// object duplicated or lost from a group of textually identical ones still shows up
// rather than being absorbed.
func statementDiff(want, got []string) string {
	only, extra := multisetDiff(want, got)
	if len(only) == 0 && len(extra) == 0 {
		return ""
	}
	return render(only, extra)
}

// multisetDiff returns the statements present in want but not got, and vice versa,
// preserving multiplicity: a statement wanted three times and present twice appears
// once in only.
func multisetDiff(want, got []string) (only, extra []string) {
	wantN, gotN := tally(want), tally(got)
	for s, n := range wantN {
		for i := 0; i < n-gotN[s]; i++ {
			only = append(only, s)
		}
	}
	for s, n := range gotN {
		for i := 0; i < n-wantN[s]; i++ {
			extra = append(extra, s)
		}
	}
	sort.Strings(only)
	sort.Strings(extra)
	return only, extra
}

func tally(ss []string) map[string]int {
	n := map[string]int{}
	for _, s := range ss {
		if strings.TrimSpace(s) != "" {
			n[s]++
		}
	}
	return n
}

// render turns the difference into something readable. Detection is already settled by
// multisetDiff; this is presentation only, and it exists because the raw form is
// unusable at the size that matters — a device-management table with thirty columns
// would print twice in full to report one changed column.
//
// Statements are keyed by their first line, which for pg_dump output is the object
// header (`CREATE TABLE "area".devices (`). When a key has exactly one statement missing
// and one added, the two describe the same object and the report narrows to the lines
// that actually moved. Anything else prints whole.
func render(only, extra []string) string {
	onlyByKey, extraByKey := groupByHeader(only), groupByHeader(extra)

	keys := map[string]bool{}
	for k := range onlyByKey {
		keys[k] = true
	}
	for k := range extraByKey {
		keys[k] = true
	}
	sorted := make([]string, 0, len(keys))
	for k := range keys {
		sorted = append(sorted, k)
	}
	sort.Strings(sorted)

	var b strings.Builder
	for _, k := range sorted {
		w, g := onlyByKey[k], extraByKey[k]
		if len(w) == 1 && len(g) == 1 {
			fmt.Fprintf(&b, "~ %s\n%s", k, indentLines(lineLevel(w[0], g[0])))
			continue
		}
		for _, s := range w {
			fmt.Fprintf(&b, "- %s\n", oneLine(s))
		}
		for _, s := range g {
			fmt.Fprintf(&b, "+ %s\n", oneLine(s))
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

func groupByHeader(stmts []string) map[string][]string {
	out := map[string][]string{}
	for _, s := range stmts {
		out[header(s)] = append(out[header(s)], s)
	}
	return out
}

func header(stmt string) string {
	if i := strings.IndexByte(stmt, '\n'); i >= 0 {
		return strings.TrimSpace(stmt[:i])
	}
	return strings.TrimSpace(stmt)
}

// lineLevel reports how two statements for the same object differ.
//
// Count-aware, like the statement comparison — though only for symmetry, not because a
// false green depends on it: lineLevel runs after statementDiff has ALREADY reported a
// difference, so nothing it does can turn a failure into a pass. It is the message a
// human reads to find out what moved.
//
// The two statements are passed whole rather than with their first line sliced off.
// Grouping keys on that first line, so any pair reaching here has an identical one and it
// cancels out on its own.
func lineLevel(want, got string) string {
	wl, gl := strings.Split(want, "\n"), strings.Split(got, "\n")
	only, extra := multisetDiff(wl, gl)
	var b strings.Builder
	for _, l := range only {
		fmt.Fprintf(&b, "- %s\n", strings.TrimSpace(l))
	}
	for _, l := range extra {
		fmt.Fprintf(&b, "+ %s\n", strings.TrimSpace(l))
	}
	if b.Len() == 0 {
		return reordering(wl, gl)
	}
	return b.String()
}

// reordering describes a pair whose lines are identical as a multiset and differ only in
// ORDER. For a CREATE TABLE that is physical column order, which is part of the schema
// and is a leading way a flatten goes wrong: gorm's AutoMigrate emits columns in
// struct-field order, so a snapshot struct that lists its fields differently produces
// exactly this.
//
// 🔴 IT HAS TO NAME THE LINES. This is not a rare branch — swapping two non-final columns
// moves no trailing comma, so the multisets match and this is the path taken by about 70%
// of real single-swap reorderings, measured over the committed goldens. Reporting only
// "the order differs" against a 24-column table leaves a human to hand-diff it, which is
// how a real finding gets waved off.
func reordering(wl, gl []string) string {
	what := "line order"
	if strings.HasPrefix(strings.TrimSpace(wl[0]), "CREATE TABLE") {
		what = "physical column order"
	}
	for i := 0; i < len(wl) && i < len(gl); i++ {
		if wl[i] != gl[i] {
			return fmt.Sprintf("(same lines, different %s — first divergence at line %d)\n- %s\n+ %s\n",
				what, i+1, strings.TrimSpace(wl[i]), strings.TrimSpace(gl[i]))
		}
	}
	return fmt.Sprintf("(same lines, different %s)\n", what)
}

func oneLine(stmt string) string {
	return strings.Join(strings.Fields(strings.ReplaceAll(stmt, "\n", " ")), " ")
}

func indentLines(s string) string {
	var b strings.Builder
	for _, l := range strings.Split(strings.TrimRight(s, "\n"), "\n") {
		fmt.Fprintf(&b, "    %s\n", l)
	}
	return b.String()
}
