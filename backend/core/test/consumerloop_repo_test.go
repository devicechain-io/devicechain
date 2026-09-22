// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package test

import (
	"os"
	"path/filepath"
	"testing"
)

// readsItsOwnStream is the set of production functions allowed to call the reader for
// themselves instead of going through messaging.RunConsumer. Keyed by repo-relative file,
// holding function names.
//
// 🔴 IT IS ENFORCED IN BOTH DIRECTIONS, which is what stops it becoming the usual rotting
// suppression file. A function found but not listed fails, because that is a new copy of the
// loop. A function listed but not found fails too, because the entry is stale and the next
// reader would take this list as an accurate account of what is exempt.
//
// 🔴 A LINE HERE IS A CLAIM THAT SOMEONE READ THE FUNCTION AND FOUND THE REASON, which is
// why there are two and why each one says what it is. "It is complicated" is not a reason;
// the two below are both cases where the loop's SHAPE differs, not just its body.
var readsItsOwnStream = map[string][]string{
	// The loop itself. Every other consumer in the tree reaches the reader through it.
	consumerLoopHome: {"RunConsumer"},

	"backend/services/event-processing/processor/ResolvedEventsProcessor.go": {
		// drainFactToHead FAILS CLOSED: it returns a read error up to a caller that aborts
		// the catch-up, so it never retries one and has nothing to pace. Giving it
		// RunConsumer would mean ADDING a retry in order to bound it. Its bound is a pass
		// counter — the outer loop returns when a pass read nothing — which is also why the
		// pacing guard next door exempts it by hand.
		"ResolvedEventsProcessor.drainFactToHead",

		// readPump FORWARDS the read error down a channel to its worker rather than handling
		// it at the read, so the error's disposition is not the loop's to make. It is the one
		// reader→chan→worker pipeline where the ERROR travels with the message, and #928's
		// own guidance is to extract the loop, not the pipeline. It paces, on the same
		// core.ReadPacer as everything else; what it does not do is handle.
		"ResolvedEventsProcessor.readPump",
	},
}

// Every production read loop in the repository must be messaging.RunConsumer.
//
// 🔴 WHY THE LOOP AND NOT JUST THE PACING. The pacing guard next door asks whether a loop
// pauses after a failed read. It cannot ask whether the loop RESETS the run of failures
// after a good one, and says so: that is a different defect with a different fix. A
// hand-rolled loop can therefore satisfy every check in this repository while omitting the
// half of core.ReadPacer's contract that ends a healthy process hours later. Nineteen copies
// of this loop existed across ten services; all nineteen happened to pair the calls
// correctly, which is a property of the people who wrote them and not of anything that was
// checked. Seventeen are now one. This keeps it that way.
func TestEveryProductionReadLoopIsTheSharedOne(t *testing.T) {
	root := workspaceRoot(t)
	found, visited, err := handRolledReadLoopsUnder(root)
	if err != nil {
		t.Fatalf("scanning %s: %v", root, err)
	}
	for _, dir := range workspaceModuleDirs(t, root) {
		abs, err := filepath.Abs(dir)
		if err != nil {
			t.Fatalf("resolving %s: %v", dir, err)
		}
		if !visited[abs] {
			t.Errorf("the scan of %s never descended into %s, so whatever it reports about that "+
				"tree it did not look at. A clean scan and a scan that reached nothing are the "+
				"same answer, which is why this is checked separately", root, abs)
		}
	}

	byFile := map[string][]handRolledReadLoop{}
	for _, u := range found {
		rel, err := filepath.Rel(root, u.File)
		if err != nil {
			rel = u.File
		}
		byFile[filepath.ToSlash(rel)] = append(byFile[filepath.ToSlash(rel)], u)
	}

	for _, file := range sortedKeys(byFile) {
		allowed := map[string]bool{}
		for _, name := range readsItsOwnStream[file] {
			allowed[name] = true
		}
		for _, u := range byFile[file] {
			if allowed[u.Function] {
				continue
			}
			t.Errorf("%s: %s reads from a message reader itself. The tree has ONE read loop and "+
				"this is not it: call messaging.RunConsumer(ctx, reader, pacer, handle) and put "+
				"what this function does with a message in handle, returning true to carry on "+
				"and false only when shutdown caught it part-way through. Writing the loop by "+
				"hand means writing core.ReadPacer's two-call contract by hand, and the half "+
				"nothing checks — Succeeded, the reset — is the one that ends a healthy process "+
				"hours later. If this reader genuinely differs in SHAPE rather than in what it "+
				"does with a message, say so in readsItsOwnStream and say why", u.Pos, u.Function)
		}
	}

	for _, file := range sortedKeys(readsItsOwnStream) {
		present := map[string]bool{}
		for _, u := range byFile[file] {
			present[u.Function] = true
		}
		for _, name := range readsItsOwnStream[file] {
			if present[name] {
				continue
			}
			t.Errorf("readsItsOwnStream exempts %s in %s, but the scan no longer reports it at "+
				"all. The exemption is inert and the paragraph justifying it now describes "+
				"nothing: delete the entry", name, file)
		}
		if len(readsItsOwnStream[file]) == 0 {
			t.Errorf("readsItsOwnStream has an empty entry for %s; delete the line rather than "+
				"leaving a file listed with nothing exempt in it", file)
		}
	}
}

// 🔴 THE NEGATIVE CONTROL. A guard is worth nothing until it has been shown to fail, and
// this one's failure mode is silence — it reports a clean tree by finding nothing, which is
// also what a broken scanner does. So point it at a file that IS a hand-rolled read loop and
// require it to say so.
func TestTheScanActuallyFindsAHandRolledReadLoop(t *testing.T) {
	dir := t.TempDir()
	src := `package fake

import "context"

type reader interface{ ReadMessage(context.Context) (int, error) }

func drainItYourself(ctx context.Context, r reader) {
	for {
		if _, err := r.ReadMessage(ctx); err != nil {
			continue
		}
	}
}
`
	if err := os.WriteFile(filepath.Join(dir, "fake.go"), []byte(src), 0o600); err != nil {
		t.Fatalf("writing the fixture: %v", err)
	}

	found, _, err := handRolledReadLoopsUnder(dir)
	if err != nil {
		t.Fatalf("scanning the fixture: %v", err)
	}
	if len(found) != 1 || found[0].Function != "drainItYourself" {
		t.Fatalf("the scan reported %+v over a file that reads a stream itself; a guard that "+
			"cannot see the thing it forbids passes the repository by accident", found)
	}
}

// The scan must refuse a tree it could not read rather than reporting it clean, for the same
// reason the pacing guard next door must.
func TestTheConsumerLoopScanRefusesATreeItParsedNothingIn(t *testing.T) {
	found, _, err := handRolledReadLoopsUnder(t.TempDir())
	if err == nil {
		t.Fatalf("scanning an empty directory returned %d findings and no error; a scan that read "+
			"no files must say so, because silence is this guard's failure mode", len(found))
	}
}
