// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package purge reclaims a deleted tenant's data (ADR-077).
//
// # What the unit of erasure is, and why it is not the functional area
//
// A tenant's data lives in STORAGE SYSTEMS, not in services. The platform has five —
// the relational database, the telemetry database, the broker, the key-value store and
// the object store — plus state that is only ever in a running process. A functional
// area is a code boundary; it is not a place data is, and several areas share one
// storage system while one area (event-management) is alone in another.
//
// So the erasure is organised by store. That is a deliberate refinement of ADR-077
// decision 5, which had each AREA reconcile its own rows, and it follows that ADR's own
// decision 7 to its conclusion: once the set of tenant-bearing tables is enumerated from
// the CATALOG rather than from a list each area maintains, the area stops being the
// thing that knows anything. Both Postgres clusters are one owning role each, so the
// catalog answers "what holds this tenant's data" completely, for every schema at once,
// where eleven hand-maintained lists answer "what eleven people remembered".
//
// The trade is real and worth stating: a store-shaped erasure means this service issues
// deletes in schemas it does not own. What it buys is the failure mode that per-area
// reclamation cannot close — an area with no purge participant contributes SILENCE, and
// silence is indistinguishable from success. There is no way to be silent here: a table
// this package cannot explain fails the purge before a row is touched
// (core/tenantpurge), and a store that is registered but not yet built reports what it
// is still holding on every single pass.
//
// # What is still a participant
//
// State no SQL predicate can reach — the DETECT engine's in-process windows and timers,
// checkpointed as one opaque blob per partition — cannot be erased from here at any
// price. That is carried as a deferral by the relational store's own plan, which is
// where it becomes visible, and it is what stops a purge completing today.
package purge

import (
	"context"
	"strings"
	"time"

	"gorm.io/gorm"
)

// Store is one storage system a tenant's data lives in.
//
// The interface is small on purpose. A store is asked to erase and to say what it still
// holds; it is never asked whether it is done, because a store grading itself is the
// thing the coordinator's settle window exists to distrust.
type Store interface {
	// Name keys this store's line in the deletion record's ledger. It is PERSISTED, so
	// renaming one orphans the history of every purge that used the old name.
	Name() string

	// Erase removes tenant's data and reports what it removed and what it did not.
	//
	// It is called on every pass until it reports clean, so it must be idempotent: the
	// second call has nothing to delete and must succeed, reporting zero rows rather
	// than an error. An error means "try again", not "this store is broken" — the
	// coordinator records it and comes back.
	//
	// epoch dates the cut. It is passed to every store even though a store whose keys
	// are the token alone has no use for it, because the ones that outlive the tenant
	// row need it to tell the purged tenant's residue from a successor's rows.
	Erase(ctx context.Context, tenant string, epoch time.Time) (Outcome, error)
}

// Database is the handle a store sweeps through, narrowed to the one method the sweep
// uses so that BOTH kinds of connection satisfy it: rdb.RdbManager, for the database this
// service owns, and rdb.Guest, for one it does not (the telemetry cluster). Taking the
// interface rather than the concrete manager is what lets the two stores be the same code
// rather than a copy with the receiver type changed.
type Database interface {
	DB(ctx context.Context) *gorm.DB
}

// Outcome is what one store reports for one pass.
type Outcome struct {
	// Rows is how much was erased on this pass. Informational: it proves the sweep ran,
	// and it cannot prove anything remains, which is what Deferred and the residual
	// scan inside each store are for.
	Rows int64

	// Deferred names data this store HOLDS AND DID NOT ERASE. Empty means clean.
	//
	// Each entry must name the data, in a sentence someone deciding whether to make an
	// erasure claim can act on. "detect_snapshots" is not an entry; "the DETECT engine's
	// checkpoint still contains this tenant's open windows and buffered event values" is.
	Deferred []string
}

// Clean reports whether this store erased everything it holds for the tenant.
func (o Outcome) Clean() bool { return len(o.Deferred) == 0 }

// Reason renders Deferred for the ledger.
func (o Outcome) Reason() string { return strings.Join(o.Deferred, "; ") }

// Pending is a store that is known to hold tenant data and has no erasure yet.
//
// 🔴 THIS TYPE IS THE HONEST PART AND IT IS NOT SCAFFOLDING. The alternative to
// registering a store that does nothing is not registering it — and then the coordinator
// completes a purge, writes a record saying the tenant's data is gone, and releases the
// token, over a store nobody remembered. A Pending store makes that hole a line in every
// ledger of every purge, stating in words what is still retained, and it makes completion
// impossible until someone replaces it. It is deleted by building the real store, not by
// removing the registration.
type Pending struct {
	// StoreName is the ledger key the real store will inherit, so the history carries
	// across the day it is built.
	StoreName string
	// Holds names the tenant data this store retains. It goes straight into the
	// deletion record, so it is written for a reader, not for a backlog.
	Holds string
}

func (p Pending) Name() string { return p.StoreName }

// Erase does nothing and says so. It never errors: a pending store is not failing, it is
// a known and stated gap, and reporting it as a failure would make it look transient.
func (p Pending) Erase(context.Context, string, time.Time) (Outcome, error) {
	return Outcome{Deferred: []string{p.Holds}}, nil
}
