// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package rdb

import (
	"context"
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

// EnableSharding turns on tenant -> shard-group routing for DB and the list API: a statement
// issued under a tenant context goes to that tenant's shard group's database.
//
// 🔴 OPT-IN, AND OFF BY DEFAULT. Until this is called the manager is exactly what it was: DB
// returns Database. Shard group 0 is ALWAYS the manager's own Database (S = 1, today's behaviour,
// is the degenerate case with no extra handles), so a service that enables routing with an empty
// groups map still behaves as before for every tenant the placement puts on group 0.
//
// 🔴 EACH EXTRA HANDLE GETS THE FULL CALLBACK CHAIN. A shard database that reached a statement
// without tenant scoping, the token grammar, the audit journal and the erasure fence would be an
// unguarded back door, so the chain is installed here rather than trusted to the caller. Pass
// freshly opened handles that have not been through registerCallbacks.
//
// 🔴 TENANT-LESS STATEMENTS GO TO THE HOME DATABASE (group 0). The tenant-scope callback already
// refuses a tenant-less statement on a tenant-scoped model (core.ErrNoTenant), so what is left is
// control data and migrations, which live with the home database.
//
// Call it during startup, before queries run on the manager.
func (rdb *RdbManager) EnableSharding(resolver ShardResolver, groups map[int]*gorm.DB) error {
	if resolver == nil {
		return errors.New("rdb: sharding needs a resolver")
	}
	cp := make(map[int]*gorm.DB, len(groups))
	for g, db := range groups {
		if g < 1 {
			return fmt.Errorf("rdb: shard group %d cannot be registered: group 0 is the manager's own Database", g)
		}
		if db == nil {
			return fmt.Errorf("rdb: shard group %d has a nil database", g)
		}
		if err := registerCallbacks(db); err != nil {
			return fmt.Errorf("rdb: shard group %d: %w", g, err)
		}
		cp[g] = db
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

// failedDB is a handle on which every statement fails with err. Where(...) takes a fresh
// instance, which is what lets AddError stick to the chain the caller continues.
func failedDB(base *gorm.DB, err error) *gorm.DB {
	tx := base.Session(&gorm.Session{NewDB: true}).Where("1 = 1")
	_ = tx.AddError(err)
	return tx
}
