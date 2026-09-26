// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package rdb

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"reflect"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"gorm.io/gorm"
)

// PurgedTenant is one functional area's erasure fence: a local record that this
// area's data for a tenant has been reclaimed, consulted inside the transaction of every
// tenant-scoped write, at most once per tenant per transaction (see tenantFenceCheck and
// fenceMemo).
//
// 🔴 IT EXISTS BECAUSE THE LIFECYCLE GATE IS NOT THE CORRECTNESS PATH, and the gate's
// own documentation says so. That gate resolves a tenant's state from user-management
// over a 60-second cache that fails OPEN, it is wired only onto data-plane admission
// hooks (no GraphQL mutation consults it anywhere), and it goes blind at exactly the
// moment the purge finishes: completion removes the tenant row, the governance query
// then errors, and an unresolvable tenant reads as "active". A purge that can only be
// enforced by a remote lookup which cannot answer is not enforced.
//
// This fence has none of those properties. It is a row in the area's OWN schema, read
// inside the writing transaction, so it is never staler than that transaction, cannot
// fail open, and cannot be skipped by a call site — the check is a global GORM callback,
// exactly like the tenant-scope predicate beside it.
//
// 🔑 THE EPOCH IS WHY THIS IS KEYED BY TWO COLUMNS AND NOT ONE. The token is the only
// tenant identity in the data plane and it is RELEASED when a purge completes, so the
// same token can be purged more than once over an instance's life. A fence keyed by
// token alone would be ambiguous the moment the token was reused — it could not say
// which incarnation it was fencing, and the row a successor's purge wrote would
// overwrite the evidence of its predecessor's.
//
// The fence is ACTIVE while CompletedAt is null. Stamping it is deliberately not the
// same act as deleting the row: what remains is this area's own local evidence that it
// erased that tenant at that epoch, in the schema that held the data, which is the one
// place a reader checking the claim would look.
//
// 🔴 LIFTING THE FENCE AT COMPLETION IS SAFE ONLY BECAUSE OF THE TOKEN HOLD, and that is
// the whole argument — do not shorten one without the other. A purge does not complete
// until it has been clean for the settle window AND older than the token hold, which
// defaults to the broker JWT's full lifetime. By then no session minted before the
// deletion can still authenticate, so there is no writer left for the fence to stop.
//
// This type is core-owned and migrated by core (see rdb.go), like the audit journal:
// it exists in EVERY functional area's schema with no per-service wiring, because an
// area that forgot to create it would be an area with no fence and no error.
type PurgedTenant struct {
	// Token is the tenant token whose data this area reclaimed.
	Token string `gorm:"primaryKey;size:128"`
	// Epoch is the purge cut, taken from the deleting service's clock. Together with
	// Token it identifies one purge; a token purged twice has two rows.
	Epoch time.Time `gorm:"primaryKey"`
	// PlantedAt is when this area first fenced the token. It is not the epoch: the
	// epoch is when the operator walked through the delete door, this is when the
	// sweep first reached this schema.
	PlantedAt time.Time `gorm:"not null"`
	// CompletedAt is null while the fence stands. It is stamped in the pass that
	// releases the token, never before.
	CompletedAt *time.Time
}

// FenceTable is the unqualified name GORM derives for PurgedTenant. It is stated as a
// constant because the fence is read by raw SQL from inside a write callback rather
// than through a model — a query that must not itself fire the callbacks it is part of.
const FenceTable = "purged_tenants"

// ErrTenantPurged is the refusal. It is returned to the caller as the write's error, so
// it says what happened and what it means rather than naming a table.
var ErrTenantPurged = errors.New("this tenant has been deleted and its data in this area erased; " +
	"the write was refused so it cannot resurrect any part of it")

// RegisterTenantFence installs the global GORM callbacks that refuse a write for a
// tenant this area has fenced.
//
// Create and Update only. Delete is deliberately NOT fenced: deleting a purged tenant's
// rows is exactly what should keep happening — notification-management's retention
// sweeper clears its own rows, and the sweep itself deletes — and a fence over Delete
// would stop the erasure it exists to protect. Query is not fenced either: a read
// resurrects nothing, and the residual scan that grades the purge is a read.
//
// It also swaps the handle's connection pool for one whose transactions remember a clear
// fence read (see fencePool and installFenceMemo), and it REFUSES a pool it cannot wrap
// rather than leaving the fence silently unmemoised. It must therefore be called on the
// handle every later session derives from, before any are derived — a rule it can check
// only as far as refusing a handle inside a transaction (see installFenceMemo); postgres.go
// and every test fixture keep it. Calling it again on a handle that already has the fence
// is a no-op, and logs nothing.
//
// 🔴 THE AUDIT JOURNAL'S OWN INSERT IS EXCLUDED, and skipping it is not a courtesy — it is
// what makes the "deletes are not fenced" sentence above TRUE. The journal's callback is
// registered After("gorm:create"), which gorm sorts AFTER the commit callback, so the
// audit row for a mutation is inserted on the pool once that mutation has already
// committed. Fencing it would therefore refuse a DELETE that had happened: the sweeper
// would be handed ErrTenantPurged for rows it had just removed, and every ordinary
// delete-by-a-still-valid-token would report a failure over a completed write.
//
// The exclusion is safe on its own terms. The journal records what happened; it cannot
// resurrect anything, because the mutation it describes was either refused one hook
// earlier (create, update) or was a delete that should have gone through. Its rows for a
// purged tenant are not swept but RETAINED, with the two columns that can name a person
// emptied — see the redaction registry in core/tenantpurge.
func RegisterTenantFence(db *gorm.DB) error {
	if err := installFenceMemo(db); err != nil {
		return err
	}
	// gorm keeps one callback per name, so registering again would change nothing but the
	// log: every repeat logs a "duplicated callback" warning. Registered once, the
	// callbacks stay registered, and a second call returns here silently.
	if db.Callback().Create().Get("dc:tenant_fence_create") != nil {
		return nil
	}
	for _, register := range []func() error{
		func() error {
			return db.Callback().Create().Before("gorm:create").Register("dc:tenant_fence_create", tenantFenceCheck)
		},
		func() error {
			return db.Callback().Update().Before("gorm:update").Register("dc:tenant_fence_update", tenantFenceCheck)
		},
	} {
		if err := register(); err != nil {
			return err
		}
	}
	return nil
}

// tenantFenceCheck refuses a write whose tenant this area has fenced.
//
// 🔑 IT READS THE FENCE ON A FRESH SESSION SHARING THIS STATEMENT'S CONNECTION, which
// inside a transaction is that transaction — the same device the audit journal uses one
// hook later. Two things follow, and both are the point. The answer is one read on the
// connection the write commits on, no earlier than the transaction's first write for that
// tenant, so it cannot be stale the way a cached remote lookup is, which can predate the
// transaction entirely; and the read is a Query, which neither this callback nor the audit
// journal is registered on, so it cannot re-enter the chain it is part of.
//
// 🔑 INSIDE A TRANSACTION IT READS THE FENCE ONCE PER TENANT, NOT ONCE PER STATEMENT. The
// first write for a tenant reads it. If no fence stands, that answer is remembered on the
// transaction itself (fenceMemo), and later writes for the same tenant in the same
// transaction do not read it again. A statement naming several tenants reads every one not
// yet proven, in one query. Only "no fence stands" is remembered: a refusal is already the
// statement's error, and a read that failed is never an answer, so the fail-closed branch
// below is exactly as it was. A gorm call outside an explicit transaction is its own
// transaction and so reads every time. And a transaction that writes the fence table
// itself forgets everything it had proven, so it reads again after that: "at most once per
// tenant per transaction, unless the transaction writes the fence".
//
// 🔴 THIS ADDS NO NEW KIND OF EXPOSURE, AND THE ARGUMENT IS WORTH HAVING IN FULL. Without
// the memo a transaction could already read the fence a moment before a purge planted it
// and commit its write a moment after; that is what reading inside the transaction means
// under READ COMMITTED, the isolation every transaction here runs at. The purge never
// relied on the fence closing that window. The fence is planted in its own committed
// transaction before each sweep, sweeps repeat, a sweep that deletes rows restarts the
// settle window, and a purge completes only after its residual scan has been clean for the
// whole of it. That is precisely the write the sweep and the residual scan exist to catch.
// Remembering the first answer for the rest of the transaction widens that same window
// from "one statement" to "one transaction"; it does not create a different one.
//
// What does change is quantity, and it is stated rather than left to be found. Before, a
// transaction that straddled the plant was usually refused at its next write and rolled
// back whole; now one whose first write for the tenant preceded the plant commits, and a
// later sweep collects its rows — more rows swept, more settle restarts, never a row the
// sweep cannot see. Both before and after, the argument assumes no transaction stays open
// longer than the purge's settle window: one that did could commit after completion, and
// nothing catches that, memo or not. Nothing enforces that bound today (no idle-in-
// transaction or statement timeout is configured); the memo adds rows to such a
// transaction, it does not create one. (On Postgres the first fence read also takes a
// share lock on the fence table until the transaction ends, so no other session can drop
// or alter it under a memoised transaction and turn a remembered answer into one that
// should have failed closed.)
//
// What the memo must never do is outlive its transaction, which is why it is a field of
// the transaction's own connection wrapper rather than an entry in a registry.
//
// What it does not cover, said plainly rather than left to be discovered: a statement that
// never builds a gorm schema. THIS fence cannot classify one — statementTenants has no
// schema to read a tenant out of — so it does not act on it, and the sweep itself is raw
// Exec, which is why the sweep still works. The fence is therefore a property of the ORM
// path, not of the database.
//
// The OUTCOME for those statements is no longer uniform, and the difference is worth being
// exact about. Raw Exec runs on gorm's Raw processor, where no callback of ours is
// registered at all, so it writes its rows unchallenged. A Create against a bare Table(...)
// with a map does reach a callback: the tenant-scope predicate one hook earlier now refuses
// it outright with ErrUnscopedStatement (see RegisterTenantScoping), so under a tenant
// context it never gets as far as this fence. What stays true is the sentence about THIS
// callback — it still cannot classify that shape, and it is still not the thing stopping it.
//
// Going through gorm rather than issuing raw SQL on the connection pool is not
// convenience. The table reference comes from the NamingStrategy, which is where the
// functional-area schema prefix lives, so the fence resolves to THIS area's table by the
// same rule every other model does; and the placeholders come from the dialect, so the
// callback works on Postgres and on the in-memory database the tests drive it with.
func tenantFenceCheck(db *gorm.DB) {
	if db.Error != nil {
		return
	}
	if fenceExempt(db) {
		return
	}
	tokens := statementTenants(db)
	if len(tokens) == 0 {
		return
	}
	memo := memoOf(db.Statement.ConnPool)
	ask, gen := tokens, uint64(0)
	if memo != nil {
		if ask, gen = memo.unproven(tokens); len(ask) == 0 {
			// Every tenant this statement names was read clear earlier in THIS transaction.
			return
		}
	}
	ctx := db.Statement.Context
	if ctx == nil {
		ctx = context.Background()
	}
	// The marker tells the transaction wrapper that this statement READS the fence, so it
	// is not mistaken for one that might have written it (see fenceTx.forgetIfFence).
	session := db.Session(&gorm.Session{NewDB: true, Context: context.WithValue(ctx, fenceReadKey{}, true)})
	var standing PurgedTenant
	err := session.Model(&PurgedTenant{}).
		Where("completed_at IS NULL").
		Where("token IN ?", ask).
		Limit(1).Take(&standing).Error
	switch {
	case err == nil:
		_ = db.AddError(fmt.Errorf("%w (tenant %q)", ErrTenantPurged, standing.Token))
	case errors.Is(err, gorm.ErrRecordNotFound), errors.Is(err, sql.ErrNoRows):
		// No fence stands for any tenant in this statement. This is the ordinary answer,
		// the only one that lets the write proceed, and the only one remembered.
		if memo != nil {
			memo.prove(ask, gen)
		}
	default:
		// 🔴 FAIL CLOSED. An unreadable fence is not an absent one, and the whole reason
		// this check is local is that "I could not ask" and "the answer is no" are the
		// same sentence to a remote gate. The fence table lives in the same schema, on
		// the same connection, inside the same transaction as the write being checked —
		// so a query that cannot answer means the write cannot succeed either, and
		// refusing costs nothing that was going to work.
		_ = db.AddError(fmt.Errorf("reading the erasure fence: %w", err))
	}
}

// FenceExempt is implemented by a model whose writes RECORD something rather than being
// something, so the erasure fence must not refuse them.
//
// 🔴 THE BAR IS "CANNOT RESURRECT", NOT "IS INCONVENIENT TO FENCE". A model qualifies only
// if writing it for a purged tenant adds nothing that could be inherited by a successor at
// that token: a record OF a failure, not a row the platform would go on to act on. Two
// implement it, and both are core-owned:
//
//   - the audit journal, whose row is written AFTER the mutation it describes has already
//     committed (gorm sorts its After hook past the commit callback), so fencing it hands
//     the sweeper a refusal for rows it has already deleted;
//   - the dead-letter store, whose row says work did NOT happen. Refusing it during a purge
//     would delete the evidence that a tenant's last alarms went nowhere, and would report
//     the refusal as a database outage.
//
// Both are swept by the ordinary tenant purge like any other tenant-bearing table, so
// exempting them from the FENCE does not exempt them from the erasure.
type FenceExempt interface {
	FenceExempt() bool
}

// fenceExempt reports whether this statement's model opts out of the fence.
func fenceExempt(db *gorm.DB) bool {
	if db.Statement.Schema == nil {
		return false
	}
	// The audit journal is matched on the gorm schema NAME as well, the same handle its
	// own recursion guard uses, because its rows are written through a bare model that
	// never reaches an interface check on some paths.
	if db.Statement.Schema.Name == auditSchemaName {
		return true
	}
	if m, ok := db.Statement.Model.(FenceExempt); ok {
		return m.FenceExempt()
	}
	if d, ok := db.Statement.Dest.(FenceExempt); ok {
		return d.FenceExempt()
	}
	return false
}

// statementTenants returns every tenant token this statement could write a row for.
//
// 🔴 IT LOOKS IN TWO PLACES BECAUSE A STATEMENT CAN NAME ITS TENANT IN EITHER. Most
// statements carry it in the CONTEXT and the scope callback injects the predicate from
// there. But a create also carries it on the ROWS, and the two are not interchangeable:
//
//   - A statement under a SYSTEM context has no context tenant by design, and its rows
//     are then the only thing naming one. event-processing's projections — plain `Tenant`
//     composite-PK columns, written from the resolved-event stream by
//     ON CONFLICT ... DO UPDATE upserts, verified resurrection vector 4 — are read that
//     way by the engine's four cross-tenant startup loads.
//   - An Updates(map) names no tenant column at all, so the rows give nothing and the
//     context is all there is.
//
// So both sources are consulted for every statement rather than one being chosen by
// shape. Reading the rows alone would also depend on this callback running after the one
// that stamps the tenant, which is a registration-order assumption no test would notice
// breaking.
//
// 🔴 THIS COMMENT USED TO SAY event-processing's tables were outside the scoping
// callbacks entirely and needed no tenant in their context. That was true when it was
// written and is now false in both halves: the callback recognises both spellings, those
// tables are scoped, and a missing tenant is core.ErrNoTenant. The old text survived the
// change that falsified it by forty lines and was caught in review — which is the case
// for saying what a comment is FOR rather than what the world around it happens to be.
func statementTenants(db *gorm.DB) []string {
	field := tenantFieldOf(db)
	if field == "" {
		return nil
	}
	seen := map[string]struct{}{}
	out := []string{}
	add := func(v string) {
		if v == "" {
			return
		}
		if _, dup := seen[v]; dup {
			return
		}
		seen[v] = struct{}{}
		out = append(out, v)
	}
	if ctx := db.Statement.Context; ctx != nil && !core.IsSystemContext(ctx) {
		if tenant, ok := core.TenantFromContext(ctx); ok {
			add(tenant)
		}
	}
	for _, v := range destTenants(db.Statement.Dest, field) {
		add(v)
	}
	return out
}

// tenantFieldOf returns the Go field name carrying this model's tenant, or "" when the
// model has none and is therefore not fenceable.
//
// It asks tenantField, which is the single authority on what "tenant-scoped" means, so
// a table this fence ignores is exactly a table the scope callback leaves unscoped and
// the sweep leaves alone. That used to be three separate lists, and they disagreed.
func tenantFieldOf(db *gorm.DB) string {
	field := tenantField(db)
	if field == nil {
		return ""
	}
	return field.Name
}

// destTenants pulls tenant values out of the statement's destination, which is a struct,
// a pointer to one, a slice or array of either (a batch insert), or a map of columns
// (Updates with a map). Anything else contributes nothing.
func destTenants(dest any, field string) []string {
	if dest == nil {
		return nil
	}
	var out []string
	var walk func(reflect.Value)
	walk = func(v reflect.Value) {
		for v.Kind() == reflect.Ptr || v.Kind() == reflect.Interface {
			if v.IsNil() {
				return
			}
			v = v.Elem()
		}
		switch v.Kind() {
		case reflect.Slice, reflect.Array:
			for i := 0; i < v.Len(); i++ {
				walk(v.Index(i))
			}
		case reflect.Struct:
			f := v.FieldByName(field)
			if f.IsValid() && f.Kind() == reflect.String {
				out = append(out, f.String())
			}
		case reflect.Map:
			// Updates(map[string]any{...}) is keyed by COLUMN name, not field name.
			for _, key := range TenantColumnNames {
				mv := v.MapIndex(reflect.ValueOf(key))
				if !mv.IsValid() {
					continue
				}
				for mv.Kind() == reflect.Interface || mv.Kind() == reflect.Ptr {
					if mv.IsNil() {
						break
					}
					mv = mv.Elem()
				}
				if mv.Kind() == reflect.String {
					out = append(out, mv.String())
				}
			}
		}
	}
	walk(reflect.ValueOf(dest))
	return out
}
