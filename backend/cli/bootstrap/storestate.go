// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"errors"
	"fmt"

	pgx "github.com/jackc/pgx/v5"
)

// What the shared relational store already holds under one instance's name.
//
// 🔴 THIS IS THE EVIDENCE THAT DECIDES WHETHER A BOOTSTRAP MAY MINT A ROOT KEY, AND IT
// WAS BEING READ ONE STEP TOO LATE. An instance's secrets are sealed by a root key that
// lives only in the cluster's etcd, which no database backup contains. So the recovery
// route is `dcctl install --restore-rdb-from <archive>` followed by `dcctl bootstrap
// --restore-root-key <artifact>` — and a bootstrap that skips the second half mints a
// FRESH key, comes up green, and leaves every recovered row permanently unreadable. The
// store itself says which of the two situations this is: a database already sitting
// there under this instance's name, owned by this instance's own login, is what a
// restore leaves behind.
//
// That question was already being asked — ensureInstanceDatabase has always branched on
// this same owner lookup — but it is asked inside the infrastructure apply, AFTER
// writeMintedSecrets has put the fresh key in the cluster. By then the answer cannot
// change anything. So the read is extracted here and taken again at step 4, and every
// consumer reasons on this one value rather than re-deriving its own.
//
// 🔑 THE STATES NAME WHAT WAS SEEN, NEVER WHY. storeStateOurs has TWO histories — a
// restored store, and a bootstrap that died between provisioning the database and
// writing the configuration document — and no reading of the store can tell them apart.
// Calling it "restored" would bake one of them in and make the other a bug. What
// separates them is whether this cluster already holds the instance, which is a different
// read; see refuseAStoreAndKeyThatDoNotMatch. storeStateAbsent carries the same ambiguity
// for the same reason, one step earlier in the same interrupted run.
type storeState int

const (
	// storeStateUnknown is the zero value ON PURPOSE. A store that will not answer must
	// never read as "nothing there": that direction mints a key over recovered data,
	// which is the one outcome nothing can undo. Anything holding an unset storeState
	// therefore holds "could not tell", and every consumer fails closed on it.
	storeStateUnknown storeState = iota
	// storeStateAbsent — no database by this name. A fresh instance.
	storeStateAbsent
	// storeStateOurs — a database by this name, owned by this instance's own login.
	// Either restored from an archive or left by an interrupted bootstrap.
	storeStateOurs
	// storeStateForeign — a database by this name owned by something else. The
	// pre-isolation shape, or a name collision with something that is not an instance.
	storeStateForeign
)

func (s storeState) String() string {
	switch s {
	case storeStateAbsent:
		return "absent"
	case storeStateOurs:
		return "present and this instance's"
	case storeStateForeign:
		return "present and owned by something else"
	default:
		return "unknown"
	}
}

// instanceStore is one reading of that state, carrying the evidence the refusals quote.
type instanceStore struct {
	State storeState
	// Owner is the database's owner as the store reports it. Meaningful only for
	// storeStateForeign — the other states have no owner to name.
	Owner string
}

// instanceDatabaseOwnerSQL asks who owns the database named after an instance. One
// spelling, because three callers branch on its answer.
const instanceDatabaseOwnerSQL = `select pg_get_userbyid(datdba) from pg_database where datname = $1`

// readInstanceStore asks the shared store what it holds under this instance's name.
//
// An error always comes back with storeStateUnknown, and storeStateUnknown never comes
// back without an error: "could not tell" is a failed run, not a state to act on.
func readInstanceStore(ctx context.Context, q instanceDBQuerier, instance string) (instanceStore, error) {
	var owner string
	err := q.QueryRow(ctx, instanceDatabaseOwnerSQL, instance).Scan(&owner)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return instanceStore{State: storeStateAbsent}, nil
	case err != nil:
		return instanceStore{}, fmt.Errorf("looking up the database for instance %q: %w", instance, err)
	case owner != instance:
		return instanceStore{State: storeStateForeign, Owner: owner}, nil
	default:
		return instanceStore{State: storeStateOurs}, nil
	}
}

// refuseAPreIsolationDatabase is the refusal of a database by this instance's name that
// belongs to something else. Shared by the precheck and by ensureInstanceDatabase so the
// operator reads the same sentence whichever raised it.
//
// 🔴 THE PRE-ISOLATION SHAPE. Before instances had logins of their own, every service
// created this database as the store's shared owner. Its tables belong to that owner, so
// handing the instance its own login would leave services unable to read their own data
// — and quietly re-owning it would carry forward exactly the access that change removed.
func refuseAPreIsolationDatabase(instance string, store instanceStore) error {
	return fmt.Errorf("%w: database %q already exists on the relational store and is owned by %q, "+
		"not by the instance's own login. It was created before each instance had a login of its "+
		"own, or it belongs to something else. Recreate the instance (`dcctl destroy` then "+
		"`dcctl bootstrap`) on a cluster built by this dcctl", errInstanceDatabaseNotOurs, instance, store.Owner)
}

// clusterEvidence is what this cluster can still say about an instance, as two reads that
// are taken only if the arm that needs them is reached. Lazy because each costs a
// different sweep and no ordinary bootstrap needs either.
//
// HoldsInstance also reports WHAT answered — the artifact that named the instance — so a
// refusal can quote what it read rather than only assert its conclusion.
type clusterEvidence struct {
	HoldsInstance       func() (held bool, source string, err error)
	KeepsItsCredentials func() (bool, error)
}

// describeNothingHeld turns the absence HoldsInstance reported into a clause a refusal
// can finish a sentence with. A source that answered and named nothing is worth quoting;
// one that could name nothing at all is not, and saying "according to nothing" would be
// worse than saying less.
func describeNothingHeld(source string) string {
	if source == "" {
		return "nothing in this cluster names an instance of that name either"
	}
	return fmt.Sprintf("%s do not name it either", source)
}

// ErrStoreAndKeyDisagree is the refusal of a bootstrap whose root key and whose
// relational store do not belong together, raised before anything is written. Typed for
// exactly the reason ErrHostTaken is: it is raised by stepCheckClusterSingletons, ahead
// of the first cluster write, so the command layer undoes the local record this run
// wrote on it.
type ErrStoreAndKeyDisagree struct{ Err error }

func (e *ErrStoreAndKeyDisagree) Error() string { return e.Err.Error() }
func (e *ErrStoreAndKeyDisagree) Unwrap() error { return e.Err }

// refuseAStoreAndKeyThatDoNotMatch settles, from one reading of the store and one
// reading of the cluster's declarations, whether this run's root key may meet what the
// relational store already holds.
//
// 🔴 IT REFUSES IN BOTH DIRECTIONS, AND THE TWO COSTS ARE NOT SYMMETRIC. Recovered rows
// met by a freshly minted key are unreadable FOREVER — the key is not derivable and the
// wrapped DEKs are not brute-forceable. A recovered key handed to an empty store costs
// nothing but is still a lie: the operator believes they recovered and did not. So the
// first is the refusal this function exists for, and the second is refused too, because
// there is no run for which it is the right thing to have done.
//
// 🔑 EACH ARM ASKS THE CLUSTER A DIFFERENT QUESTION, AND THE WRITE ORDER IS WHY. Both
// arms face the same ambiguity — a bootstrap that died part-way is re-runnable BY DESIGN
// (that window is the one stepRefuseRebuild deliberately leaves open) and what it leaves
// behind looks from the store exactly like a recovery. But the two arms are at different
// points in that run, so different evidence is available:
//
//   - storeStateOurs asks KeepsItsCredentials. A database exists, and applyInfra writes
//     this instance's Secrets one call BEFORE it creates that database — an order
//     TestApplyInfraAppliesOnlyTheInstanceInOrder pins — so for any run of this bootstrap
//     the two are there together. If the database is here and the Secrets are not, the
//     namespace that held them is gone, and the root key went with it.
//   - storeStateAbsent asks HoldsInstance. There is no database yet, so there are no
//     Secrets to look for either; the earliest artifact that run can have left is its
//     declaration, written two steps earlier.
//
// 🔴 THE DECLARATION IS NOT ENOUGH FOR THE FIRST ARM, AND THAT IS NOT A DETAIL. The
// Instance CRD is CLUSTER-SCOPED, so `kubectl delete ns dci-<instance>` takes the root
// key and the configuration document and LEAVES the declaration standing — while the
// database, which lives in the shared store rather than that namespace, survives too. A
// bootstrap keyed on the declaration would read that as its own half-built work and mint
// over rows whose key the operator had just deleted. The Secrets are in the namespace, so
// they answer that case correctly.
//
// ⚠️ NEITHER READ IS AIRTIGHT, and the honest bound is worth stating: deleting only the
// configuration document while leaving the namespace still presents as a repairable run.
// The Secrets read strictly dominates the declaration read; it does not close everything.
//
// 🔴 THEY ARE THUNKS BECAUSE THE ARMS THAT NEED THEM ARE THE RARE ONES, and passing the
// reads rather than their results is what keeps "which state needs which evidence" from
// being duplicated into the caller — the mistake this whole change exists to undo.
//
// 🔴 --no-escrow NEEDS NO CLAUSE OF ITS OWN, AND ITS ABSENCE HERE IS LOAD-BEARING.
// EscrowFlags.Validate already refuses --no-escrow together with --restore-root-key, so
// requiring the restore against a store that is already ours makes the combination
// unreachable. The message still names it, because an operator who bootstrapped with
// --no-escrow has no artifact to pass and needs to be told that, not sent looking.
func refuseAStoreAndKeyThatDoNotMatch(st *State, store instanceStore, ev clusterEvidence) error {
	restoring := st.Escrow.RestoringRootKey()
	switch store.State {
	case storeStateAbsent:
		if restoring {
			// 🔴 THE RETRY IS NOT THE RECOVERY, AND THE SAME READ SEPARATES THEM HERE TOO.
			// --restore-root-key is ALSO how a bootstrap is re-run after it died having
			// already escrowed its key: WriteEscrow will not overwrite an artifact, and
			// describeBlockingArtifact tells the operator to point this flag at the one on
			// disk. That artifact is written in the RENDER step, one step AFTER the
			// declaration — so a retry always has this cluster holding the instance, and a
			// recovery onto a rebuilt cluster never does. Refusing both would make a
			// half-built instance unrepairable, which is the regression this branch exists
			// to avoid.
			held, source, err := ev.HoldsInstance()
			if err != nil {
				return fmt.Errorf("--restore-root-key names a key for instance %q and the relational "+
					"store holds no database for it, so dcctl asked whether this cluster holds a "+
					"half-built instance of that name — and could not tell: %w", st.Instance, err)
			}
			if held {
				return nil
			}
			// Nothing here, and nothing half-built either. Not destructive, and refused
			// anyway: an instance seeded with a recovered key over an empty store looks
			// like a successful recovery from the outside and holds none of the data the
			// operator came back for. The likeliest cause is that the relational restore
			// did not happen — `dcctl install --restore-rdb-from` is a no-op against a
			// store that already exists — and that is what they need to hear.
			return &ErrStoreAndKeyDisagree{Err: fmt.Errorf(
				"--restore-root-key recovers the key that opens instance %q's stored secrets, and there "+
					"is nothing here for it to open: the relational store holds no database for %q, and "+
					"%s.\n\n"+
					"  To build a NEW instance under this name, drop --restore-root-key — it mints a key "+
					"of its own, and an instance that starts empty needs no older one.\n\n"+
					"  To RECOVER one, the data has to come back first:\n"+
					"    dcctl install %s --restore-rdb-from <archive>\n"+
					"    dcctl bootstrap %s %s --restore-root-key <artifact>\n\n"+
					"  A relational restore only takes effect on a cluster whose store is not there yet; "+
					"run against one that already exists it is silently a no-op, which is the likeliest "+
					"reason that store is empty now.",
				st.Instance, st.Instance, describeNothingHeld(source), st.Provider, st.Provider, st.Instance)}
		}
		return nil

	case storeStateOurs:
		if restoring {
			// The recovery route, taken correctly. Whether the artifact carries the RIGHT
			// key is refuseRestoreOverADifferentKey's question, not this one.
			return nil
		}
		kept, err := ev.KeepsItsCredentials()
		if err != nil {
			return fmt.Errorf("the relational store already holds a database for instance %q, and dcctl "+
				"could not tell whether this cluster still holds the credentials that were written "+
				"beside it: %w. Refusing rather than assuming they are there: assuming wrong mints a "+
				"fresh root key over rows nothing can open afterwards", st.Instance, err)
		}
		if kept {
			// A bootstrap being run again over its own half-built instance: the database
			// and the Secrets written one call before it are both here, which is the state
			// applyInfra leaves when it is interrupted after provisioning the database.
			// Nothing has served from that database yet, so nothing in it is sealed.
			return nil
		}
		return &ErrStoreAndKeyDisagree{Err: fmt.Errorf(
			"the relational store already holds database %q, owned by this instance's own login — but "+
				"namespace %s does not hold the credentials dcctl writes beside that database, one "+
				"call before it is created. The two are only ever separated by the database outliving "+
				"the namespace: this store came back from an archive onto a new cluster, or that "+
				"namespace was deleted out from under a running instance.\n\n"+
				"Either way the root key that sealed the secrets in those rows is gone, and no database "+
				"backup contains it — it lived in that cluster's etcd. Minting a fresh one here would "+
				"come up green and leave every one of them permanently unreadable.\n\n"+
				"  dcctl bootstrap %s %s --restore-root-key <artifact>\n\n"+
				"The artifact is the .escrow file written when the instance was first bootstrapped (by "+
				"default under ~/.devicechain/escrow). If it was bootstrapped with --no-escrow there is "+
				"no artifact and no way to read those rows again: drop the database (`dcctl destroy %s "+
				"%s` clears what is left) and bootstrap a new instance, which starts empty",
			st.Instance, InstanceNamespace(st.Instance), st.Provider, st.Instance, st.Provider, st.Instance)}

	case storeStateForeign:
		// The same refusal ensureInstanceDatabase raises, taken before the root key and
		// four database credentials are written rather than after.
		return &ErrStoreAndKeyDisagree{Err: refuseAPreIsolationDatabase(st.Instance, store)}

	default:
		return fmt.Errorf("the relational store did not say what it holds for instance %q, and dcctl "+
			"will not assume it holds nothing: a bootstrap onto a store it could not read is how a "+
			"recovered instance gets a fresh root key and loses every secret in it", st.Instance)
	}
}
