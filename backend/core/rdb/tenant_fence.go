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
// area's data for a tenant has been reclaimed, consulted in the same transaction
// as every tenant-scoped write.
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
// inside the writing transaction, so it cannot be stale, cannot fail open, and cannot
// be skipped by a call site — the check is a global GORM callback, exactly like the
// tenant-scope predicate beside it.
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
// hook later. Two things follow, and both are the point. The answer is the one the write
// itself will be committed against, so it cannot be stale the way a cached remote lookup
// is; and the read is a Query, which neither this callback nor the audit journal is
// registered on, so it cannot re-enter the chain it is part of.
//
// What it does not cover, said plainly rather than left to be discovered: a statement that
// never builds a gorm schema. Raw Exec, and Create against a bare Table(...) with a map,
// write rows without any callback running. The sweep itself is raw Exec, which is why it
// still works — but it means the fence is a property of the ORM path, not of the database.
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
	ctx := db.Statement.Context
	if ctx == nil {
		ctx = context.Background()
	}
	session := db.Session(&gorm.Session{NewDB: true, Context: ctx})
	var standing PurgedTenant
	err := session.Model(&PurgedTenant{}).
		Where("completed_at IS NULL").
		Where("token IN ?", tokens).
		Limit(1).Take(&standing).Error
	switch {
	case err == nil:
		_ = db.AddError(fmt.Errorf("%w (tenant %q)", ErrTenantPurged, standing.Token))
	case errors.Is(err, gorm.ErrRecordNotFound), errors.Is(err, sql.ErrNoRows):
		// No fence stands for any tenant in this statement. This is the ordinary answer
		// and the only one that lets the write proceed.
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
// 🔴 IT LOOKS IN TWO PLACES BECAUSE THERE ARE TWO SPELLINGS, and missing the second one
// would leave the fence silently off for the area that needs it most. Most models embed
// rdb.TenantScoped and carry TenantId, and for those the tenant is in the context — the
// scoping callback injects it. event-processing's projections carry a plain `Tenant`
// composite-PK column and no embed, deliberately (see its baseline_snapshot.go), so the
// scoping callbacks do not apply to them at all and no tenant need be in their context:
// they are written from the resolved-event stream by ON CONFLICT ... DO UPDATE upserts,
// which is verified resurrection vector 4. Their tenant is in the ROW.
//
// Both sources are consulted for every statement rather than one being chosen by shape.
// The context alone would miss the row-carried spelling; the rows alone would miss an
// Updates(map) that names no tenant column, and would depend on this callback running
// after the one that stamps TenantId, which is a registration-order assumption no test
// would notice breaking.
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
// model has none and is therefore not fenceable. The order matches the two spellings
// tenantpurge classifies by, so a table this fence ignores is a table the sweep also
// leaves alone.
func tenantFieldOf(db *gorm.DB) string {
	if !ensureSchema(db) {
		return ""
	}
	for _, name := range []string{tenantFieldName, plainTenantFieldName} {
		if _, ok := db.Statement.Schema.FieldsByName[name]; ok {
			return name
		}
	}
	return ""
}

// plainTenantFieldName is event-processing's spelling of the tenant column.
const plainTenantFieldName = "Tenant"

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
			for _, key := range []string{"tenant_id", "tenant"} {
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
