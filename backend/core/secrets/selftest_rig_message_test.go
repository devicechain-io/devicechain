// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package secrets

import (
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// rigScript is the DR rig, reached from this module. The test does not search for
// it: a search that comes up empty is indistinguishable from a rig that no longer
// asserts anything, and this test's whole job is to notice a broken coupling.
const rigScript = "../../../hack/dr-rig.sh"

// rigRefusalAssignment matches the one line in the rig that names the sentence its
// negative control greps for. Anchored to the start of a line so a mention of the
// variable inside a comment or a message cannot satisfy it.
var rigRefusalAssignment = regexp.MustCompile(`(?m)^root_key_refusal="([^"]+)"$`)

// TestRigGrepsForTheRefusalItAsserts pins a coupling that spans a language, a
// process and a cluster boundary, and that nothing else can see.
//
// hack/dr-rig.sh's negative control recovers an instance under a deliberately wrong
// root key and asserts that notification-management REFUSED TO START, by grepping
// that pod's log for a sentence out of the refusal SelfTest produces below. There is
// no way for the rig to import that sentence — it is emitted by a service running
// inside the cluster, not by a tool the rig executes — so it is a literal copy.
//
// Reword the Go message and nothing breaks: this suite stays green, every build is
// clean, and the rig simply stops finding its sentence. Its control then reports
// "the refusal never appeared", which reads as a broken cluster or a slow bring-up
// rather than as a stale string — the precise wrong-reason failure the control was
// rewritten to eliminate. And the rig is manual, so nothing would surface it until
// somebody ran a drill and disbelieved the result.
//
// 🔴 This test reads a file OUTSIDE this module. Go's test cache does not track such
// files, so a cached PASS survives an edit to the rig; -count=1 is what makes it
// honest, which CI passes and the documented full sweep passes.
func TestRigGrepsForTheRefusalItAsserts(t *testing.T) {
	raw, err := os.ReadFile(rigScript)
	if err != nil {
		abs, _ := filepath.Abs(rigScript)
		t.Fatalf("could not read the DR rig at %s: %v\n"+
			"This test exists to keep that script's grep string and this package's refusal in "+
			"step. If the rig moved, move this test with it rather than deleting it — a "+
			"silently absent check is what it is here to prevent.", abs, err)
	}

	m := rigRefusalAssignment.FindSubmatch(raw)
	if m == nil {
		t.Fatalf("%s no longer assigns root_key_refusal=\"...\" on a line of its own.\n"+
			"Either the rig stopped asserting the startup refusal — in which case its negative "+
			"control has lost a leg — or the assignment was reshaped and this test can no "+
			"longer find it.", rigScript)
	}
	needle := string(m[1])

	// 🔴 The test's own negative control. strings.Contains reports true for the empty
	// string, so a regex that matched an empty or near-empty capture would make every
	// assertion below pass without comparing anything. The rig's sentence is a clause,
	// and a short one would not be distinctive in a pod log either.
	if len(needle) < 20 {
		t.Fatalf("the rig's root_key_refusal is %q (%d bytes), which is too short to be "+
			"evidence in a pod log and too weak to make this test's comparison mean anything",
			needle, len(needle))
	}

	// The refusal is produced by running the real path, not by quoting the format
	// string: a test that compared the rig's needle against a constant declared here
	// would agree with itself while SelfTest emitted something else entirely.
	db := newStoreDB(t)
	sealing := newTestKP(t)
	if err := NewStore(db, sealing).Put(t.Context(), instanceRef("connector/1/auth"), []byte("stored")); err != nil {
		t.Fatalf("put: %v", err)
	}
	if n := countSecrets(t, db); n != 1 {
		t.Fatalf("premise broken: the store must hold exactly one row, holds %d", n)
	}

	key, err := differentRootKey()
	if err != nil {
		t.Fatalf("generate a second key: %v", err)
	}
	wrong, err := NewInstanceKeyProvider(key)
	if err != nil {
		t.Fatalf("premise broken: the second key must be well-formed: %v", err)
	}

	result, err := SelfTest(t.Context(), db, wrong)
	if err == nil {
		t.Fatalf("premise broken: a wrong-but-well-formed key must be refused, got result %q", result)
	}
	if got := err.Error(); !strings.Contains(got, needle) {
		t.Fatalf("the refusal no longer contains the sentence %s greps for.\n"+
			"  rig greps for: %q\n"+
			"  refusal says:  %q\n"+
			"Update BOTH, or the rig's negative control fails on a missing string and reports "+
			"it as a cluster problem.", rigScript, needle, got)
	}

	// The refusal travels to the rig as a WRAPPED error through the service's
	// startup path and out of core's `log.Error().Err(err)`, so the sentence has to
	// survive wrapping too. Anything that moved it into a struct field the
	// formatting drops would break the rig while the check above still passed.
	wrapped := errors.New("Unable to initialize microservice: " + err.Error())
	if !strings.Contains(wrapped.Error(), needle) {
		t.Fatalf("the sentence does not survive wrapping, which is how it reaches the pod log")
	}
}
