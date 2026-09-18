// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"errors"
	"strings"
	"testing"

	pgx "github.com/jackc/pgx/v5"
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

// storeAnswering builds the thunk step 4 hands the policy, and reports whether it was
// called. Whether it is called is a PROPERTY, not an implementation detail: the read
// behind it walks every declaration, minted Secret and Helm release in the cluster, and
// only one of the four states has any use for the answer.
func storeAnswering(held bool, err error) (func() (bool, error), *int) {
	calls := 0
	return func() (bool, error) {
		calls++
		return held, err
	}, &calls
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
	answer, calls := storeAnswering(false, nil)
	err := refuseAStoreAndKeyThatDoNotMatch(mintingState(), instanceStore{State: storeStateOurs}, answer)

	var typed *ErrStoreAndKeyDisagree
	if !errors.As(err, &typed) {
		t.Fatalf("a bootstrap minting a key over a recovered store was not refused with the typed "+
			"refusal, so the command layer would keep a local record for an instance that was never "+
			"built: %v", err)
	}
	if *calls != 1 {
		t.Errorf("the cluster was asked %d time(s) whether it holds this instance, want once", *calls)
	}
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
	answer, calls := storeAnswering(false, nil)
	if err := refuseAStoreAndKeyThatDoNotMatch(restoringState(), instanceStore{State: storeStateOurs}, answer); err != nil {
		t.Fatalf("the documented recovery route was refused: %v", err)
	}
	if *calls != 0 {
		t.Errorf("a run already carrying the key swept the cluster %d time(s) to learn something "+
			"it had no use for", *calls)
	}
}

// 🔴 THE WINDOW stepRefuseRebuild DELIBERATELY LEAVES OPEN. A bootstrap that died
// between provisioning its database and writing its configuration document is re-run to
// repair it, and the database it left behind is indistinguishable from a restored one by
// looking at the store. What separates them is that its own earlier run had already
// written this instance into the cluster two steps before the database existed.
func TestABootstrapFinishingItsOwnHalfBuiltInstanceIsNotRefused(t *testing.T) {
	answer, calls := storeAnswering(true, nil)
	if err := refuseAStoreAndKeyThatDoNotMatch(mintingState(), instanceStore{State: storeStateOurs}, answer); err != nil {
		t.Fatalf("a bootstrap re-run over its own half-built instance was refused, which makes that "+
			"instance unrepairable: %v", err)
	}
	if *calls != 1 {
		t.Errorf("the cluster was asked %d time(s), want once", *calls)
	}
}

// 🔴 A CLUSTER THAT WILL NOT SAY WHETHER IT HOLDS THE INSTANCE IS NOT A CLUSTER THAT
// DOES NOT. And the failure is NOT typed: the typed refusals clear the local record, and
// clearing it for a run whose check never completed is the orphan the record prevents.
func TestAClusterThatWillNotAnswerStopsTheRunWithoutBeingARefusal(t *testing.T) {
	answer, _ := storeAnswering(false, errors.New("the API server is unreachable"))
	err := refuseAStoreAndKeyThatDoNotMatch(mintingState(), instanceStore{State: storeStateOurs}, answer)
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
	answer, calls := storeAnswering(false, nil)
	err := refuseAStoreAndKeyThatDoNotMatch(mintingState(), instanceStore{}, answer)
	if err == nil {
		t.Fatal("an unread store was treated as an absent one, which is the one direction that " +
			"cannot be undone")
	}
	var typed *ErrStoreAndKeyDisagree
	if errors.As(err, &typed) {
		t.Errorf("a store that was never read was typed as a refusal: %v", err)
	}
	if *calls != 0 {
		t.Errorf("a run that could not read the store still swept the cluster %d time(s)", *calls)
	}
}

// --restore-root-key against a store with nothing in it, on a cluster holding no trace of
// the instance. Not destructive, and refused anyway: it produces an empty instance under
// a recovered key while the operator believes they recovered, and the likeliest cause is
// a relational restore that silently did not run.
func TestARecoveredKeyIsRefusedWhenThereIsNothingToOpen(t *testing.T) {
	answer, calls := storeAnswering(false, nil)
	err := refuseAStoreAndKeyThatDoNotMatch(restoringState(), instanceStore{State: storeStateAbsent}, answer)

	var typed *ErrStoreAndKeyDisagree
	if !errors.As(err, &typed) {
		t.Fatalf("a recovered key over an empty store was not refused: %v", err)
	}
	if !strings.Contains(err.Error(), "--restore-rdb-from") {
		t.Errorf("the refusal does not name the step that was missed:\n%s", err)
	}
	if *calls != 1 {
		t.Errorf("the cluster was asked %d time(s) whether it holds a half-built instance of this "+
			"name, want once", *calls)
	}
}

// 🔴 THE OTHER READER OF --restore-root-key, AND REFUSING IT WOULD HAVE BEEN A
// REGRESSION. WriteEscrow will not overwrite an existing artifact, so a bootstrap that
// died after escrowing its key is re-run by pointing this same flag at the artifact on
// disk — describeBlockingArtifact says so in as many words. The store may hold nothing
// yet, because the run can die between the render step that writes the artifact and the
// apply that creates the database. What makes it a retry rather than a recovery is that
// its own earlier run had already written this instance into the cluster.
func TestARetryAfterAFailedBootstrapMayReuseItsOwnEscrowedKey(t *testing.T) {
	answer, calls := storeAnswering(true, nil)
	if err := refuseAStoreAndKeyThatDoNotMatch(restoringState(), instanceStore{State: storeStateAbsent}, answer); err != nil {
		t.Fatalf("a bootstrap re-run with the key its own failed run escrowed was refused, which "+
			"leaves that instance unrepairable — the artifact cannot be overwritten either: %v", err)
	}
	if *calls != 1 {
		t.Errorf("the cluster was asked %d time(s), want once", *calls)
	}
}

// And a cluster that will not say, on that same branch: a failed read is not "nothing
// half-built here", and it is not typed, so the local record survives it.
func TestARetryIsNotAssumedWhenTheClusterWillNotAnswer(t *testing.T) {
	answer, _ := storeAnswering(false, errors.New("the API server is unreachable"))
	err := refuseAStoreAndKeyThatDoNotMatch(restoringState(), instanceStore{State: storeStateAbsent}, answer)
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
	answer, calls := storeAnswering(false, nil)
	if err := refuseAStoreAndKeyThatDoNotMatch(mintingState(), instanceStore{State: storeStateAbsent}, answer); err != nil {
		t.Fatalf("an ordinary first bootstrap was refused: %v", err)
	}
	if *calls != 0 {
		t.Errorf("an ordinary bootstrap swept the cluster %d time(s)", *calls)
	}
}

// 🔴 THE PRE-ISOLATION SHAPE, REFUSED BEFORE THE ROOT KEY IS WRITTEN RATHER THAN AFTER.
// ensureInstanceDatabase has always refused this, from inside the infrastructure apply —
// one line after writeMintedSecrets. Same sentence, four steps earlier, and now typed so
// the record goes back too.
func TestADatabaseOwnedBySomethingElseIsRefusedBeforeAnythingIsWritten(t *testing.T) {
	for _, st := range []*State{mintingState(), restoringState()} {
		answer, calls := storeAnswering(true, nil)
		err := refuseAStoreAndKeyThatDoNotMatch(st, instanceStore{State: storeStateForeign, Owner: "devicechain"}, answer)

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
		if *calls != 0 {
			t.Errorf("a foreign database still cost a cluster sweep (%d)", *calls)
		}
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
