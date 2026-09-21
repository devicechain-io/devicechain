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
// 🔴 IT IS EMPTY, AND THE MAP IS DELIBERATELY KEPT RATHER THAN DELETED WITH ITS LAST
// ENTRY. It is the difference between "nothing is listed" and "nothing was found", and the
// next person to add an unpaced read loop should have to write it here — where the rule
// above says it has to come back out again — rather than discover the mechanism from
// scratch and reach for a way to silence the test.
//
// All nine the scanner originally found are resolved. Seven in event-processing shared one
// package-local readErrorBackoff constant, which is how they came to have the same defect:
// a fixed pause bounds the RATE of the retries and says nothing about how many there may
// be. The constant survives under an honest name, persistRetryBackoffBase, on the persist
// path it actually describes.
//
// The eighth was lwm2m-ingest's Dispatcher.Run, whose entry recorded a claim this guard
// could not check: that a durable broker outage stalls the term's lease renewal, which
// evicts the term and ends the loop. Tested rather than inherited, and it did not earn the
// exemption. Lease.KeepAlive gives up only when Renew FAILS past its TTL window, and the
// lease renews over the SAME connection the reader uses — so it covers a dead broker and
// nothing else. Every error that actually reaches this loop (a 409 at a MaxAckPending or
// MaxWaiting ceiling, a consumer whose leadership keeps moving, a subscription that cannot
// be rebuilt) happens on a HEALTHY connection, where the term renews indefinitely while
// the loop retries once a second forever. It is paced.
//
// The ninth was never a defect: see boundedByOtherMeans.
var knownUnpacedReadLoops = map[string][]string{}

// boundedByOtherMeans is the ONE read loop that is not a defect and that this scanner
// cannot see is not a defect. It is deliberately a SEPARATE list from the debt ledger
// above: "already correct" and "still to fix" are different claims, and a single list
// holding both teaches the next reader to treat every line as noise.
//
// 🔴 IT EXISTS BECAUSE THE SCANNER USED TO GET THIS RIGHT BY ACCIDENT. drainFactToHead
// ends its inner read loop with a `break`, and an earlier version of this guard read that
// as leaving the read loop — so the function was exempted, and the commit that introduced
// the guard claimed the exemption was "computed from the loop'"'"'s own control flow, so it
// cannot be claimed by a loop that does not earn it". That was wrong twice over. The
// `break` leaves the INNER loop and lands in an outer one that reads again, and the same
// reasoning silently exempted every read loop nested inside a second loop.
//
// What actually bounds it is a pass counter the scanner never looks at: the outer loop
// probes, drains what is available, and returns when a pass read NOTHING
// (`if read == 0`), either concluding the catch-up or failing closed on a degraded
// broker. A non-EOF read error returns immediately. So it terminates, and a pacer would
// add nothing.
//
// A line here is a claim that someone READ the function and found the bound. It is worth
// less than a computed exemption, which is why there is exactly one.
var boundedByOtherMeans = map[string][]string{
	"backend/services/event-processing/processor/ResolvedEventsProcessor.go": {"drainFactToHead"},
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

	byFile := map[string][]unpacedReadLoop{}
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
		for _, name := range boundedByOtherMeans[file] {
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

	// The exemption list rots the same way the ledger does, so it is checked the same way:
	// a name here that the scan no longer reports means the loop was paced, rewritten or
	// deleted, and the hand-read justification above it now describes nothing.
	for _, file := range sortedKeys(boundedByOtherMeans) {
		present := map[string]bool{}
		for _, u := range byFile[file] {
			present[u.Function] = true
		}
		for _, name := range boundedByOtherMeans[file] {
			if present[name] {
				continue
			}
			t.Errorf("boundedByOtherMeans exempts %s in %s, but the scan no longer reports it "+
				"at all. The exemption is inert and the paragraph justifying it now describes "+
				"nothing: delete the entry", name, file)
		}
	}

	// An entry whose slice is empty lists nothing that can be found or missed, so it slips
	// past both loops above and sits here forever looking like outstanding work.
	for _, file := range sortedKeys(knownUnpacedReadLoops) {
		if len(knownUnpacedReadLoops[file]) == 0 {
			t.Errorf("knownUnpacedReadLoops has an empty entry for %s; delete the line rather "+
				"than leaving a file listed with nothing outstanding in it", file)
		}
	}
}

// A scan that parsed nothing reports no findings, which reads exactly like a scan that
// found nothing wrong. unpacedReadLoopsUnder refuses rather than returning that answer,
// and this is the test that the refusal works — it had none until a review pointed out
// that the sibling guard tests its equivalent and this one did not.
func TestAScanThatParsesNothingRefusesRatherThanReportingClean(t *testing.T) {
	found, _, err := unpacedReadLoopsUnder(t.TempDir())
	if err == nil {
		t.Fatalf("scanning an empty directory returned %d findings and no error; a scan that "+
			"read no files must say so, because silence is this guard's failure mode", len(found))
	}
}
