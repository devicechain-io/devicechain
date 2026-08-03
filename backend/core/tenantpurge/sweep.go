// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package tenantpurge

import (
	"context"
	"fmt"
	"strings"

	"gorm.io/gorm"
)

// TableResult is what one table contributed to a sweep or a residual scan.
type TableResult struct {
	Table Table
	Class Class
	Rows  int64
}

// Result is the outcome of a Sweep or a Residue scan over one tenant.
type Result struct {
	// Tenant is the token acted on.
	Tenant string
	// Tables lists every table that contributed rows, in delete order — rows DELETED
	// when this came from Sweep, rows STILL PRESENT when it came from Residue. The
	// field is named for what it holds rather than for either caller, because the two
	// readings are opposites and a name that suited one would misreport the other.
	// Tables contributing zero rows are omitted: a report of 65 zeroes buries the four
	// lines that matter, and for Residue an empty list is precisely the success case.
	Tables []TableResult
	// Rows is the total across Tables.
	Rows int64
	// Deferred lists tables that hold this tenant's data and were NOT erased. It is
	// carried on every result so a caller cannot report a complete erasure without
	// first having to look at what was left behind.
	Deferred []Entry
}

// Complete reports whether the sweep erased everything it found, i.e. whether nothing
// was deferred. A caller about to write a deletion record must consult this — "Sweep
// returned no error" is a much weaker claim and does not mean the tenant is gone.
func (r Result) Complete() bool { return len(r.Deferred) == 0 }

// ErrUnclassified is returned when a plan contains a table this package cannot
// explain. It carries the offending tables so the message names them.
type ErrUnclassified struct{ Tables []Table }

func (e *ErrUnclassified) Error() string {
	names := make([]string, 0, len(e.Tables))
	for _, t := range e.Tables {
		names = append(names, t.String())
	}
	return fmt.Sprintf("%d table(s) hold rows the tenant purge cannot classify, so a purge would "+
		"silently retain data: %s. Give each one a tenant column, a foreign key into tenant-bearing "+
		"data, or an entry in the tenantpurge exemption registry stating why it is safe to skip",
		len(e.Tables), strings.Join(names, ", "))
}

// checkClassified fails a plan that contains unclassified tables.
//
// This is the fail-closed hinge of the package, and it deliberately fires before any
// row is touched. The alternative — sweep what we understand, skip the rest — produces
// the exact outcome ADR-077 exists to prevent: a purge reporting success while a table
// nobody classified keeps the tenant's data indefinitely.
func checkClassified(plan *Plan) error {
	var bad []Table
	for _, e := range plan.Entries {
		if e.Class == ClassUnclassified {
			bad = append(bad, e.Table)
		}
	}
	if len(bad) > 0 {
		return &ErrUnclassified{Tables: bad}
	}
	return nil
}

// Sweep deletes every row belonging to tenant, in foreign-key-safe order.
//
// The whole sweep runs in ONE transaction. Not for speed: a partially swept tenant is
// a database whose foreign keys hold but whose erasure claim is false, and nothing
// from outside can tell which half ran. Either the rows are gone or nothing moved.
//
// # Why raw SQL rather than the gorm model path
//
// Two reasons, both correctness rather than taste. The tenant-scope callbacks inject a
// predicate from the tenant in the context, so a gorm delete would need the purge to
// impersonate the tenant it is erasing — and would silently do nothing for
// event-processing's projections, which those callbacks do not recognise. And several
// models embed gorm.Model, so a gorm delete is a SOFT delete: it would set deleted_at,
// report rows affected, and leave every byte in place.
//
// # Why the token alone identifies the rows
//
// The tenant's own row survives in `purging` state for the whole purge, and its unique
// index on the token is what stops a successor being created at that token meanwhile.
// So while this runs the token is unambiguous and the sweep needs no epoch bound. The
// epoch is load-bearing for what OUTLIVES the sweep — the deletion record, and any
// fence that survives the row — not for the delete itself.
func Sweep(ctx context.Context, db *gorm.DB, plan *Plan, tenant string) (Result, error) {
	res := Result{Tenant: tenant, Deferred: plan.OfClass(ClassDeferred)}
	if err := checkSweepable(plan, tenant); err != nil {
		return res, err
	}
	idx := plan.index()

	err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		for _, e := range plan.Actionable() {
			where, args, err := tenantPredicate(idx, e.Table, tenant, 0)
			if err != nil {
				return err
			}
			out := tx.Exec(fmt.Sprintf("DELETE FROM %s WHERE %s", e.Table.quoted(), where), args...)
			if out.Error != nil {
				return fmt.Errorf("sweep %s: %w", e.Table, out.Error)
			}
			if out.RowsAffected > 0 {
				res.Tables = append(res.Tables, TableResult{
					Table: e.Table, Class: e.Class, Rows: out.RowsAffected,
				})
				res.Rows += out.RowsAffected
			}
		}
		return nil
	})
	if err != nil {
		return Result{Tenant: tenant, Deferred: res.Deferred}, err
	}
	return res, nil
}

// Residue counts the rows that still identify as tenant's after a sweep — the
// re-verify half of plant → act → re-verify, at tenant scope.
//
// It is a genuinely weaker check than it looks, and saying so is the point: it asks
// the same classification the same question, so a table the plan never knew about is
// invisible to both. What actually makes coverage complete is checkClassified, which
// refuses to run at all while any table is unexplained. This function's job is the
// narrower one of catching a delete that did not take — a row re-inserted by a service
// still running, a subquery that matched nothing because the delete order was wrong, a
// trigger that put something back.
//
// It is therefore worth running twice with a pause between: the resurrection vectors
// ADR-077 catalogues are all writes by a live service, and a scan run immediately after
// the transaction commits can only see the ones that were already in flight.
func Residue(ctx context.Context, db *gorm.DB, plan *Plan, tenant string) (Result, error) {
	res := Result{Tenant: tenant, Deferred: plan.OfClass(ClassDeferred)}
	if err := checkSweepable(plan, tenant); err != nil {
		return res, err
	}
	idx := plan.index()

	for _, e := range plan.Actionable() {
		where, args, err := tenantPredicate(idx, e.Table, tenant, 0)
		if err != nil {
			return res, err
		}
		var n int64
		q := db.WithContext(ctx).Raw(
			fmt.Sprintf("SELECT count(*) FROM %s WHERE %s", e.Table.quoted(), where), args...).Scan(&n)
		if q.Error != nil {
			return res, fmt.Errorf("residual scan %s: %w", e.Table, q.Error)
		}
		if n > 0 {
			res.Tables = append(res.Tables, TableResult{Table: e.Table, Class: e.Class, Rows: n})
			res.Rows += n
		}
	}
	return res, nil
}

// checkSweepable rejects the two ways a caller can ask for something destructive and
// wrong: an empty token, and a plan with unexplained tables.
func checkSweepable(plan *Plan, tenant string) error {
	if tenant == "" {
		return fmt.Errorf("refusing to act on an empty tenant token: it would match every row whose " +
			"tenant column was never set, in every area")
	}
	return checkClassified(plan)
}

// maxPredicateDepth bounds the recursion in tenantPredicate.
//
// Cycles are already rejected when the plan is built, so reaching this bound means the
// schema grew a join chain deeper than any that has ever existed. Failing is right:
// the alternative is a query nested deeply enough that nobody reviewing a purge could
// say what it matches.
const maxPredicateDepth = 8

// tenantPredicate builds the WHERE clause identifying tenant's rows in table t,
// returning the clause and its bind arguments in order.
//
// For a direct table that is a column test. For a transitive table it is the OR of one
// subquery per link into a tenant-bearing parent — OR rather than AND, deliberately: a
// join row referencing the purged tenant's membership is that tenant's data whatever
// its other columns point at, and requiring every link to match would leave exactly the
// rows joining the purged tenant to something shared, which is most of them.
//
// The recursion handles a parent that is itself transitive. That case does not arise in
// today's schema (the one transitive table joins straight to a direct parent), but the
// depth is a property of whatever migrations exist at the time, not of this code, so it
// is resolved rather than assumed away. Delete order emits children before parents, so
// every parent a subquery reads is still populated when the child is swept.
func tenantPredicate(idx map[Table]Entry, t Table, tenant string, depth int) (string, []any, error) {
	if depth > maxPredicateDepth {
		return "", nil, fmt.Errorf("foreign-key chain deeper than %d while identifying %s's rows in %s",
			maxPredicateDepth, tenant, t)
	}
	e, ok := idx[t]
	if !ok {
		return "", nil, fmt.Errorf("%s is referenced by a foreign key but is not in the plan", t)
	}

	switch e.Class {
	case ClassDirect:
		return fmt.Sprintf("%s = ?", quoteIdent(e.Column)), []any{tenant}, nil

	case ClassTransitive:
		if len(e.Links) == 0 {
			return "", nil, fmt.Errorf("%s is transitive but carries no links", t)
		}
		clauses := make([]string, 0, len(e.Links))
		var args []any
		for _, l := range e.Links {
			if len(l.Columns) != len(l.ParentColumns) {
				return "", nil, fmt.Errorf("%s: foreign key to %s pairs %d columns with %d",
					t, l.Parent, len(l.Columns), len(l.ParentColumns))
			}
			inner, innerArgs, err := tenantPredicate(idx, l.Parent, tenant, depth+1)
			if err != nil {
				return "", nil, err
			}
			// A row whose referencing columns are NULL references nothing, so it is
			// not reached through this link; `IN (subquery)` already excludes it.
			clauses = append(clauses, fmt.Sprintf("(%s) IN (SELECT %s FROM %s WHERE %s)",
				strings.Join(quoteAll(l.Columns), ", "),
				strings.Join(quoteAll(l.ParentColumns), ", "),
				l.Parent.quoted(), inner))
			args = append(args, innerArgs...)
		}
		return strings.Join(clauses, " OR "), args, nil

	default:
		return "", nil, fmt.Errorf("%s is %s and identifies no tenant's rows", t, e.Class)
	}
}

func quoteAll(names []string) []string {
	out := make([]string, len(names))
	for i, n := range names {
		out[i] = quoteIdent(n)
	}
	return out
}

// index maps each table to its entry, for the predicate builder's parent lookups.
func (p *Plan) index() map[Table]Entry {
	idx := make(map[Table]Entry, len(p.Entries))
	for _, e := range p.Entries {
		idx[e.Table] = e
	}
	return idx
}
