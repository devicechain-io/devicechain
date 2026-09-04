// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package tenantpurge

import (
	"context"
	"fmt"
	"strings"

	"gorm.io/gorm"

	"github.com/devicechain-io/dc-microservice/rdb"
)

// TableResult is what one table contributed to a sweep or a residual scan.
type TableResult struct {
	Table Table
	Class Class
	// Origin carries Entry.Origin through, so a relation whose generated name means
	// nothing on its own can still be named in the message an operator gets when a purge
	// is STUCK — which is the one moment the provenance matters most.
	Origin string
	Rows   int64
}

// Describe renders the table for a human, with its provenance when it has any.
func (r TableResult) Describe() string { return describeTable(r.Table, r.Origin) }

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

	// Redacted lists every table whose rows were KEPT with their identifiers destroyed —
	// rows redacted when this came from Sweep, rows STILL CARRYING an identifier when it
	// came from Residue.
	//
	// 🔴 IT IS A SEPARATE FIELD BECAUSE Tables CANNOT CARRY IT HONESTLY. That field means
	// "deleted" on one side of the call and "still present" on the other, and a redacted
	// row is neither: it is a row deliberately still present whose contents changed.
	// Folding the count in would make a successful retention read to Residue's caller as
	// a failed erasure, turning the correct outcome into a permanently stuck purge.
	Redacted []TableResult
}

// Complete reports whether the sweep erased everything it found, i.e. whether nothing
// was deferred. A caller about to write a deletion record must consult this — "Sweep
// returned no error" is a much weaker claim and does not mean the tenant is gone.
func (r Result) Complete() bool { return len(r.Deferred) == 0 }

// ErrUnclassified is returned when a plan contains a table this package cannot
// explain. It carries the offending entries so the message names them — entries rather
// than tables, because a relation admitted from another catalog (a continuous
// aggregate's materialization) has a name that means nothing on its own and needs the
// provenance Entry.Describe carries.
type ErrUnclassified struct{ Entries []Entry }

func (e *ErrUnclassified) Error() string {
	names := make([]string, 0, len(e.Entries))
	for _, t := range e.Entries {
		names = append(names, t.Describe())
	}
	return fmt.Sprintf("%d table(s) hold rows the tenant purge cannot classify, so a purge would "+
		"silently retain data: %s. Give each one a tenant column, a foreign key into tenant-bearing "+
		"data, or an entry in the tenantpurge exemption registry stating why it is safe to skip",
		len(e.Entries), strings.Join(names, ", "))
}

// checkClassified fails a plan that contains unclassified tables.
//
// This is the fail-closed hinge of the package, and it deliberately fires before any
// row is touched. The alternative — sweep what we understand, skip the rest — produces
// the exact outcome ADR-077 exists to prevent: a purge reporting success while a table
// nobody classified keeps the tenant's data indefinitely.
func checkClassified(plan *Plan) error {
	var bad []Entry
	for _, e := range plan.Entries {
		if e.Class == ClassUnclassified {
			bad = append(bad, e)
		}
	}
	if len(bad) > 0 {
		return &ErrUnclassified{Entries: bad}
	}
	return nil
}

// Precondition is a check the caller runs INSIDE the sweep's transaction, before the
// first delete. Returning an error aborts the sweep with nothing written.
//
// It exists for one specific claim: that the token still names a tenant it is safe to
// erase. Establishing that outside the transaction proves it was true at some earlier
// moment, which is a different and much weaker statement — see Sweep.
type Precondition func(tx *gorm.DB) error

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
// epoch is load-bearing for what OUTLIVES the sweep — the deletion record, and any fence
// that survives the row — not for the delete itself.
//
// # 🔴 CALLER'S PRECONDITION: the tenant must already be in `purging`
//
// This function does not know what that means, and deliberately does not: the lifecycle
// lives in user-management's iam_tenants, and teaching core about that table would invert
// the layering for every other user of the package. So the guarantee is the CALLER's, and
// it is not a formality — the whole sweep commits in one transaction, so a transposed
// token erases a live tenant completely and atomically, with no partial state to notice
// on the way.
//
// What this function DOES provide is somewhere to put that check where it cannot go
// stale: pre runs inside the transaction, so the lifecycle is read under the same snapshot
// that is about to do the deleting. A caller that checks beforehand instead is checking a
// value that can change before the first DELETE lands — and the gap is not theoretical,
// because the coordinator's advisory lock is a session lease, not a fence: lose the
// connection holding it and the pass keeps running on other pooled connections while a
// peer starts its own. pre is what makes that race lose safely. It may be nil, for a
// caller with no lifecycle to consult (a test, or a database that does not hold the
// tenant table) — and such a caller has the weaker guarantee, by construction.
func Sweep(ctx context.Context, db *gorm.DB, plan *Plan, tenant string, pre Precondition) (Result, error) {
	res := Result{Tenant: tenant, Deferred: plan.OfClass(ClassDeferred)}
	if err := checkSweepable(plan, tenant); err != nil {
		return res, err
	}
	idx := plan.index()

	err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if pre != nil {
			if err := pre(tx); err != nil {
				return fmt.Errorf("refusing to sweep %q: %w", tenant, err)
			}
		}
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
					Table: e.Table, Class: e.Class, Origin: e.Origin, Rows: out.RowsAffected,
				})
				res.Rows += out.RowsAffected
			}
		}

		// The retained tables, in the same transaction as the deletes. Either the
		// tenant's rows are gone and its identifiers are destroyed, or nothing moved —
		// a redaction that committed while the sweep rolled back would leave a journal
		// that could no longer say who did what, over data still present.
		for _, e := range plan.OfClass(ClassRedacted) {
			where, args, err := tenantPredicate(idx, e.Table, tenant, 0)
			if err != nil {
				return err
			}
			sets := make([]string, 0, len(e.Redact))
			setArgs := make([]any, 0, len(e.Redact))
			carrying := make([]string, 0, len(e.Redact))
			for _, col := range e.Redact {
				sets = append(sets, fmt.Sprintf("%s = ?", rdb.QuoteIdentifier(col)))
				setArgs = append(setArgs, "")
				carrying = append(carrying, fmt.Sprintf("%s <> ''", rdb.QuoteIdentifier(col)))
			}
			// 🔴 THE UPDATE EXCLUDES ROWS IT HAS ALREADY EMPTIED, and that clause is what
			// makes the count mean anything. A purge sweeps on every pass, and without it
			// this statement would rewrite the tenant's whole journal every minute for
			// twelve hours and report a non-zero figure forever — so "identifiers
			// destroyed on this pass" would be indistinguishable from "identifiers
			// destroyed at some point", which is the number the deletion record cites.
			//
			// It is NOT what keeps the purge completable, and reading it that way would
			// be the dangerous mistake: the settle window is restarted by Outcome.Rows,
			// which carries the DELETE count alone (purge/coordinator.go, eraseOne). A
			// redaction that ran forever would waste work and lie in the ledger; it would
			// not stall the erasure. Both are worth fixing, only one is worth fearing.
			out := tx.Exec(fmt.Sprintf("UPDATE %s SET %s WHERE %s AND (%s)",
				e.Table.quoted(), strings.Join(sets, ", "), where, strings.Join(carrying, " OR ")),
				append(setArgs, args...)...)
			if out.Error != nil {
				return fmt.Errorf("redacting %s: %w", e.Table, out.Error)
			}
			if out.RowsAffected > 0 {
				res.Redacted = append(res.Redacted, TableResult{
					Table: e.Table, Class: e.Class, Origin: e.Origin, Rows: out.RowsAffected,
				})
			}
		}
		return nil
	})
	if err != nil {
		return Result{Tenant: tenant, Deferred: res.Deferred}, err
	}
	return res, nil
}

// Residue counts the rows that still identify as tenant's after a sweep — the re-verify
// half of plant → act → re-verify, at tenant scope.
//
// # What it can and cannot see, stated exactly
//
// It is a weaker check than it looks, and saying so is the point. It asks the same
// classification the same question, so a table the plan never knew about is invisible to
// both. What actually makes coverage complete is checkClassified, which refuses to run
// at all while any table is unexplained; this function's narrower job is catching a
// delete that did not take — most realistically a row re-inserted by a service that is
// still running, which is every resurrection vector ADR-077 catalogues.
//
// For a TRANSITIVE table it does not check the child directly, and cannot: the predicate
// selects children of tenant-bearing parents, and the sweep has just deleted those
// parents, so the subquery matches nothing whatever the child holds. What makes this
// sound rather than vacuous is the foreign key itself — a table is only transitive
// BECAUSE a foreign key ties it to that parent, so a surviving child row would have
// blocked the parent's delete and failed the sweep outright. A resurrection that
// re-creates both is caught through the parent, which is direct. A leftover child alone
// is not a state the database permits.
//
// It is worth running twice with a pause between. A scan run the instant the transaction
// commits can only see writes that were already in flight.
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
			res.Tables = append(res.Tables, TableResult{
				Table: e.Table, Class: e.Class, Origin: e.Origin, Rows: n})
			res.Rows += n
		}
	}

	// 🔴 A RETAINED TABLE NEEDS ITS OWN RE-VERIFY, and counting its rows would be the
	// wrong question — they are supposed to be there. What is checked is that none of
	// them still carries an identifier. Without this the redaction is the one step in
	// the whole purge that grades itself: the UPDATE reports a row count, and a row
	// count is exactly what a WHERE clause that selected the wrong rows also produces.
	for _, e := range plan.OfClass(ClassRedacted) {
		if len(e.Redact) == 0 {
			return res, fmt.Errorf("%s is classified redacted but names no columns to empty, so "+
				"the purge would report its identifiers destroyed having touched nothing", e.Table)
		}
		where, args, err := tenantPredicate(idx, e.Table, tenant, 0)
		if err != nil {
			return res, err
		}
		carrying := make([]string, 0, len(e.Redact))
		for _, col := range e.Redact {
			// A bare `<> ''` is right, and it is worth saying why rather than reaching for
			// coalesce. `NULL <> ''` evaluates to NULL, so a NULL is not counted — and
			// "not counted" is the correct answer for a column holding no identifier.
			// Both spellings of absent, the empty string this purge writes and the NULL a
			// row may always have carried, read as clean because they ARE clean.
			carrying = append(carrying, fmt.Sprintf("%s <> ''", rdb.QuoteIdentifier(col)))
		}
		var n int64
		q := db.WithContext(ctx).Raw(fmt.Sprintf("SELECT count(*) FROM %s WHERE %s AND (%s)",
			e.Table.quoted(), where, strings.Join(carrying, " OR ")), args...).Scan(&n)
		if q.Error != nil {
			return res, fmt.Errorf("residual identifier scan %s: %w", e.Table, q.Error)
		}
		if n > 0 {
			res.Redacted = append(res.Redacted, TableResult{
				Table: e.Table, Class: e.Class, Origin: e.Origin, Rows: n})
		}
	}
	return res, nil
}

// Retained reports whether a residual scan found rows still carrying an identifier in a
// table the purge keeps. It is deliberately not folded into Rows: a caller reading Rows
// is asking "did the deletes take", and a caller reading this is asking "did the
// retention keep its promise". Both must be false for an area to ack.
func (r Result) Retained() int64 {
	var n int64
	for _, t := range r.Redacted {
		n += t.Rows
	}
	return n
}

// checkSweepable rejects the three ways a caller can ask for something destructive and
// wrong: an empty token, an empty plan, and a plan with unexplained tables.
func checkSweepable(plan *Plan, tenant string) error {
	if tenant == "" {
		return fmt.Errorf("refusing to act on an empty tenant token: it would match every row whose " +
			"tenant column was never set, in every area")
	}
	// 🔴 AN EMPTY PLAN IS THE MOST REASSURING WAY THIS PACKAGE CAN LIE, and every other
	// check here passes vacuously on it: checkClassified finds no unclassified table, the
	// sweep deletes from no table, and Residue reads no table — so the caller is told the
	// tenant is gone from a database nobody looked inside.
	//
	// No correct steady state produces it. A database whose owning area was never deployed
	// does not EXIST, which a caller learns from the connection rather than from here. One
	// that exists with no tables means a restore still loading, migrations that have not
	// run, or a connection to the wrong database — transient or misconfigured, and in
	// every case something to block on rather than certify.
	//
	// This is the runtime twin of assertPlanIsPopulated in tools/migrationdiff, which
	// refuses an empty plan in CI for the same reason and with the same wording: no
	// findings is also what measuring nothing looks like.
	if len(plan.Entries) == 0 {
		return fmt.Errorf("refusing to act on an empty classification: a database with no tables in "+
			"it would report %q completely erased having examined nothing. Expect this while a "+
			"restore is still loading or migrations have not yet run; if it persists, this is not "+
			"the database it is supposed to be", tenant)
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
	// A redacted table is selected exactly as a direct one is — it has a tenant column,
	// which is why an exemption could never have covered it — and differs only in what
	// the statement built around this predicate does with the rows it finds.
	case ClassDirect, ClassRedacted:
		if e.Column == "" {
			return "", nil, fmt.Errorf("%s is %s but carries no tenant column", t, e.Class)
		}
		return fmt.Sprintf("%s = ?", rdb.QuoteIdentifier(e.Column)), []any{tenant}, nil

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
			// A single-column link whose value is NULL references nothing, and
			// `IN (subquery)` already excludes it. For a COMPOSITE link the row
			// tuple is NULL if any column is, so a partially-NULL row is excluded
			// too — which matches the foreign key's own MATCH SIMPLE semantics,
			// under which such a row is not considered to reference the parent and
			// does not block its deletion either. The two agree, which is what
			// keeps the sweep from tripping over a row it declined to match.
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
		out[i] = rdb.QuoteIdentifier(n)
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
