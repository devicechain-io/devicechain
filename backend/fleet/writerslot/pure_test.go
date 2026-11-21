// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package writerslot

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestAllTerminatedIsOnlyEverContainerStatuses(t *testing.T) {
	const pod = "pod-1"
	done := func(n ...bool) PodObservation {
		var cs []ContainerStatus
		for _, d := range n {
			cs = append(cs, ContainerStatus{Name: "c", Terminated: d})
		}
		return PodObservation{PodUID: pod, Containers: cs}
	}
	if !done(true).AllTerminated(pod) || !done(true, true).AllTerminated(pod) {
		t.Fatal("a pod whose every container terminated was not accepted")
	}
	// Every non-proof the spec names is refused: nothing observed, a container still running,
	// another pod, an observation of nothing.
	for name, obs := range map[string]PodObservation{
		"one container still running": done(true, false),
		"no containers at all":        {PodUID: pod},
		"a different pod":             {PodUID: "pod-2", Containers: []ContainerStatus{{Terminated: true}}},
		"an empty observation":        {},
	} {
		if obs.AllTerminated(pod) {
			t.Errorf("%s was accepted as termination", name)
		}
	}
	// Running and NodeReady describe liveness; they are never termination evidence.
	if (PodObservation{PodUID: pod, Running: true, NodeReady: true}).AllTerminated(pod) {
		t.Error("liveness fields were read as termination")
	}
}

func TestCheckClusterIdentityRefusesEachElementWithItsOwnReason(t *testing.T) {
	first := time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC)
	intent := Intent{Project: "p", Location: "l", CloudName: "dcf-abc", Nonce: "nonce", FirstSubmitAt: first}
	good := ObservedCluster{Project: "p", Location: "l", Name: "dcf-abc", NonceLabel: "nonce", CreateTime: first.Add(time.Minute), ResourceID: "id-1"}
	if r := CheckClusterIdentity(intent, good); r != ReasonVerified {
		t.Fatalf("the all-match case was refused: %s", r)
	}
	mut := func(f func(*ObservedCluster)) string { g := good; f(&g); return CheckClusterIdentity(intent, g) }
	cases := map[string]struct {
		got  string
		want string
	}{
		"name":                           {mut(func(o *ObservedCluster) { o.Name = "dcf-other" }), ReasonNameMismatch},
		"project":                        {mut(func(o *ObservedCluster) { o.Project = "q" }), ReasonProjectMismatch},
		"location":                       {mut(func(o *ObservedCluster) { o.Location = "m" }), ReasonLocationMismatch},
		"nonce":                          {mut(func(o *ObservedCluster) { o.NonceLabel = "wrong" }), ReasonNonceMismatch},
		"no label":                       {mut(func(o *ObservedCluster) { o.NonceLabel = "" }), ReasonNonceMismatch},
		"created long before the intent": {mut(func(o *ObservedCluster) { o.CreateTime = first.Add(-10 * time.Minute) }), ReasonCreateBeforeIntent},
	}
	for name, c := range cases {
		if c.got != c.want {
			t.Errorf("%s: got %s want %s", name, c.got, c.want)
		}
	}
	// The skew boundary: exactly the margin before the intent is accepted, one nanosecond more is not.
	if r := mut(func(o *ObservedCluster) { o.CreateTime = first.Add(-defaultImportSkew) }); r != ReasonVerified {
		t.Errorf("creation exactly at the skew margin was refused: %s", r)
	}
	if r := mut(func(o *ObservedCluster) { o.CreateTime = first.Add(-defaultImportSkew - time.Nanosecond) }); r != ReasonCreateBeforeIntent {
		t.Errorf("creation just past the skew margin was accepted: %s", r)
	}
	// Once the API id is known, a different id under the same name is IDENTITY_MISMATCH.
	known := intent
	known.CloudResourceID = "id-1"
	if r := CheckClusterIdentity(known, good); r != ReasonVerified {
		t.Errorf("a matching id was refused: %s", r)
	}
	other := good
	other.ResourceID = "id-2"
	if r := CheckClusterIdentity(known, other); r != ReasonIdentityMismatch {
		t.Errorf("a different id under the same name: %s", r)
	}
	// No create time recorded for the intent is not a pass.
	noIntent := intent
	noIntent.FirstSubmitAt = time.Time{}
	if r := CheckClusterIdentity(noIntent, good); r != ReasonCreateBeforeIntent {
		t.Errorf("an intent with no first_submit_at was accepted: %s", r)
	}
	// And an intent with no nonce never matches, not even an empty label.
	noNonce := intent
	noNonce.Nonce = ""
	empty := good
	empty.NonceLabel = ""
	if r := CheckClusterIdentity(noNonce, empty); r != ReasonNonceMismatch {
		t.Errorf("an empty nonce matched an empty label: %s", r)
	}
}

func TestCheckPoolImport(t *testing.T) {
	declared := []string{"database", "services"}
	if r := CheckPoolImport("id-1", "id-1", "database", declared); r != ReasonVerified {
		t.Errorf("a declared pool of the verified cluster was refused: %s", r)
	}
	if r := CheckPoolImport("id-1", "id-2", "database", declared); r != ReasonPoolParentNotVerified {
		t.Errorf("a pool of another cluster: %s", r)
	}
	if r := CheckPoolImport("", "", "database", declared); r != ReasonPoolParentNotVerified {
		t.Errorf("no verified cluster: %s", r)
	}
	if r := CheckPoolImport("id-1", "id-1", "stranger", declared); r != ReasonPoolNotDeclared {
		t.Errorf("an undeclared pool: %s", r)
	}
}

func TestStateRoleNameIsDerivedFromTheRunUID(t *testing.T) {
	n, err := StateRoleName("33333333-3333-3333-3333-333333333333")
	if err != nil || n != "fleet_run_33333333333333333333333333333333" {
		t.Fatalf("%q %v", n, err)
	}
	for _, bad := range []string{"", "x", "33333333-3333-3333-3333-33333333333;", `"; DROP ROLE x; --`} {
		if _, err := StateRoleName(bad); err == nil {
			t.Errorf("%q was accepted", bad)
		}
	}
}

func TestDestroyAndImportExecutionFailLoudly(t *testing.T) {
	m := &Manager{}
	// DESTROY is refused before anything else is looked at, so no database is needed to see it.
	if _, err := m.Acquire(context.Background(), RunSpec{Kind: KindDestroy}); !errors.Is(err, ErrDestroyNotImplemented) {
		t.Fatalf("Acquire(DESTROY): %v", err)
	}
	if err := m.ExecuteImport(context.Background(), "a", "b"); !errors.Is(err, ErrImportNotImplemented) {
		t.Fatalf("ExecuteImport: %v", err)
	}
	var none Premises = NoPremises{}
	for _, mode := range []string{ModeKindJWKS, ModeGKEWI, "anything"} {
		if none.ExpiryHolds(mode) {
			t.Errorf("the default premises accepted EXPIRY in %s", mode)
		}
	}
}

// The data-class label is informational and is never authority for anything: no code of this
// package reads it. The check is on the source, because a label read cannot be seen in behaviour
// until someone relies on it.
func TestNoCodePathReadsALabelToAuthorize(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	var scanned int
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		scanned++
		for _, banned := range []string{"dc-fleet-data-class", "data_class", "DataClass"} {
			if strings.Contains(string(b), banned) {
				t.Errorf("%s mentions %q: the data-class label and column authorize nothing", f, banned)
			}
		}
	}
	if scanned == 0 {
		t.Fatal("scanned no source files")
	}
}

func TestVocabularyHelpers(t *testing.T) {
	if !in("HELD", allStates...) || in("FUTURE", allStates...) {
		t.Fatal("in() or allStates is wrong")
	}
	for _, s := range []string{StateFree, StateHeld, StateUncertain, StateExcluded, StateRecovering, StateQuarantined} {
		if !in(s, allStates...) {
			t.Errorf("%s missing from allStates", s)
		}
	}
	if lifecyclePermits(LifecycleDestroyed, KindCreate) || lifecyclePermits("FUTURE", KindCreate) {
		t.Error("a destroyed or unknown lifecycle permitted a kind")
	}
	if !lifecyclePermits(LifecycleTearingDown, KindRecover) || lifecyclePermits(LifecycleTearingDown, KindChange) {
		t.Error("TEARING_DOWN must permit only RECOVER (and the refused DESTROY)")
	}
}
