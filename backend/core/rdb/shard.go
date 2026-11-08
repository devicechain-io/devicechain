// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package rdb

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"

	"github.com/devicechain-io/dc-microservice/core"
	"gorm.io/gorm"
)

// ErrNoShard is what a routed query fails with when the tenant's shard group has no database
// registered in this process. It is fail-closed on purpose: shard group 0 is a real database, so
// answering "group 0" for a group this process does not know would read or write another
// tenant set's rows.
var ErrNoShard = errors.New("rdb: the tenant's shard group has no database registered in this process; " +
	"refusing to route the statement elsewhere (fail-closed)")

// ErrWrongShard is what a statement fails with when it reaches a database that is not the one
// its tenant lives in: a tenant-b statement on the home handle, or a system-context write of a
// tenant-b row to a handle that is not b's group.
var ErrWrongShard = errors.New("rdb: this statement's tenant does not live in the database it reached " +
	"(fail-closed; the handle was obtained for a different tenant or without one)")

// ShardResolver answers which shard group owns a tenant. core/partition.Router is the production
// implementation; the interface keeps this package free of the placement contract's wire format.
type ShardResolver interface {
	// ShardGroup returns the shard group of tenant, or an error when it cannot say (for example
	// no placement has been loaded yet). An error must never be turned into a default group.
	ShardGroup(tenant string) (int, error)
}

// shardSet is the immutable routing state; it is swapped whole so a reader never sees a resolver
// from one configuration and databases from another.
type shardSet struct {
	resolver ShardResolver
	groups   map[int]*gorm.DB // shard group >= 1; group 0 is the manager's own Database
}

// shardPluginName keys the per-handle placement record in gorm's Config.Plugins.
const shardPluginName = "dc:shard"

// shardGuardCreate is the create-chain name of the placement guard; the column-array insert
// bypasses the chain and calls checkPlacement itself when it finds this name registered.
const shardGuardCreate = "dc:shard_guard_create"

// shardPlugin records, on a handle, which shard group that handle IS and how to resolve a
// tenant. Every handle knows its own group, so a statement can be checked against the database
// it actually reached rather than the one DB(ctx) meant to hand out.
type shardPlugin struct {
	group    int
	resolver ShardResolver
}

func (p *shardPlugin) Name() string              { return shardPluginName }
func (p *shardPlugin) Initialize(*gorm.DB) error { return nil }

func placementOf(db *gorm.DB) *shardPlugin {
	p, _ := db.Config.Plugins[shardPluginName].(*shardPlugin)
	return p
}

// checkPlacement refuses a tenant-scoped statement that reached the wrong database. tenants are
// the tenant ids named by the rows being written (may be empty).
//
// 🔴 A SYSTEM CONTEXT IS NOT A WAY AROUND ROUTING. The tenant-scope callback skips a system
// context entirely (no predicate, no tenant required), so on its own it would let a system
// context write a tenant-b row to any handle. Under a system context only the home handle
// accepts a tenant-scoped model, and only for rows that resolve to group 0; a shard handle
// refuses it outright.
func checkPlacement(db *gorm.DB, tenants []string) error {
	p := placementOf(db)
	if p == nil {
		return nil
	}
	if core.IsSystemContext(db.Statement.Context) {
		if p.group != 0 {
			return fmt.Errorf("%w: a system context may not touch a tenant-scoped table on shard group %d", ErrWrongShard, p.group)
		}
		for _, tn := range tenants {
			if tn == "" {
				continue
			}
			g, err := p.resolver.ShardGroup(tn)
			if err != nil {
				return fmt.Errorf("rdb: shard group of tenant %q: %w", tn, err)
			}
			if g != 0 {
				return fmt.Errorf("%w: tenant %q lives in group %d, this is group 0", ErrWrongShard, tn, g)
			}
		}
		return nil
	}
	tenant, ok := core.TenantFromContext(db.Statement.Context)
	if !ok {
		return nil // the tenant-scope callback refuses this with ErrNoTenant
	}
	g, err := p.resolver.ShardGroup(tenant)
	if err != nil {
		return fmt.Errorf("rdb: shard group of tenant %q: %w", tenant, err)
	}
	if g != p.group {
		return fmt.Errorf("%w: tenant %q lives in group %d, this is group %d", ErrWrongShard, tenant, g, p.group)
	}
	return nil
}

// shardGuard is the placement callback registered on every handle once sharding is enabled.
func shardGuard(db *gorm.DB) { guardPlacement(db, false) }

// shardGuardRow is shardGuard for the Row() chain. A handle carrying an error makes gorm's row
// callback skip, Row() then finds no *sql.Row and returns nil, and Scan on that panics. So here
// a refusal swaps the statement's pool for a refusing one instead: the row callback still runs
// and yields a *sql.Row that carries the refusal.
func shardGuardRow(db *gorm.DB) { guardPlacement(db, true) }

func guardPlacement(db *gorm.DB, row bool) {
	if db.Error != nil || placementOf(db) == nil {
		return
	}
	if _, refused := db.Statement.ConnPool.(refusingPool); refused {
		return // already refused, with the more specific error, at the pool
	}
	field := tenantField(db)
	if field == nil || (db.Statement.Schema != nil && db.Statement.Schema.Name == auditSchemaName) {
		// Not tenant-scoped, or the audit journal, which is written on the handle its mutation
		// ran on and so is always in step with it.
		return
	}
	tenants := destTenants(db.Statement.Dest, field.Name)
	// A map update that assigns the tenant column ("tenant_id" or "TenantId") moves rows between
	// tenants; the value it assigns is a tenant this statement writes.
	if assign, ok := db.Statement.Dest.(map[string]interface{}); ok {
		for k, v := range assign {
			if k != field.Name && k != field.DBName {
				continue
			}
			tn, isStr := v.(string)
			if !isStr {
				_ = db.AddError(fmt.Errorf("%w: the tenant assigned by this update cannot be determined", ErrWrongShard))
				return
			}
			tenants = append(tenants, tn)
		}
	}
	if m := db.Statement.Model; m != nil {
		tenants = append(tenants, destTenants(m, field.Name)...)
	}
	if err := checkPlacement(db, tenants); err != nil {
		if row {
			// The statement may be shared with a chained handle (a transaction's), so the pool is
			// put back by dc:shard_restore_row once gorm:row has read through the refusing one.
			db.Statement.Settings.Store(rowPoolKey, db.Statement.ConnPool)
			db.Statement.ConnPool = refusingPool{err: err}
			return
		}
		_ = db.AddError(err)
	}
}

// rowPoolKey holds the pool a refused Row() statement had before the guard swapped it.
const rowPoolKey = "dc:shard_row_pool"

// restoreRowPool undoes shardGuardRow's swap after the row has been read.
func restoreRowPool(db *gorm.DB) {
	if pool, ok := db.Statement.Settings.LoadAndDelete(rowPoolKey); ok {
		db.Statement.ConnPool = pool.(gorm.ConnPool)
	}
}

func registerShardGuard(db *gorm.DB) error {
	for _, register := range []func() error{
		func() error {
			return db.Callback().Row().After("gorm:row").Register("dc:shard_restore_row", restoreRowPool)
		},
		func() error {
			return db.Callback().Query().Before("gorm:query").Register("dc:shard_guard_query", shardGuard)
		},
		func() error {
			return db.Callback().Row().Before("gorm:row").Register("dc:shard_guard_row", shardGuardRow)
		},
		func() error {
			return db.Callback().Update().Before("gorm:update").Register("dc:shard_guard_update", shardGuard)
		},
		func() error {
			return db.Callback().Delete().Before("gorm:delete").Register("dc:shard_guard_delete", shardGuard)
		},
		func() error {
			return db.Callback().Create().Before("gorm:create").Register(shardGuardCreate, shardGuard)
		},
	} {
		if err := register(); err != nil {
			return err
		}
	}
	return nil
}

// EnableSharding turns on tenant -> shard-group routing for DB and the list API: a statement
// issued under a tenant context goes to that tenant's shard group's database.
//
// 🔴 OPT-IN, AND OFF BY DEFAULT. Until this is called the manager is exactly what it was: DB
// returns Database. Shard group 0 is ALWAYS the manager's own Database. An EMPTY groups map is
// refused: S = 1 never routes through the router, so there is nothing for it to do and a no-op
// that looked enabled would only hide a missing configuration.
//
// 🔴 ALL OR NOTHING. Every entry is validated before any handle is touched, and a second call is
// refused (callbacks are registered on live handles, which is a startup-time act). Handles must
// be distinct, must not be the manager's own Database, and must be freshly opened (they have not
// been through registerCallbacks).
//
// 🔴 EACH HANDLE GETS THE FULL CALLBACK CHAIN AND A PLACEMENT GUARD, THE HOME HANDLE INCLUDED.
// The guard knows which group its handle is and refuses (ErrWrongShard) a tenant-scoped
// statement whose tenant lives elsewhere, however the handle was reached: a transaction opened
// without a tenant and then given one, or a system context (which the tenant-scope callback
// skips) writing a tenant's row. Under a system context only the home handle accepts a
// tenant-scoped model, for rows in group 0.
//
// 🔴 TENANT-LESS STATEMENTS GO TO THE HOME DATABASE (group 0), where the tenant-scope callback
// refuses them on a tenant-scoped model (core.ErrNoTenant) unless the context is a system one.
//
// KNOWN GAPS (documented, not closed here): an update that assigns the tenant column through a
// raw SET clause or an expression is not inspected; PrepareStmt handles are not supported
// (the owned config never sets it, and a refused handle shares its base's statement cache).
//
// PRECONDITIONS BEFORE ANY SERVICE ENABLES THIS (none is done by this package):
//   - Fence and audit state are PER DATABASE. A shard's erasure fence reads that shard's own
//     purged_tenants, which no purge writes yet, and its audit journal is the shard's own. Until
//     purge plants fences per shard, a purged tenant is not fenced on its shard.
//   - Shard handles should be opened by the manager from configuration through the same path as
//     home (pgx dialector, pool sizing, retry); that waits on the shard DSN configuration.
//   - Per-shard migrations and lifecycle.
func (rdb *RdbManager) EnableSharding(resolver ShardResolver, groups map[int]*gorm.DB) error {
	if resolver == nil {
		return errors.New("rdb: sharding needs a resolver")
	}
	if len(groups) == 0 {
		return errors.New("rdb: sharding needs at least one shard group beyond group 0; with S = 1 do not enable it")
	}
	if rdb.Database == nil {
		return errors.New("rdb: the manager has no Database yet; enable sharding after Initialize")
	}
	if rdb.shards.Load() != nil {
		return errors.New("rdb: sharding is already enabled; it can be enabled once")
	}
	// Handles are told apart by their callback registry, not their pointer: a Session of the home
	// handle is a different *gorm.DB but shares home's callbacks and plugins, and registering a
	// shard's guard on it would install that guard on home itself.
	seen := map[any]int{rdb.Database.Callback(): 0}
	cp := make(map[int]*gorm.DB, len(groups))
	for g, db := range groups {
		switch {
		case g < 1:
			return fmt.Errorf("rdb: shard group %d cannot be registered: group 0 is the manager's own Database", g)
		case db == nil:
			return fmt.Errorf("rdb: shard group %d has a nil database", g)
		}
		if other, dup := seen[db.Callback()]; dup {
			if other == 0 {
				return fmt.Errorf("rdb: shard group %d is the manager's own Database (or a session of it)", g)
			}
			return fmt.Errorf("rdb: shard groups %d and %d share one handle", other, g)
		}
		seen[db.Callback()] = g
		cp[g] = db
	}
	for g, db := range cp {
		if err := registerCallbacks(db); err != nil {
			return fmt.Errorf("rdb: shard group %d: %w", g, err)
		}
		if err := db.Use(&shardPlugin{group: g, resolver: resolver}); err != nil {
			return fmt.Errorf("rdb: shard group %d: %w", g, err)
		}
		if err := registerShardGuard(db); err != nil {
			return fmt.Errorf("rdb: shard group %d: %w", g, err)
		}
	}
	if err := rdb.Database.Use(&shardPlugin{group: 0, resolver: resolver}); err != nil {
		return err
	}
	if err := registerShardGuard(rdb.Database); err != nil {
		return err
	}
	rdb.shards.Store(&shardSet{resolver: resolver, groups: cp})
	return nil
}

// ShardingEnabled reports whether tenant routing is on.
func (rdb *RdbManager) ShardingEnabled() bool { return rdb.shards.Load() != nil }

// routed picks the database for ctx. Without sharding it is Database, unchanged.
func (rdb *RdbManager) routed(ctx context.Context) *gorm.DB {
	set := rdb.shards.Load()
	if set == nil {
		return rdb.Database
	}
	tenant, ok := core.TenantFromContext(ctx)
	if !ok {
		return rdb.Database
	}
	g, err := set.resolver.ShardGroup(tenant)
	if err != nil {
		return failedDB(rdb.Database, fmt.Errorf("rdb: shard group of tenant %q: %w", tenant, err))
	}
	if g == 0 {
		return rdb.Database
	}
	db, ok := set.groups[g]
	if !ok {
		return failedDB(rdb.Database, fmt.Errorf("%w (tenant %q, group %d)", ErrNoShard, tenant, g))
	}
	return db
}

// refusingPool is a gorm.ConnPool on which nothing can start: no transaction, no statement, no
// prepared statement. It exists so a refused handle cannot touch ANY real connection, which a
// handle that merely carried an error would (gorm's Transaction begins on the pool before it
// looks at the error, and never rolls back).
type refusingPool struct{ err error }

func (p refusingPool) PrepareContext(context.Context, string) (*sql.Stmt, error) { return nil, p.err }
func (p refusingPool) ExecContext(context.Context, string, ...interface{}) (sql.Result, error) {
	return nil, p.err
}
func (p refusingPool) QueryContext(context.Context, string, ...interface{}) (*sql.Rows, error) {
	return nil, p.err
}
func (p refusingPool) BeginTx(context.Context, *sql.TxOptions) (gorm.ConnPool, error) {
	return nil, p.err
}

// QueryRowContext must return a *sql.Row, which cannot be built with an error by hand; one read
// from a database that can never connect carries one, so Scan returns it instead of panicking.
// The database is built per call so the row carries p.err itself, not a generic refusal; it never
// opened a connection, so closing it straight away costs nothing.
func (p refusingPool) QueryRowContext(ctx context.Context, q string, args ...interface{}) *sql.Row {
	db := sql.OpenDB(refusingConnector{err: p.err})
	defer db.Close()
	return db.QueryRowContext(ctx, q, args...)
}

type refusingConnector struct{ err error }

func (c refusingConnector) Connect(context.Context) (driver.Conn, error) { return nil, c.err }
func (c refusingConnector) Driver() driver.Driver                        { return refusingDriver{err: c.err} }

type refusingDriver struct{ err error }

func (d refusingDriver) Open(string) (driver.Conn, error) { return nil, d.err }

// failedDB is a handle on which every statement fails with err and which holds no real
// connection pool. A Row() read reports err too: the refusing pool builds its *sql.Row from a
// connector that fails with err.
func failedDB(base *gorm.DB, err error) *gorm.DB {
	pool := refusingPool{err: err}
	// Where(...) first: it gives the chain its OWN Statement. Session alone hands back the base
	// handle's Statement, and setting its pool would swap the pool under every user of the base.
	tx := base.Session(&gorm.Session{NewDB: true}).Where("1 = 1")
	tx.Config.ConnPool = pool // Session copied the Config, so this is this chain's own
	tx.Statement.ConnPool = pool
	// No AddError, deliberately: a handle that already carries an error makes Row() return nil
	// (its callbacks skip), and Scan on that panics. Failing at the pool instead makes every
	// entry point, Row included, return the error.
	return tx
}
