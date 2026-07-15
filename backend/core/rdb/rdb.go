// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package rdb

import (
	"context"
	"fmt"
	"hash/fnv"
	"strings"
	"time"

	"github.com/devicechain-io/dc-microservice/config"
	"github.com/devicechain-io/dc-microservice/core"
	gormigrate "github.com/go-gormigrate/gormigrate/v2"
	"github.com/rs/zerolog/log"
	"gorm.io/gorm"
)

// Manages lifecycle of relational database interactions.
type RdbManager struct {
	Microservice       *core.Microservice
	Database           *gorm.DB
	Migrations         []*gormigrate.Migration
	InstanceConfig     config.DatastoreConfiguration
	MicroserviceConfig config.MicroserviceDatastoreConfiguration

	lifecycle core.LifecycleManager
}

// Create a new rdb manager.
func NewRdbManager(ms *core.Microservice, callbacks core.LifecycleCallbacks,
	migrations []*gormigrate.Migration, icfg config.DatastoreConfiguration,
	cfg config.MicroserviceDatastoreConfiguration) *RdbManager {
	rdb := &RdbManager{
		Microservice:       ms,
		Migrations:         migrations,
		InstanceConfig:     icfg,
		MicroserviceConfig: cfg,
	}
	// Create lifecycle manager.
	rdbname := fmt.Sprintf("%s-%s", ms.FunctionalArea, "rdb")
	rdb.lifecycle = core.NewLifecycleManager(rdbname, rdb, callbacks)
	return rdb
}

// DB returns a *gorm.DB bound to the given context so that the tenant-scope
// callbacks (see tenant_scope.go) can read the per-request/per-message tenant
// from context. All tenant-scoped data access must go through this.
func (rdb *RdbManager) DB(ctx context.Context) *gorm.DB {
	return rdb.Database.WithContext(ctx)
}

// Query for a list of models based on filter and pagination criteria.
func (rdb *RdbManager) ListOf(ctx context.Context, mdl interface{}, filters func(db *gorm.DB) *gorm.DB, pag Pagination) (*gorm.DB, SearchResultsPagination) {
	// Sanity check page number.
	if pag.PageNumber < 1 {
		pag.PageNumber = 1
	}

	// Bind the context once (so the tenant-scope callbacks see it) and reuse it
	// for both the count and data queries; each Model(...) clones a fresh stmt.
	base := rdb.Database.WithContext(ctx)

	// Execute count query.
	count := int64(0)
	result := base.Model(mdl)
	if filters != nil {
		result = filters(result)
	}
	_ = result.Count(&count)
	total := int32(count)

	// Execute data query.
	result = base.Model(mdl)
	if filters != nil {
		result = filters(result)
	}
	result.Scopes(Paginate(pag))

	// Short-circuit for the explicit internal all-rows path (Paginate applied no
	// LIMIT). External requests never set Unbounded and so are always bounded below.
	if pag.Unbounded {
		return result, SearchResultsPagination{
			PageStart:    1,
			PageEnd:      total,
			TotalRecords: total,
		}
	}

	// Use the effective (defaulted/clamped) page size so the reported span matches
	// the LIMIT/OFFSET Paginate actually applied (ADR-029).
	size := pag.EffectivePageSize()
	last := pag.PageNumber * size
	if total < last {
		last = total
	}

	srpag := SearchResultsPagination{
		PageStart:    (pag.PageNumber-1)*size + 1,
		PageEnd:      last,
		TotalRecords: total,
	}
	return result, srpag
}

// Initialize component.
func (rdb *RdbManager) Initialize(ctx context.Context) error {
	return rdb.lifecycle.Initialize(ctx)
}

// Lifecycle callback that runs initialization logic.
func (rdb *RdbManager) ExecuteInitialize(ctx context.Context) error {
	// Make sure database exists before interacting with it.
	dbtype := rdb.InstanceConfig.Type
	if strings.HasPrefix(dbtype, "postgres") {
		err := rdb.initializePostgres(ctx)
		if err != nil {
			return err
		}
	} else if strings.HasPrefix(dbtype, "timescaledb") {
		err := rdb.initializePostgres(ctx)
		if err != nil {
			return err
		}
	} else {
		return fmt.Errorf("relational database %s not currently supported", dbtype)
	}

	// Run migrations in a database independent manner.
	mtable := MigrationTableName(rdb.Microservice.FunctionalArea)
	options := MigrationOptions(rdb.Microservice.FunctionalArea)
	// Migrations are system-level bootstrap operations that run before any tenant
	// exists. Bind a system context so the tenant-scoping Create callback (ADR-015
	// fail-closed) does not reject gormigrate's own bookkeeping INSERTs into the
	// migrations table — without this, the first row written after a tenant-scoped
	// model is migrated fails with ErrNoTenant. DDL inside the migrations is
	// unaffected (the callbacks act on row operations, not schema changes).
	migrateDB := rdb.Database.WithContext(core.WithSystemContext(ctx))
	m := gormigrate.New(migrateDB, options, rdb.Migrations)

	// Serialize migrations across concurrently-rolling pods with a Postgres
	// session-level advisory lock (methodology §10.3). During a rolling update old
	// and new pods run side by side and each calls Migrate() at startup; without a
	// lock they would race on DDL. The lock is keyed by this service's migration
	// table, so different services never block each other — only replicas of the
	// same service serialize. Whichever pod loses the race waits, then finds the
	// migrations already recorded by gormigrate and no-ops. (This pairs with the
	// expand/contract discipline that keeps old and new schema mutually readable
	// for the rollout window.)
	if err := rdb.WithAdvisoryLock(ctx, AdvisoryLockKey(mtable), m.Migrate); err != nil {
		return err
	}

	return nil
}

// MigrationTableName returns the gormigrate bookkeeping table name for a functional
// area (dashes → underscores, e.g. "device-management" → "device_management_migrations").
func MigrationTableName(area string) string {
	return strings.ReplaceAll(fmt.Sprintf("%s_%s", area, "migrations"), "-", "_")
}

// MigrationOptions returns the gormigrate options every service's migration chain
// runs with. Exported so an out-of-process equivalence harness (the migration-flatten
// check) drives the chain identically to production — same table, same
// UseTransaction:false (Timescale DDL can't run in a txn), same lenient
// ValidateUnknownMigrations (migrations may be pruned from the slice once their shape
// is dead) — so a squashed baseline is diffed against the real thing, not a lookalike.
func MigrationOptions(area string) *gormigrate.Options {
	return &gormigrate.Options{
		TableName:                 MigrationTableName(area),
		IDColumnName:              "id",
		IDColumnSize:              255,
		UseTransaction:            false,
		ValidateUnknownMigrations: false,
	}
}

// AdvisoryLockKey derives a stable bigint key for pg_advisory_lock from a name,
// so the lock namespace is deterministic across pods and restarts. The
// uint64→int64 conversion may yield a negative key; pg_advisory_lock takes the
// full signed bigint range, so that is fine and every distinct hash still maps to
// a distinct lock. Callers should namespace the name per concern (e.g. the
// migration table name, or a per-service policy-reconcile key) so unrelated
// startup work never serializes on the same lock.
func AdvisoryLockKey(name string) int64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(name))
	return int64(h.Sum64())
}

// WithAdvisoryLock holds a Postgres session-level advisory lock for the duration
// of fn, so only one pod runs it at a time (used to serialize migrations and
// startup policy reconciliation across concurrently-rolling replicas). It pins a
// single pooled connection for the lock's lifetime (a session lock is bound to the
// connection that took it) and always releases it, even though fn's own statements
// run on other pooled connections — advisory locks are instance-global, so they
// still serialize.
func (rdb *RdbManager) WithAdvisoryLock(ctx context.Context, key int64, fn func() error) error {
	sqldb, err := rdb.Database.DB()
	if err != nil {
		return err
	}
	conn, err := sqldb.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()

	// The acquire blocks until the lock is free, but on ctx (the startup context),
	// so a SIGTERM mid-wait cancels it and unwinds startup rather than hanging — a
	// peer that crashes while holding the lock drops its session, which Postgres
	// releases automatically. The wait is intentionally not time-bounded: a hard
	// timeout would spuriously fail this pod while a peer runs a legitimately slow
	// migration.
	if _, err := conn.ExecContext(ctx, "SELECT pg_advisory_lock($1)", key); err != nil {
		return fmt.Errorf("acquire advisory lock: %w", err)
	}
	// Release on the same session before the connection returns to the pool, so a
	// reused connection does not carry a stale lock. Use a fresh, short context —
	// not ctx — so the unlock still runs when ctx was cancelled during fn (e.g. a
	// SIGTERM mid-migration); otherwise the lock would linger on the pooled
	// connection until the session ends.
	defer func() {
		unlockCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := conn.ExecContext(unlockCtx, "SELECT pg_advisory_unlock($1)", key); err != nil {
			log.Warn().Err(err).Msg("Failed to release advisory lock; it clears when the session ends.")
		}
	}()

	return fn()
}

// Start component.
func (rdb *RdbManager) Start(ctx context.Context) error {
	return rdb.lifecycle.Start(ctx)
}

// Lifecycle callback that runs startup logic.
func (rdb *RdbManager) ExecuteStart(context.Context) error {
	return nil
}

// Stop component.
func (rdb *RdbManager) Stop(ctx context.Context) error {
	return rdb.lifecycle.Stop(ctx)
}

// Lifecycle callback that runs shutdown logic.
func (rdb *RdbManager) ExecuteStop(context.Context) error {
	return nil
}

// Terminate component.
func (rdb *RdbManager) Terminate(ctx context.Context) error {
	return rdb.lifecycle.Terminate(ctx)
}

// Lifecycle callback that runs termination logic.
func (rdb *RdbManager) ExecuteTerminate(context.Context) error {
	sqldb, err := rdb.Database.DB()
	if err != nil {
		return err
	}
	sqldb.Close()
	return nil
}
