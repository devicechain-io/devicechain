// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package rdb

import (
	"context"
	"fmt"
	"hash/fnv"
	"strings"

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

	// Short-circuit for returning all.
	if pag.PageSize < 1 {
		return result, SearchResultsPagination{
			PageStart:    1,
			PageEnd:      total,
			TotalRecords: total,
		}
	}

	last := pag.PageNumber * pag.PageSize
	if total < last {
		last = total
	}

	srpag := SearchResultsPagination{
		PageStart:    (pag.PageNumber-1)*pag.PageSize + 1,
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
	mtable := strings.ReplaceAll(fmt.Sprintf("%s_%s", rdb.Microservice.FunctionalArea, "migrations"), "-", "_")
	options := &gormigrate.Options{
		TableName:                 mtable,
		IDColumnName:              "id",
		IDColumnSize:              255,
		UseTransaction:            false,
		ValidateUnknownMigrations: false,
	}
	m := gormigrate.New(rdb.Database, options, rdb.Migrations)

	// Serialize migrations across concurrently-rolling pods with a Postgres
	// session-level advisory lock (methodology §10.3). During a rolling update old
	// and new pods run side by side and each calls Migrate() at startup; without a
	// lock they would race on DDL. The lock is keyed by this service's migration
	// table, so different services never block each other — only replicas of the
	// same service serialize. Whichever pod loses the race waits, then finds the
	// migrations already recorded by gormigrate and no-ops. (This pairs with the
	// expand/contract discipline that keeps old and new schema mutually readable
	// for the rollout window.)
	if err := rdb.withMigrationLock(ctx, advisoryLockKey(mtable), m.Migrate); err != nil {
		return err
	}

	return nil
}

// advisoryLockKey derives a stable bigint key for pg_advisory_lock from the
// migration table name, so the lock namespace is per-service and deterministic
// across pods and restarts.
func advisoryLockKey(name string) int64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(name))
	return int64(h.Sum64())
}

// withMigrationLock holds a Postgres session-level advisory lock for the duration
// of fn, so only one pod migrates at a time. It pins a single pooled connection
// for the lock's lifetime (a session lock is bound to the connection that took
// it) and always releases it, even though fn's own statements run on other pooled
// connections — advisory locks are instance-global, so they still serialize.
func (rdb *RdbManager) withMigrationLock(ctx context.Context, key int64, fn func() error) error {
	sqldb, err := rdb.Database.DB()
	if err != nil {
		return err
	}
	conn, err := sqldb.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()

	if _, err := conn.ExecContext(ctx, "SELECT pg_advisory_lock($1)", key); err != nil {
		return fmt.Errorf("acquire migration advisory lock: %w", err)
	}
	// Release on the same session before the connection returns to the pool, so a
	// reused connection does not carry a stale lock.
	defer func() {
		if _, err := conn.ExecContext(ctx, "SELECT pg_advisory_unlock($1)", key); err != nil {
			log.Warn().Err(err).Msg("Failed to release migration advisory lock; it clears when the session ends.")
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
