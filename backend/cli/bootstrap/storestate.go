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
// 🔑 clusterHoldsInstance IS WHAT SEPARATES THE TWO HISTORIES OF storeStateOurs, AND IT
// IS ONLY LEGIBLE HERE. A bootstrap that died between provisioning the database and
// writing the configuration document is re-runnable BY DESIGN — that window is the one
// stepRefuseRebuild deliberately leaves open — and it leaves a database that looks
// exactly like a restored one. But that database can only exist if the same run already
// wrote this instance's declaration two steps earlier, while a recovery onto a rebuilt
// cluster faces fresh etcd and has written nothing. THIS run's declaration lands at step
// 6, after this check, so the distinction holds at step 4 and is gone by step 7.
//
// 🔴 IT IS A THUNK BECAUSE THE BRANCHES THAT NEED IT ARE THE RARE ONES. The read walks
// declarations, minted Secrets and Helm releases; asking it on every ordinary bootstrap
// would be a cluster-wide sweep to answer a question only a recovery or a retry raises.
// Passing the read rather than its result is what keeps "which state needs this" from
// being duplicated into the caller — the mistake this whole change exists to undo.
//
// 🔴 --no-escrow NEEDS NO CLAUSE OF ITS OWN, AND ITS ABSENCE HERE IS LOAD-BEARING.
// EscrowFlags.Validate already refuses --no-escrow together with --restore-root-key, so
// requiring the restore against a store that is already ours makes the combination
// unreachable. The message still names it, because an operator who bootstrapped with
// --no-escrow has no artifact to pass and needs to be told that, not sent looking.
func refuseAStoreAndKeyThatDoNotMatch(st *State, store instanceStore, clusterHoldsInstance func() (bool, error)) error {
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
			held, err := clusterHoldsInstance()
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
				"--restore-root-key recovers the key that opens instance %q's stored secrets, but the "+
					"relational store holds no database for %q, so there is nothing here for it to open.\n\n"+
					"  Recover the data first:  dcctl install %s --restore-rdb-from <archive>\n"+
					"  Then bootstrap:          dcctl bootstrap %s %s --restore-root-key <artifact>\n\n"+
					"A relational restore only takes effect on a cluster whose store is not there yet — "+
					"run against one that already exists it is silently a no-op. To build a NEW instance "+
					"under this name, drop --restore-root-key: it will mint a key of its own",
				st.Instance, st.Instance, st.Provider, st.Provider, st.Instance)}
		}
		return nil

	case storeStateOurs:
		if restoring {
			// The recovery route, taken correctly. Whether the artifact carries the RIGHT
			// key is refuseRestoreOverADifferentKey's question, not this one.
			return nil
		}
		held, err := clusterHoldsInstance()
		if err != nil {
			return fmt.Errorf("the relational store already holds a database for instance %q, and dcctl "+
				"could not tell whether this cluster holds the instance it belongs to: %w. Refusing "+
				"rather than assuming it does: assuming wrong mints a fresh root key over recovered "+
				"rows, and nothing can open them afterwards", st.Instance, err)
		}
		if held {
			// A bootstrap being run again over its own half-built instance. The database
			// is this run's own work, not somebody's recovery, and the credential
			// machinery already knows how to finish it.
			return nil
		}
		return &ErrStoreAndKeyDisagree{Err: fmt.Errorf(
			"the relational store already holds database %q, owned by this instance's own login, and "+
				"this cluster has no declaration for it — so it came back from an archive rather than "+
				"from a run of this bootstrap.\n\n"+
				"Every secret in those rows is sealed by instance %q's root key, and no database backup "+
				"contains that key: it lived in the destroyed cluster's etcd. Minting a fresh one here "+
				"would come up green and leave all of them permanently unreadable.\n\n"+
				"  dcctl bootstrap %s %s --restore-root-key <artifact>\n\n"+
				"The artifact is the .escrow file written when the instance was first bootstrapped "+
				"(by default under ~/.devicechain/escrow). If that instance was bootstrapped with "+
				"--no-escrow there is no artifact and no way to read those rows again; drop the "+
				"database (`dcctl destroy %s %s` clears what is left) and bootstrap a new instance, "+
				"which starts empty",
			st.Instance, st.Instance, st.Provider, st.Instance, st.Provider, st.Instance)}

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
