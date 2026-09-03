// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"fmt"
	"sort"
	"strings"
)

// replayExemptions names the migrations that are known NOT to be re-runnable, and why.
//
// There is one legitimate reason to be on this list: the migration is a FROZEN pre-GA
// baseline that is not re-runnable and cannot be edited to become so. CLAUDE.md forbids
// editing a baseline, with one bar an edit can clear — the change must alter
// re-runnability ONLY, with `verify` and `replay` proving the schema byte-identical, which
// is how event-management's was re-cut and this list emptied — its statements are a snapshot of a point in
// time, and rewriting one silently changes what a *fresh* install builds while every
// existing database applies cleanly and looks healthy. The pre-GA remedy for a
// half-applied baseline is `dcctl destroy` + `bootstrap`, not a repair migration.
//
// 🔴 AN EXEMPTION IS AN EXPECTED FAILURE, NOT A SKIP, and that distinction is the whole
// design. A skipped migration is never run, so nothing notices when the defect changes,
// is fixed, or is joined by a second one. Worse, the obvious staleness check — "does this
// id still exist?" — cannot fire on the event this entry itself proposes as the remedy: a
// re-cut baseline KEEPS ITS ID. event-management's was re-cut once already and is still
// `20260729000000`. A skip would then silence a brand-new baseline that had never been
// replay-tested in its life, while the entry read as current.
//
// So each entry names the SYMPTOM, and the gate RUNS the migration and requires it to
// fail with that symptom. An exemption that starts passing is itself a failure ("delete
// this entry"); an exemption that fails differently is a failure ("the defect moved").
// It is a pinned known-defect test, which is the only kind of exception that keeps
// telling the truth after the thing it excused has changed.
//
// A migration you are APPENDING never belongs here. Make it re-runnable instead; the
// gate's error message lists the causes that are almost always the answer.
var replayExemptions = []replayExemption{
	// EMPTY, AND THAT IS THE GOAL STATE RATHER THAN AN OVERSIGHT.
	//
	// There was one entry: event-management's frozen pre-GA baseline, which could not
	// survive its own replay. It re-typed six columns to COLLATE "C" and later built a
	// continuous aggregate over them, so a replay — which is what gormigrate does when a
	// migration fails partway, since these run with UseTransaction:false — hit "cannot
	// alter type of a column used by a view or rule". The operator saw event-management
	// crash-looping on an error naming a VIEW, with no mention of the wedged migration,
	// on a schema too half-built to serve, and no forward path but destroy + bootstrap.
	//
	// That was acceptable pre-GA and would not have been after: a released instance
	// cannot be told to destroy itself. So the baseline was re-cut to be re-runnable —
	// the collation is skipped when already C, the five derived index names are written
	// out with IF NOT EXISTS, and the aggregate is created only when absent. The
	// resulting schema is byte-identical, which is why that re-cut needed no golden
	// update and no instance recreation.
	//
	// 🔴 Two things that re-cut taught, worth more than the entry it removed. The fix
	// everyone reaches for first — "put the collation before the aggregate" — does
	// nothing: it was already before it, and the problem is that the aggregate SURVIVES
	// the failed run. And fixing the collation only moved the failure: the migration then
	// ran twice without erroring while DROPPING AND RECREATING the aggregate, discarding
	// its materialization. This gate caught that second one because it compares the
	// schema and the Timescale catalog rather than the exit status.
	//
	// If you are about to add an entry here, re-read the paragraph above the type: the
	// only legitimate reason is a frozen baseline that cannot be edited, and that reason
	// is now weaker than it was, because one was edited on purpose and it cost nothing.
}

type replayExemption struct {
	area string
	id   string
	// symptom is a substring the replay failure must contain. It is what turns this entry
	// from a skip into an assertion: the gate requires the migration to fail, and to fail
	// THIS way.
	//
	// 🔴 Pin the STATEMENT, not just the database's complaint. "cannot alter type of a
	// column used by a view or rule" is a generic Postgres message; it would still match
	// if the failure moved to a different table, a different column, or a gorm AutoMigrate
	// re-type — and the gate would report "fails as registered" about a defect nobody had
	// looked at. Including the migration's own prefix ("collate measurement_events.
	// device_token:") makes the entry describe one statement.
	symptom string
	// reason states the defect and its blast radius in one line, in the operator's terms
	// — not "baseline, frozen", which says nothing about what happens.
	reason string
}

func replayExemptionFor(area, id string) (replayExemption, bool) {
	for _, e := range replayExemptions {
		if e.area == area && e.id == id {
			return e, true
		}
	}
	return replayExemption{}, false
}

// assertExemptionsResolve fails when an exemption names a migration that no longer exists
// in any chain.
//
// 🔴 The same shape as assertGoldensCovered, and here for the same reason: a stale entry
// is invisible. It silences nothing, breaks nothing, and reports nothing — it just sits
// there implying a defect that was fixed, and the next reader takes it for current.
//
// This is NOT the main staleness guard, and on its own it would be a weak one. It catches
// a renamed or deleted id; the expected-failure run in runReplay catches the much likelier
// case of an entry that is simply no longer true.
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
		"The migration was renamed or removed. Delete the exemption, or correct the id — an "+
		"exemption nothing matches quietly outlives the defect it excused.",
		strings.Join(stale, ", "))
}
