// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"fmt"
	"sort"
	"strings"
)

// replayExemptions names the migrations the replay gate does NOT check, and why.
//
// There is exactly one legitimate reason to be on this list: the migration is a FROZEN
// pre-GA baseline that is not re-runnable and cannot be edited to become so. CLAUDE.md
// forbids editing a baseline categorically — its statements are a snapshot of a point in
// time, and rewriting one silently changes what a *fresh* install builds while every
// existing database applies cleanly and looks healthy. The pre-GA remedy for a
// half-applied baseline is `dcctl destroy` + `bootstrap`, not a repair migration.
//
// 🔴 An exemption is a KNOWN DEFECT with a stated reason, not an absolution. Each entry
// says what breaks and what the operator sees, so that "it is exempt" never quietly
// becomes "it is fine".
//
// A migration you are APPENDING never belongs here. Make it re-runnable instead; the
// gate's error message lists the causes that are almost always the answer.
var replayExemptions = []replayExemption{
	{
		area: "event-management",
		id:   "20260729000000",
		reason: "frozen pre-GA baseline; a replay dies on ALTER COLUMN ... COLLATE because the " +
			"continuous aggregate it later creates reads those columns (see below)",
	},
}

// The event-management baseline, stated in full, because the one-line reason above is a
// label and this is the defect.
//
// WHAT HAPPENS. The baseline creates the hypertables, re-types six columns to
// `varchar(128) COLLATE "C"`, creates the indexes, and then creates the
// measurement_rollups continuous aggregate over those same columns. Run forward on an
// empty schema that order is fine. Replayed — which is what gormigrate does when the
// baseline fails partway, since migrations run with UseTransaction:false and nothing is
// rolled back — the aggregate is already there, and Postgres refuses the re-type with
// "cannot alter type of a column used by a view or rule" (SQLSTATE 0A000).
//
// WHAT AN OPERATOR SEES. event-management crash-looping on an error that names a view,
// with no mention of the migration that is wedged. Every retry fails identically. The
// schema is half-built, so the service cannot serve either. There is no forward path:
// the fix is `dcctl destroy` + `bootstrap`, which is the pre-GA remedy for a baseline and
// is why this is a registered exemption rather than a release blocker today.
//
// WHAT THIS EXEMPTION ALSO HIDES. The baseline creates five indexes with no name
// (baseline.go:140-145 — deliberately, so their Postgres-derived names match the golden).
// An unnamed CREATE INDEX does not fail on replay; Postgres finds the derived name taken
// and silently creates "<name>1". So past the collation failure this baseline would also
// leave five duplicate indexes — written on every insert, read by nothing. The gate's
// before/after schema comparison would report those, and cannot, because the replay
// aborts earlier. Both defects sit in the same frozen file and neither can be fixed
// without re-cutting it.
//
// 🔴 THIS IS A DECISION TO REVISIT BEFORE v1.0.0, not a closed one. Pre-GA, "recreate the
// instance" is an accepted remedy and the exemption is honest. After GA it is not: a
// released instance cannot be told to destroy itself, so a baseline that cannot survive
// its own replay is a permanent trap for whoever hits an infrastructure blip mid-migration.
// The choice at the cut line is to re-cut this baseline (moving the collation ahead of the
// aggregate and naming the five indexes) or to ship knowing this.

// replayExemption is one registered non-re-runnable migration.
type replayExemption struct {
	area string
	id   string
	// reason states the defect and its blast radius in one line, in the operator's
	// terms — not "baseline, frozen", which says nothing about what happens.
	reason string
}

func replayExemptionFor(area, id string) (string, bool) {
	for _, e := range replayExemptions {
		if e.area == area && e.id == id {
			return e.reason, true
		}
	}
	return "", false
}

// assertExemptionsResolve fails when an exemption names a migration that no longer
// exists in any chain.
//
// 🔴 This is the same shape as assertGoldensCovered, and it is here for the same reason:
// a stale entry is invisible. It silences nothing, breaks nothing, and reports nothing —
// it just sits there implying a defect that was fixed or a baseline that was re-cut, and
// the next reader takes it for current. A registry of exceptions has to be checked
// against the thing it makes exceptions to, or it decays into folklore.
func assertExemptionsResolve(all []area) error {
	live := map[string]bool{}
	for _, a := range all {
		for _, m := range a.migrations {
			live[a.name+"/"+m.ID] = true
		}
	}
	var stale []string
	for _, e := range replayExemptions {
		if !live[e.area+"/"+e.id] {
			stale = append(stale, e.area+"/"+e.id)
		}
	}
	if len(stale) == 0 {
		return nil
	}
	sort.Strings(stale)
	return fmt.Errorf("replay exemption(s) naming a migration that is not in any chain: %s\n"+
		"The migration was renamed, removed, or its baseline re-cut. Delete the exemption, or "+
		"correct the id — an exemption nothing matches quietly outlives the defect it excused.",
		strings.Join(stale, ", "))
}
