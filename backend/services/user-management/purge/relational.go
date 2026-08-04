// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package purge

import (
	"context"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/devicechain-io/dc-microservice/tenantpurge"
)

// Relational erases a tenant from a Postgres database, across every functional area's
// schema in it.
//
// It is built to serve BOTH clusters — the relational store, where ten areas share a
// database, and the telemetry store, where event-management is alone in another — because
// they differ only in the connection and the ledger name: the classification, the delete
// order and the residual scan are identical, both being one database with one owning role
// and one catalog that answers for all of it. Today only the relational one is
// registered; the telemetry store is still Pending (see stores.go), which is why the name
// is a parameter rather than a constant.
type Relational struct {
	name string
	db   *rdb.RdbManager
	pre  func(tenant string) tenantpurge.Precondition
}

// NewRelational builds the store for a database. name keys the ledger and is persisted.
//
// pre builds the check run inside the sweep's own transaction, for the tenant being
// erased; it may be nil. Supplying one is what
// makes the lifecycle interlock un-raceable rather than merely checked-recently, so the
// relational cluster — the one whose sweep can erase every area at once — always gets it.
// The telemetry cluster cannot: iam_tenants is in the other database, so nothing there can
// read the lifecycle under the deleting transaction's snapshot. That store's interlock is
// the coordinator's re-read alone, and saying so is better than implying otherwise.
func NewRelational(name string, db *rdb.RdbManager,
	pre func(tenant string) tenantpurge.Precondition) *Relational {
	return &Relational{name: name, db: db, pre: pre}
}

func (r *Relational) Name() string { return r.name }

// Erase classifies the database, deletes the tenant's rows, and reads back whether any
// remain.
//
// # The plan is rebuilt on every tenant's pass
//
// Classification is a snapshot of the catalog, and migrations append normally now, so a
// plan held across a deploy that added a table would simply never sweep it — and nothing
// would say so, because the fail-closed check inspects the plan rather than the database.
// Two catalog reads against a table count in the tens is not a cost worth optimising into
// a silent retention bug.
//
// It is per TENANT, not per pass, because that is where the call sits — with N tenants
// purging at once a pass classifies N times identically. Hoisting it to the pass would
// need either a stage in the Store interface or a plan handed across it, and both are
// worse than the two queries they save while N is the 0 or 1 it is in practice.
//
// # A residual row is a failure, not a deferral
//
// The two are recorded differently on purpose. A deferral is data this mechanism does not
// erase and will not erase until someone builds something. A residual row is data it DID
// erase which came back — which means a writer is still live for a tenant whose access
// was cut, i.e. one of the resurrection vectors ADR-077 catalogues. That is transient by
// nature: the next pass sweeps it again, and the settle window will not let the purge
// complete until a pass finds nothing. So it is returned as an error, which is what the
// ledger records as retryable.
func (r *Relational) Erase(ctx context.Context, tenant string, _ time.Time) (Outcome, error) {
	db := r.handle(ctx)

	plan, err := tenantpurge.Classify(ctx, db)
	if err != nil {
		return Outcome{}, fmt.Errorf("classifying %s: %w", r.name, err)
	}

	var pre tenantpurge.Precondition
	if r.pre != nil {
		pre = r.pre(tenant)
	}
	swept, err := tenantpurge.Sweep(ctx, db, plan, tenant, pre)
	if err != nil {
		return Outcome{}, fmt.Errorf("sweeping %s: %w", r.name, err)
	}

	out := Outcome{Rows: swept.Rows, Deferred: deferrals(swept)}

	residue, err := tenantpurge.Residue(ctx, db, plan, tenant)
	if err != nil {
		return out, fmt.Errorf("residual scan of %s: %w", r.name, err)
	}
	if residue.Rows > 0 {
		return out, fmt.Errorf("%d row(s) still identify as %q in %s after the sweep (%s) — "+
			"something is still writing for a tenant whose access was cut",
			residue.Rows, tenant, r.name, describe(residue))
	}
	return out, nil
}

// handle returns a gorm handle in the system context.
//
// The system context is what the fail-closed tenant callback requires for a query that
// carries no tenant, and every statement here is exactly that: the sweep names the tenant
// in its own predicate rather than inheriting one, because it has to reach tables the
// callback does not recognise (event-processing's projections key the tenant as a plain
// column) and tables belonging to no tenant at all.
func (r *Relational) handle(ctx context.Context) *gorm.DB {
	return r.db.DB(core.WithSystemContext(ctx))
}

// deferrals turns a plan's deferred tables into the sentences the ledger carries. The
// registry's reason is the sentence — it was written to be read by someone deciding
// whether an erasure claim can be made, which is this exact reader.
func deferrals(res tenantpurge.Result) []string {
	out := make([]string, 0, len(res.Deferred))
	for _, e := range res.Deferred {
		out = append(out, fmt.Sprintf("%s: %s", e.Table, e.Reason))
	}
	return out
}

// describe renders a residual scan's tables for an error message. A count alone tells an
// operator a purge is stuck without telling them where to look.
func describe(res tenantpurge.Result) string {
	parts := make([]string, 0, len(res.Tables))
	for _, t := range res.Tables {
		parts = append(parts, fmt.Sprintf("%s=%d", t.Table, t.Rows))
	}
	if len(parts) == 0 {
		return "no table named them, which should be impossible"
	}
	return strings.Join(parts, ", ")
}
