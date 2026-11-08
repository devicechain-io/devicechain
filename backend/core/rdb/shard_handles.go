// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package rdb

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/rs/zerolog/log"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

// shardPingTimeout bounds one health probe of one shard.
const shardPingTimeout = 5 * time.Second

// openShardFunc opens one shard database from its DSN. It is a field on the manager so a test
// can stand in for Postgres; production uses openPostgresShard.
type openShardFunc func(dsn, functionalArea string) (*gorm.DB, error)

// openPostgresShard opens a shard exactly as the home database is opened: the same pgx
// dialector and the same owned gorm configuration.
func openPostgresShard(dsn, functionalArea string) (*gorm.DB, error) {
	dialector, err := newPostgresDialector(postgres.Config{DSN: dsn})
	if err != nil {
		return nil, err
	}
	return gorm.Open(dialector, ownedGormConfig(functionalArea))
}

// openShards opens, sizes and health-checks one handle per configured shard, retrying each as
// the home connection is retried. It is all or nothing: if any shard cannot be opened, every
// handle opened so far is closed and the error is returned, so startup stops rather than run
// with a placement that names a database it cannot reach.
//
// The handles are returned UNREGISTERED: no callback chain, no placement guard. They become
// routing targets only through EnableConfiguredSharding, which hands them to EnableSharding,
// the single place that wires a handle in.
func (rdb *RdbManager) openShards(ctx context.Context, pgconf *PostgresConfig) (map[int]*gorm.DB, error) {
	if len(pgconf.Shards) == 0 {
		return nil, nil
	}
	open := rdb.openShard
	if open == nil {
		open = openPostgresShard
	}
	opened := make(map[int]*gorm.DB, len(pgconf.Shards))
	fail := func(err error) (map[int]*gorm.DB, error) {
		closeHandles(opened)
		return nil, err
	}
	for _, sc := range pgconf.Shards {
		dsn, err := rdb.computePostgresDsn(sc.postgresConfigOf())
		if err != nil {
			return fail(fmt.Errorf("shard group %d: %w", sc.Group, err))
		}
		var db *gorm.DB
		if err := core.RetryInfraConnect(ctx, fmt.Sprintf("postgres shard group %d", sc.Group), func(ctx context.Context) error {
			var oerr error
			if db, oerr = open(dsn, rdb.Microservice.FunctionalArea); oerr != nil {
				return oerr
			}
			if oerr = pingShard(ctx, db); oerr != nil {
				closeHandle(db)
				db = nil
			}
			return oerr
		}); err != nil {
			return fail(fmt.Errorf("shard group %d (%s:%d): %w", sc.Group, sc.Hostname, sc.Port, err))
		}
		if rdb.MicroserviceConfig.SqlDebug {
			db = db.Debug()
		}
		if err := applyPoolSizing(db, rdb.MicroserviceConfig,
			log.Info().Int("shard_group", sc.Group)); err != nil {
			closeHandle(db)
			return fail(fmt.Errorf("shard group %d: %w", sc.Group, err))
		}
		opened[sc.Group] = db
	}
	return opened, nil
}

func pingShard(ctx context.Context, db *gorm.DB) error {
	sqldb, err := db.DB()
	if err != nil {
		return err
	}
	pctx, cancel := context.WithTimeout(ctx, shardPingTimeout)
	defer cancel()
	return sqldb.PingContext(pctx)
}

func closeHandle(db *gorm.DB) error {
	sqldb, err := db.DB()
	if err != nil {
		return err
	}
	return sqldb.Close()
}

func closeHandles(m map[int]*gorm.DB) error {
	groups := make([]int, 0, len(m))
	for g := range m {
		groups = append(groups, g)
	}
	sort.Ints(groups)
	var errs []error
	for _, g := range groups {
		if err := closeHandle(m[g]); err != nil {
			errs = append(errs, fmt.Errorf("shard group %d: %w", g, err))
		}
	}
	return errors.Join(errs...)
}

// ConfiguredShardGroups lists, in order, the shard groups (beyond 0) whose databases the
// manager opened from configuration. Empty when sharding is unconfigured.
func (rdb *RdbManager) ConfiguredShardGroups() []int {
	groups := make([]int, 0, len(rdb.shardHandles))
	for g := range rdb.shardHandles {
		groups = append(groups, g)
	}
	sort.Ints(groups)
	return groups
}

// EnableConfiguredSharding turns routing on over the shard databases the manager opened from
// its configuration. It fails when none are configured (S = 1 has nothing to route) and
// otherwise defers every other check to EnableSharding.
func (rdb *RdbManager) EnableConfiguredSharding(resolver ShardResolver) error {
	if len(rdb.shardHandles) == 0 {
		return errors.New("rdb: no shard databases are configured; sharding stays off")
	}
	return rdb.EnableSharding(resolver, rdb.shardHandles)
}

// CheckShards probes every configured shard database and returns a joined error naming each
// group that did not answer. It is nil when sharding is unconfigured. A probe is bounded
// (shardPingTimeout), so a blackholed shard cannot hang a readiness check.
func (rdb *RdbManager) CheckShards(ctx context.Context) error {
	var errs []error
	for _, g := range rdb.ConfiguredShardGroups() {
		if err := pingShard(ctx, rdb.shardHandles[g]); err != nil {
			errs = append(errs, fmt.Errorf("shard group %d: %w", g, err))
		}
	}
	return errors.Join(errs...)
}
