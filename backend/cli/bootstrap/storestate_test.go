// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"errors"
	"strings"
	"testing"

	pgx "github.com/jackc/pgx/v5"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// fakeStore answers the one query readInstanceStore asks, and nothing else: a query it
// does not recognise is an error rather than a zero value, so a reader that starts asking
// something different fails here instead of quietly reading an empty answer.
type fakeStore struct {
	owner string
	// noRows makes the store answer "there is no such database".
	noRows bool
	// err makes the store refuse to answer at all.
	err error
	// asked counts the owner lookups.
	asked int
}

func (f *fakeStore) Exec(context.Context, string, ...any) (pgconnCommandTag, error) {
	return nil, errors.New("readInstanceStore must not write")
}

func (f *fakeStore) QueryRow(_ context.Context, sql string, _ ...any) pgx.Row {
	if sql != instanceDatabaseOwnerSQL {
		return fakeRow(func(...any) error { return errors.New("unexpected query: " + sql) })
	}
	f.asked++
	return fakeRow(func(dest ...any) error {
		switch {
		case f.err != nil:
			return f.err
		case f.noRows:
			return pgx.ErrNoRows
		}
		*dest[0].(*string) = f.owner
		return nil
	})
}

// stubInstanceCredentials replaces the read that answers whether this instance's own
// credentials are still in its namespace.
func stubInstanceCredentials(t *testing.T, kept bool, err error) {
	t.Helper()
	orig := readInstanceCredentialSecret
	t.Cleanup(func() { readInstanceCredentialSecret = orig })
	readInstanceCredentialSecret = func(context.Context, string, string) (bool, error) {
		return kept, err
	}
}

// 🔴 THE FOUR ANSWERS, AND THE ONE THAT MUST NOT COLLAPSE INTO ANOTHER. A store that
// will not answer reading as "no database here" is what mints a fresh root key over
// recovered rows, so the error case is asserted to be storeStateUnknown by name rather
// than merely "not nil error".
func TestTheStoreSaysWhichOfFourThingsItHolds(t *testing.T) {
	for _, tc := range []struct {
		name  string
		store *fakeStore
		want  storeState
		owner string
		fails bool
	}{
		{"no database at all", &fakeStore{noRows: true}, storeStateAbsent, "", false},
		{"a database owned by this instance's own login", &fakeStore{owner: "beta"}, storeStateOurs, "", false},
		{"a database owned by something else", &fakeStore{owner: "devicechain"}, storeStateForeign, "devicechain", false},
		{"a store that will not answer", &fakeStore{err: errors.New("connection reset")}, storeStateUnknown, "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := readInstanceStore(context.Background(), tc.store, "beta")
			if tc.fails != (err != nil) {
				t.Fatalf("error = %v, want failure = %t", err, tc.fails)
			}
			if got.State != tc.want {
				t.Errorf("the store read as %v, want %v", got.State, tc.want)
			}
			if got.Owner != tc.owner {
				t.Errorf("the owner read as %q, want %q", got.Owner, tc.owner)
			}
			if tc.store.asked != 1 {
				t.Errorf("the store was asked %d time(s), want once", tc.store.asked)
			}
		})
	}
}

// fakeEvidence stands in for the two cluster reads and counts each SEPARATELY.
//
// 🔑 WHICH READ AN ARM TAKES IS A PROPERTY, NOT AN IMPLEMENTATION DETAIL, and it is the
// property the hand-deleted-namespace case turns on: the ours arm must ask for the
// instance's credentials, because a declaration outlives the namespace that held the root
// key. An arm reading the other one — or reading either when it should read neither —
// fails here.
type fakeEvidence struct {
	held    bool
	heldErr error
	kept    bool
	keptErr error

	heldCalls, keptCalls int
}

func (f *fakeEvidence) evidence() clusterEvidence {
	return clusterEvidence{
		HoldsInstance: func() (bool, string, error) {
			f.heldCalls++
			return f.held, "the instance declarations in this cluster", f.heldErr
		},
		KeepsItsCredentials: func() (bool, error) {
			f.keptCalls++
			return f.kept, f.keptErr
		},
	}
}

// reads asserts exactly how many times each side was asked.
func (f *fakeEvidence) reads(t *testing.T, wantHeld, wantKept int) {
	t.Helper()
	if f.heldCalls != wantHeld {
		t.Errorf("the cluster was asked %d time(s) whether it holds this instance, want %d",
			f.heldCalls, wantHeld)
	}
	if f.keptCalls != wantKept {
		t.Errorf("the instance's credentials were looked for %d time(s), want %d",
			f.keptCalls, wantKept)
	}
}

func restoringState() *State {
	return &State{Instance: "beta", Provider: "local", Escrow: EscrowPlan{
		RestoredRootKey: "a2V5", RestoredFrom: "/tmp/beta.escrow",
	}}
}

func mintingState() *State { return &State{Instance: "beta", Provider: "local"} }

// 🔴 THE HAZARD ITSELF. A relational store restored from an archive holds this
// instance's database; a bootstrap that mints a fresh root key over it comes up green
// and leaves every recovered secret sealed shut. Nothing in the cluster holds this
// instance, so it cannot be a bootstrap finishing its own work.
func TestAStoreThatCameBackFromAnArchiveRefusesAFreshlyMintedKey(t *testing.T) {
	ev := &fakeEvidence{kept: false}
	err := refuseAStoreAndKeyThatDoNotMatch(mintingState(), instanceStore{State: storeStateOurs}, ev.evidence())

	var typed *ErrStoreAndKeyDisagree
	if !errors.As(err, &typed) {
		t.Fatalf("a bootstrap minting a key over a recovered store was not refused with the typed "+
			"refusal, so the command layer would keep a local record for an instance that was never "+
			"built: %v", err)
	}
	ev.reads(t, 0, 1)
	// The operator has to be able to act on it: the route back, and the one case where
	// there is no route back at all.
	for _, want := range []string{"--restore-root-key", "--no-escrow", "dcctl bootstrap local beta"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not mention %q:\n%s", want, err)
		}
	}
}

// The same store, met by a run that carries the key those rows are sealed by. This is
// the documented recovery route, and it must not be refused — nor should it cost the
// cluster-wide read, which only the refusal above has any use for.
func TestTheRecoveryRouteIsAllowedWithoutAskingTheCluster(t *testing.T) {
	ev := &fakeEvidence{}
	if err := refuseAStoreAndKeyThatDoNotMatch(restoringState(), instanceStore{State: storeStateOurs}, ev.evidence()); err != nil {
		t.Fatalf("the documented recovery route was refused: %v", err)
	}
	ev.reads(t, 0, 0)
}

// 🔴 THE WINDOW stepRefuseRebuild DELIBERATELY LEAVES OPEN. A bootstrap that died
// between provisioning its database and writing its configuration document is re-run to
// repair it, and the database it left behind is indistinguishable from a restored one by
// looking at the store. What separates them is that its own earlier run had already
// written this instance into the cluster two steps before the database existed.
func TestABootstrapFinishingItsOwnHalfBuiltInstanceIsNotRefused(t *testing.T) {
	ev := &fakeEvidence{kept: true}
	if err := refuseAStoreAndKeyThatDoNotMatch(mintingState(), instanceStore{State: storeStateOurs}, ev.evidence()); err != nil {
		t.Fatalf("a bootstrap re-run over its own half-built instance was refused, which makes that "+
			"instance unrepairable: %v", err)
	}
	ev.reads(t, 0, 1)
}

// 🔴 A CLUSTER THAT WILL NOT SAY WHETHER IT HOLDS THE INSTANCE IS NOT A CLUSTER THAT
// DOES NOT. And the failure is NOT typed: the typed refusals clear the local record, and
// clearing it for a run whose check never completed is the orphan the record prevents.
func TestAClusterThatWillNotAnswerStopsTheRunWithoutBeingARefusal(t *testing.T) {
	ev := &fakeEvidence{keptErr: errors.New("the API server is unreachable")}
	err := refuseAStoreAndKeyThatDoNotMatch(mintingState(), instanceStore{State: storeStateOurs}, ev.evidence())
	if err == nil {
		t.Fatal("a cluster that could not be read was treated as one holding nothing, which mints a " +
			"fresh root key over recovered rows")
	}
	var typed *ErrStoreAndKeyDisagree
	if errors.As(err, &typed) {
		t.Errorf("a failed read was typed as a refusal, so the command layer would clear the local "+
			"record of a run that may have been about to succeed: %v", err)
	}
}

// 🔴 AND THE SAME RULE ONE LAYER DOWN. storeStateUnknown is the zero value on purpose;
// deciding from it must fail rather than fall through to "nothing there".
func TestAStoreThatWasNotReadIsNotReadAsEmpty(t *testing.T) {
	ev := &fakeEvidence{}
	err := refuseAStoreAndKeyThatDoNotMatch(mintingState(), instanceStore{}, ev.evidence())
	if err == nil {
		t.Fatal("an unread store was treated as an absent one, which is the one direction that " +
			"cannot be undone")
	}
	var typed *ErrStoreAndKeyDisagree
	if errors.As(err, &typed) {
		t.Errorf("a store that was never read was typed as a refusal: %v", err)
	}
	ev.reads(t, 0, 0)
}

// --restore-root-key against a store with nothing in it, on a cluster holding no trace of
// the instance. Not destructive, and refused anyway: it produces an empty instance under
// a recovered key while the operator believes they recovered, and the likeliest cause is
// a relational restore that silently did not run.
func TestARecoveredKeyIsRefusedWhenThereIsNothingToOpen(t *testing.T) {
	ev := &fakeEvidence{held: false}
	err := refuseAStoreAndKeyThatDoNotMatch(restoringState(), instanceStore{State: storeStateAbsent}, ev.evidence())

	var typed *ErrStoreAndKeyDisagree
	if !errors.As(err, &typed) {
		t.Fatalf("a recovered key over an empty store was not refused: %v", err)
	}
	if !strings.Contains(err.Error(), "--restore-rdb-from") {
		t.Errorf("the refusal does not name the step that was missed:\n%s", err)
	}
	// It quotes what it READ rather than only asserting the conclusion.
	if !strings.Contains(err.Error(), "the instance declarations in this cluster") {
		t.Errorf("the refusal does not say what it read to conclude nothing is here:\n%s", err)
	}
	ev.reads(t, 1, 0)
}

// 🔴 THE OTHER READER OF --restore-root-key, AND REFUSING IT WOULD HAVE BEEN A
// REGRESSION. WriteEscrow will not overwrite an existing artifact, so a bootstrap that
// died after escrowing its key is re-run by pointing this same flag at the artifact on
// disk — describeBlockingArtifact says so in as many words. The store may hold nothing
// yet, because the run can die between the render step that writes the artifact and the
// apply that creates the database. What makes it a retry rather than a recovery is that
// its own earlier run had already written this instance into the cluster.
func TestARetryAfterAFailedBootstrapMayReuseItsOwnEscrowedKey(t *testing.T) {
	ev := &fakeEvidence{held: true}
	if err := refuseAStoreAndKeyThatDoNotMatch(restoringState(), instanceStore{State: storeStateAbsent}, ev.evidence()); err != nil {
		t.Fatalf("a bootstrap re-run with the key its own failed run escrowed was refused, which "+
			"leaves that instance unrepairable — the artifact cannot be overwritten either: %v", err)
	}
	ev.reads(t, 1, 0)
}

// And a cluster that will not say, on that same branch: a failed read is not "nothing
// half-built here", and it is not typed, so the local record survives it.
func TestARetryIsNotAssumedWhenTheClusterWillNotAnswer(t *testing.T) {
	ev := &fakeEvidence{heldErr: errors.New("the API server is unreachable")}
	err := refuseAStoreAndKeyThatDoNotMatch(restoringState(), instanceStore{State: storeStateAbsent}, ev.evidence())
	if err == nil {
		t.Fatal("a cluster that could not be read was treated as holding nothing")
	}
	var typed *ErrStoreAndKeyDisagree
	if errors.As(err, &typed) {
		t.Errorf("a failed read was typed as a refusal, clearing the local record of a run that "+
			"may have been about to succeed: %v", err)
	}
}

// The ordinary first bootstrap of an instance: nothing in the store, no recovery asked
// for, and no cluster-wide sweep to establish it.
func TestAFreshInstanceIsNotRefusedAndCostsNoClusterRead(t *testing.T) {
	ev := &fakeEvidence{}
	if err := refuseAStoreAndKeyThatDoNotMatch(mintingState(), instanceStore{State: storeStateAbsent}, ev.evidence()); err != nil {
		t.Fatalf("an ordinary first bootstrap was refused: %v", err)
	}
	ev.reads(t, 0, 0)
}

// 🔴 THE PRE-ISOLATION SHAPE, REFUSED BEFORE THE ROOT KEY IS WRITTEN RATHER THAN AFTER.
// ensureInstanceDatabase has always refused this, from inside the infrastructure apply —
// one line after writeMintedSecrets. Same sentence, four steps earlier, and now typed so
// the record goes back too.
func TestADatabaseOwnedBySomethingElseIsRefusedBeforeAnythingIsWritten(t *testing.T) {
	for _, st := range []*State{mintingState(), restoringState()} {
		ev := &fakeEvidence{held: true, kept: true}
		err := refuseAStoreAndKeyThatDoNotMatch(st, instanceStore{State: storeStateForeign, Owner: "devicechain"}, ev.evidence())

		var typed *ErrStoreAndKeyDisagree
		if !errors.As(err, &typed) {
			t.Fatalf("a database owned by %q was not refused early: %v", "devicechain", err)
		}
		if !errors.Is(err, errInstanceDatabaseNotOurs) {
			t.Errorf("the early refusal lost the cause the late one carries: %v", err)
		}
		if !strings.Contains(err.Error(), `owned by "devicechain"`) {
			t.Errorf("the refusal does not name the owner it read:\n%s", err)
		}
		ev.reads(t, 0, 0)
	}
}

// 🔴 THE COUNTERWEIGHT TO EVERY REFUSAL ABOVE. --no-escrow is never given a clause of
// its own in the policy: what removes it is that EscrowFlags.Validate already refuses it
// alongside --restore-root-key, so requiring the restore against a store that is already
// ours makes the pair unreachable. If that validation is ever relaxed, this fails and the
// policy needs the clause it does not have today.
func TestNoEscrowCannotAccompanyTheOnlyFlagThatOpensARecoveredStore(t *testing.T) {
	err := EscrowFlags{NoEscrow: true, RestoreFile: "/tmp/beta.escrow"}.Validate()
	if err == nil {
		t.Fatal("--no-escrow may now be passed with --restore-root-key, so a recovery can ask for " +
			"the key and refuse to keep it — refuseAStoreAndKeyThatDoNotMatch has no clause for that")
	}
}

// 🔴 THE REFUSAL HAS TO COME OUT OF STEP 4, not merely exist as a function. The command
// layer clears this run's local record on the TYPE, and that is only safe because the
// type is raised from the one step TestTheSingletonStepRunsBeforeAnythingIsWritten holds
// ahead of the operator install and the declaration.
func TestTheSingletonStepRefusesAMintedKeyOverARecoveredStore(t *testing.T) {
	stubSingletons(t, clusterSingletons{}, nil)
	stubNamespacePrecheck(t)
	calls := stubSharedStore(t, instanceStore{State: storeStateOurs}, nil)
	stubClusterInstances(t, clusterInstances{}, nil)

	st := &State{Instance: "beta", IngressHost: "beta.localhost", Install: installed(), Values: map[string]string{}}
	err := stepCheckClusterSingletons(context.Background(), st)

	var typed *ErrStoreAndKeyDisagree
	if !errors.As(err, &typed) {
		t.Fatalf("step 4 did not refuse a bootstrap that would mint a fresh root key over a "+
			"recovered relational store: %v", err)
	}
	if *calls != 1 {
		t.Errorf("the store was read %d time(s), want once", *calls)
	}

	// The same step, the same store, with the key those rows are sealed by: allowed.
	stubSharedStore(t, instanceStore{State: storeStateOurs}, nil)
	st = &State{Instance: "beta", IngressHost: "beta.localhost", Install: installed(),
		Escrow: EscrowPlan{RestoredRootKey: "a2V5", RestoredFrom: "/tmp/beta.escrow"},
		Values: map[string]string{}}
	if err := stepCheckClusterSingletons(context.Background(), st); err != nil {
		t.Fatalf("step 4 refused the documented recovery route: %v", err)
	}
}

// A State with no install record has no store to reach, so step 4 asks nothing and
// decides nothing — rather than deciding from the storeStateUnknown it would get back.
func TestTheSingletonStepDoesNotJudgeAStoreItNeverRead(t *testing.T) {
	stubSingletons(t, clusterSingletons{}, nil)
	stubNamespacePrecheck(t)
	calls := stubSharedStore(t, instanceStore{}, nil)
	swept := withClusterInstances(t, nil, errors.New("nothing should ask this"))

	st := &State{Instance: "beta", IngressHost: "beta.localhost", Values: map[string]string{}}
	if err := stepCheckClusterSingletons(context.Background(), st); err != nil {
		t.Fatalf("a state with no install record was judged against a store nobody read: %v", err)
	}
	if *calls != 0 || *swept != 0 {
		t.Errorf("a state with no install record still read the store (%d) or swept the cluster (%d)",
			*calls, *swept)
	}
}

// 🔴 THE WIRING, NOT THE POLICY. refuseAStoreAndKeyThatDoNotMatch is pinned above
// against a thunk built by hand to fail; this drives the thunk the STEP builds, which is
// a different piece of code and the one that ships. Swallow the error there and an
// unreachable API server becomes "this cluster holds nothing" — which routes a
// legitimately half-built instance straight into the archive refusal, and, because that
// refusal is typed, takes its local record with it.
func TestAnUnreadableClusterDoesNotBecomeAnArchiveRefusal(t *testing.T) {
	stubSingletons(t, clusterSingletons{}, nil)
	stubNamespacePrecheck(t)
	stubSharedStore(t, instanceStore{State: storeStateOurs}, nil)
	stubInstanceCredentials(t, false, errors.New("the API server is unreachable"))

	err := stepCheckClusterSingletons(context.Background(), &State{
		Instance: "beta", IngressHost: "beta.localhost", Install: installed(), Values: map[string]string{}})
	if err == nil {
		t.Fatal("a cluster that could not be read was treated as one holding nothing, so a bootstrap " +
			"repairing its own half-built instance is refused whenever the API server hiccups")
	}
	var typed *ErrStoreAndKeyDisagree
	if errors.As(err, &typed) {
		t.Errorf("a failed cluster read reached the operator as the archive refusal, which clears "+
			"the local record of a run that may have been about to succeed: %v", err)
	}
}

// 🔴 AND THE ORDER, WHICH IS THE OTHER HALF OF WHAT THE HOST CHECK ALREADY PINS. The
// store is the expensive one to reach — a port-forward into the database primary and a
// sign-in — so a run that is going to be refused for its namespace must find that out
// first. TestTheSingletonStepRefusesAnInstanceTheStoreHasNoBudgetFor holds the host ahead
// of it; this holds the namespace.
func TestANamespaceRefusalNeverReachesTheStore(t *testing.T) {
	stubSingletons(t, clusterSingletons{}, nil)
	// A namespace bearing this instance's name that carries no label saying it is this
	// instance's — the shape refuseANamespaceThisInstanceDoesNotOwn stops.
	stubNamespacePrecheck(t, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: InstanceNamespace("beta")}})
	calls := stubSharedStore(t, instanceStore{State: storeStateAbsent}, nil)

	err := stepCheckClusterSingletons(context.Background(), &State{
		Instance: "beta", IngressHost: "beta.localhost", Install: installed(), Values: map[string]string{}})
	var refusal *ErrNamespaceUnavailable
	if !errors.As(err, &refusal) {
		t.Fatalf("step 4 did not refuse a namespace that is not this instance's: %v", err)
	}
	if *calls != 0 {
		t.Errorf("a run refused for its namespace still signed in to the relational store %d time(s)", *calls)
	}
}

// 🔴 THE CASE THE DECLARATION COULD NOT SEE, AND THE REASON THE OURS ARM DOES NOT ASK IT.
// The Instance CRD is cluster-scoped, so `kubectl delete ns dci-<instance>` takes the root
// key and the configuration document and LEAVES the declaration — while the database,
// which lives in the shared store rather than that namespace, survives. Keyed on the
// declaration this reads as a repairable half-built run and mints over rows whose key the
// operator just deleted; keyed on the credentials that travel WITH the database, it does
// not.
func TestADatabaseThatOutlivedItsNamespaceIsNotMistakenForARepair(t *testing.T) {
	ev := &fakeEvidence{held: true, kept: false}
	err := refuseAStoreAndKeyThatDoNotMatch(mintingState(), instanceStore{State: storeStateOurs}, ev.evidence())

	var typed *ErrStoreAndKeyDisagree
	if !errors.As(err, &typed) {
		t.Fatalf("a database whose namespace was deleted out from under it was read as this run's own "+
			"half-built work, so the bootstrap would mint a fresh root key over rows sealed by the key "+
			"that namespace held: %v", err)
	}
	// It must not have reached the declaration at all — that is the read that gets this
	// case wrong, and consulting it even as a tiebreak would reintroduce the hole.
	ev.reads(t, 0, 1)
	if !strings.Contains(err.Error(), InstanceNamespace("beta")) {
		t.Errorf("the refusal does not name the namespace it looked in:\n%s", err)
	}
}

// The step, not the policy: the ours arm's evidence has to be the credentials read when
// it runs for real. Stubbing the declarations to name the instance — the shape a deleted
// namespace leaves — must NOT rescue a bootstrap whose credentials are gone.
func TestTheSingletonStepAsksForCredentialsNotTheDeclaration(t *testing.T) {
	stubSingletons(t, clusterSingletons{}, nil)
	stubNamespacePrecheck(t)
	stubSharedStore(t, instanceStore{State: storeStateOurs}, nil)
	stubClusterInstances(t, clusterInstances{IDs: []string{"beta"}, Source: "the instance declarations in this cluster"}, nil)
	stubInstanceCredentials(t, false, nil)

	err := stepCheckClusterSingletons(context.Background(), &State{
		Instance: "beta", IngressHost: "beta.localhost", Install: installed(), Values: map[string]string{}})
	var typed *ErrStoreAndKeyDisagree
	if !errors.As(err, &typed) {
		t.Fatalf("step 4 let a bootstrap through onto a database whose namespace is gone, because a "+
			"surviving declaration said the cluster still holds the instance: %v", err)
	}

	// And the counterweight: the same step, same declarations, credentials still there —
	// a genuine half-built run, which must go through.
	stubInstanceCredentials(t, true, nil)
	if err := stepCheckClusterSingletons(context.Background(), &State{
		Instance: "beta", IngressHost: "beta.localhost", Install: installed(), Values: map[string]string{}}); err != nil {
		t.Fatalf("step 4 refused a bootstrap repairing its own half-built instance: %v", err)
	}
}
