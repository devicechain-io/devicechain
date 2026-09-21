// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package test

import (
	"path/filepath"
	"strings"
	"testing"
)

// knownUnpacedReadLoops is the REMAINING debt: functions that read from a message reader
// and can come back for another read after a failure, with nothing bounding how long the
// failures may go on. Keyed by file, holding the function names.
//
// 🔴 IT IS ENFORCED IN BOTH DIRECTIONS, which is what stops it becoming the usual rotting
// suppression file. A function found but not listed fails, because that is new debt. A
// function listed but not found fails too, because the entry is stale and the next reader
// would take this list as an accurate account of what is left. Adopting a pacer means
// deleting the line in the same commit; the map reaching empty means this check is a plain
// assertion again.
//
// Names, not counts or line numbers. A line number turns every unrelated edit above it
// into a failure here, which teaches people to re-run and paste rather than to read — and
// a bare count cannot tell you WHICH loop moved, which is the only thing worth knowing
// when the number changes.
//
// 🔴 THE EIGHT BELOW ARE ONE DEFECT, NOT EIGHT. Seven share a package-local
// readErrorBackoff constant in event-processing and the eighth is lwm2m-ingest's downlink
// dispatcher; all of them log the error, wait a fixed interval, and go round again with no
// ceiling. The fix is the same at each: a core.ReadPacer field, PauseAfterError on the
// error path, Succeeded on the good one.
//
// 🔴 lwm2m-ingest's Dispatcher.Run CARRIES A CLAIM THIS GUARD CANNOT CHECK — that a
// durable broker outage stalls the term's lease renewal, which evicts the term and ends
// the loop, so it never spins forever. That is plausible and it is UNVERIFIED. Whoever
// takes this entry should test the claim before either pacing it or exempting it; a bound
// nothing exercises is a bound nobody knows they have lost.
var knownUnpacedReadLoops = map[string][]string{
	"backend/services/event-processing/processor/ResolvedEventsProcessor.go": {"readPump", "runRuleConsumer"},
	"backend/services/event-processing/processor/attribute_consumer.go":      {"runAttributeConsumer"},
	"backend/services/event-processing/processor/geofence_set_consumer.go":   {"drainFenceSetStream"},
	"backend/services/event-processing/processor/react_dispatcher.go":        {"run"},
	"backend/services/event-processing/processor/roster_consumer.go":         {"runEntityDeletedConsumer", "runRosterConsumer"},
	"backend/services/lwm2m-ingest/downlink/dispatcher.go":                   {"Run"},
}

// Every read loop in the repository must bound how long it will retry a failing read.
//
// 🔴 WHY A GUARD AND NOT A CHECKLIST — the thing this arc actually proved. Eleven loops
// adopted core.ReadPacer over five passes, and the number still outstanding was counted
// by hand from grep output three separate times: it was written down as three, then as
// seven-plus-one, then as eight. The tree says nine, one of which is not a defect. Each of
// those numbers was recorded in a commit message or a PR body by someone who had the grep
// output in front of them. Hand enumeration did not converge once, so the tree is asked
// instead.
//
// The scanner's own behaviour — that it fires on both retry shapes, on a discarded error,
// and on a break that only leaves a select, and that it stays quiet on a loop that fails
// closed — is pinned in readpacing_test.go. That file is the one that shows this check can
// fail; this one only runs it over the repository.
func TestEveryReadLoopInTheRepositoryBoundsItsRetries(t *testing.T) {
	root := workspaceRoot(t)
	found, visited, err := unpacedReadLoopsUnder(root)
	if err != nil {
		t.Fatalf("scanning %s: %v", root, err)
	}
	for _, dir := range workspaceModuleDirs(t, root) {
		abs, err := filepath.Abs(dir)
		if err != nil {
			t.Fatalf("resolving %s: %v", dir, err)
		}
		if !visited[abs] {
			t.Errorf("the scan of %s never descended into %s, so whatever it reports about "+
				"that tree it did not look at. A clean scan and a scan that reached nothing "+
				"are the same answer, which is why this is checked separately", root, abs)
		}
	}

	byFile := map[string][]UnpacedReadLoop{}
	for _, u := range found {
		rel, err := filepath.Rel(root, u.File)
		if err != nil {
			rel = u.File
		}
		rel = filepath.ToSlash(rel)
		byFile[rel] = append(byFile[rel], u)
	}

	for _, file := range sortedKeys(byFile) {
		allowed := map[string]bool{}
		for _, name := range knownUnpacedReadLoops[file] {
			allowed[name] = true
		}
		for _, u := range byFile[file] {
			if allowed[u.Function] {
				continue
			}
			t.Errorf("%s: %s reads from a message reader and comes back for another read after "+
				"a failure, with nothing bounding how long that may go on. A read error that "+
				"never clears leaves the pod reporting ready while it consumes nothing, and "+
				"spinning slower is not making progress. Give it a core.ReadPacer: build one "+
				"with core.NewReadPacer(ms, %q), call PauseAfterError(ctx, err) on the error "+
				"path and return when it reports stop, and call Succeeded() after a good read "+
				"so the budget bounds an unbroken run rather than a lifetime. If this loop "+
				"instead leaves on EVERY read error, make it do so plainly — the guard reads "+
				"that and stays quiet",
				u.Pos, u.Function, strings.TrimSuffix(filepath.Base(file), ".go"))
		}
	}

	for _, file := range sortedKeys(knownUnpacedReadLoops) {
		present := map[string]bool{}
		for _, u := range byFile[file] {
			present[u.Function] = true
		}
		for _, name := range knownUnpacedReadLoops[file] {
			if present[name] {
				continue
			}
			t.Errorf("knownUnpacedReadLoops lists %s in %s but the scan does not find it. The "+
				"entry is stale: delete the line, and the file's whole entry if that empties "+
				"it. An over-stated ledger is how a suppression list outlives the debt it "+
				"records, and it is read by the next person as what is left to do", name, file)
		}
	}

	// A ledger entry naming a file the scan never parsed would sit here forever looking
	// like outstanding work. The loop above catches that only if the file still exists;
	// this catches the rename.
	for _, file := range sortedKeys(knownUnpacedReadLoops) {
		if len(knownUnpacedReadLoops[file]) == 0 {
			t.Errorf("knownUnpacedReadLoops has an empty entry for %s; delete the line rather "+
				"than leaving a file listed with nothing outstanding in it", file)
		}
	}
}
