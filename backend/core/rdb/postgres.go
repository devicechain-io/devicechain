// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package rdb

import (
	"context"
	"fmt"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	pgx "github.com/jackc/pgx/v5"
	"github.com/rs/zerolog/log"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/schema"
)

// Compute non-database connection URL for querying/creating database.
//
// This carries the SAME sslMode as the per-database DSN, which it previously did
// not: the URL form omitted sslmode entirely, so libpq/pgx applied its own
// default of `prefer` while the DSN form hard-coded `disable`. Two different TLS
// postures on the same hop, decided by which function happened to build the
// string — and the weaker one was the one used for the connection that CREATES
// databases (ADR-020 A2.1).
func (rdb *RdbManager) computePostgresRootUrl(pgconfig *PostgresConfig) (string, error) {
	sslMode, err := resolveSslMode(pgconfig.SslMode)
	if err != nil {
		return "", err
	}
	config := rdb.InstanceConfig
	hostname := fmt.Sprintf("%v", config.Configuration["hostname"])
	port := fmt.Sprintf("%v", config.Configuration["port"])
	username := fmt.Sprintf("%v", config.Configuration["username"])
	password := fmt.Sprintf("%v", config.Configuration["password"])
	return fmt.Sprintf("postgres://%s:%s@%s:%s/postgres?sslmode=%s",
		username, password, hostname, port, sslMode), nil
}

// Assure that database is created before connecting to it.
func (rdb *RdbManager) assurePostgresDatabase(pgconfig *PostgresConfig) error {
	url, err := rdb.computePostgresRootUrl(pgconfig)
	if err != nil {
		return err
	}
	// Log the connection coordinates but never the URL — it embeds the password (C1).
	log.Info().Str("database", rdb.Microservice.InstanceId).
		Str("host", pgconfig.Hostname).Int32("port", pgconfig.Port).
		Msg("Verifying that instance database exists.")
	conn, err := pgx.Connect(context.Background(), url)
	if err != nil {
		return err
	}
	defer conn.Close(context.Background())

	// List all databases
	found := false
	result := conn.PgConn().ExecParams(context.Background(), "SELECT datname FROM pg_database WHERE datistemplate = false", [][]byte{}, nil, nil, nil)
	for result.NextRow() {
		currdb := string(result.Values()[0])
		if rdb.Microservice.InstanceId == currdb {
			log.Info().Msg("Found existing instance database.")
			found = true
		}
	}
	_, err = result.Close()
	if err != nil {
		return err
	}

	if !found {
		// Create instance database.
		log.Info().Msg("Database was not found. Creating...")
		result := conn.PgConn().ExecParams(context.Background(), fmt.Sprintf("CREATE DATABASE %s", rdb.Microservice.InstanceId),
			[][]byte{}, nil, nil, nil)
		_, err := result.Close()
		if err != nil {
			return err
		}
		log.Info().Str("database", rdb.Microservice.InstanceId).Msg("Successfully created instance database.")
	}

	return nil
}

// Compute connection URL for the instance database, used to create the
// functional-area schema.
//
// This is the THIRD connection builder on this hop, and it was the one missed
// when sslMode was introduced — found by a vacuity audit, not by reading the
// diff. All three now resolve the same configured value, which is the whole
// point: a TLS posture that applies to two of three connections is not a
// posture, it is a coin toss whose outcome depends on which function ran.
func (rdb *RdbManager) computePostgresInstanceDatabaseUrl(pg *PostgresConfig) (string, error) {
	sslMode, err := resolveSslMode(pg.SslMode)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("postgres://%s:%s@%s:%d/%s?sslmode=%s",
		pg.Username, pg.Password, pg.Hostname, pg.Port, rdb.Microservice.InstanceId, sslMode), nil
}

// Assure that functional area schema is created before connecting to it.
func (rdb *RdbManager) assurePostgresSchema(pgconfig *PostgresConfig) error {
	log.Info().Str("schema", rdb.Microservice.FunctionalArea).Msg("Verifying that schema exists.")
	url, err := rdb.computePostgresInstanceDatabaseUrl(pgconfig)
	if err != nil {
		return err
	}
	conn, err := pgx.Connect(context.Background(), url)
	if err != nil {
		return err
	}
	defer conn.Close(context.Background())

	// List all databases
	found := false
	result := conn.PgConn().ExecParams(context.Background(), "SELECT schema_name FROM information_schema.schemata", [][]byte{}, nil, nil, nil)
	for result.NextRow() {
		currsch := string(result.Values()[0])
		if rdb.Microservice.FunctionalArea == currsch {
			log.Info().Msg("Found existing schema for functional area.")
			found = true
		}
	}
	_, err = result.Close()
	if err != nil {
		return err
	}

	if !found {
		// Create functional area schema.
		log.Info().Msg("Schema was not found. Creating...")
		result := conn.PgConn().ExecParams(context.Background(), fmt.Sprintf("CREATE SCHEMA \"%s\"", rdb.Microservice.FunctionalArea),
			[][]byte{}, nil, nil, nil)
		_, err := result.Close()
		if err != nil {
			return err
		}
		log.Info().Str("schema", rdb.Microservice.FunctionalArea).Msg("Successfully created schema.")
	}

	return nil
}

// Compute DSN for connecting to database. Returns an error rather than a
// best-effort DSN when sslMode is unusable: a bad TLS posture must stop the
// service at startup, not be silently downgraded into one that happens to
// connect.
func (rdb *RdbManager) computePostgresDsn(pg *PostgresConfig) (string, error) {
	sslMode, err := resolveSslMode(pg.SslMode)
	if err != nil {
		return "", err
	}
	// Pin search_path to this service's functional-area schema (then public) so
	// that EVERY statement on the connection — not just GORM model operations,
	// which are schema-qualified by the NamingStrategy TablePrefix — resolves into
	// the right schema. Without this, gormigrate's bookkeeping table and any
	// raw-SQL migration (e.g. an `ALTER TABLE roles`) fall back to the default
	// search_path and land in / look up `public`, where the service's tables do
	// not exist — so a migration against a fresh database fails with
	// "relation does not exist". `public` stays on the path so extension functions
	// installed there (TimescaleDB's create_hypertable, etc.) remain resolvable.
	dsn := fmt.Sprintf("user=%s password=%s host=%s dbname=%s port=%d sslmode=%s search_path=%s,public",
		pg.Username, pg.Password, pg.Hostname, rdb.Microservice.InstanceId, pg.Port, sslMode, rdb.Microservice.FunctionalArea)
	// Never log the password (C1): emit only the non-sensitive connection
	// coordinates. PostgresConfig.Password is treated as redacted everywhere.
	// sslMode is logged deliberately: "is this link encrypted" is the kind of
	// question that should be answerable from a pod's logs.
	log.Info().Str("username", pg.Username).Str("hostname", pg.Hostname).
		Int32("port", pg.Port).Str("ssl_mode", sslMode).Msg("Initializing database connectivity")
	return dsn, nil
}

// Boostrap a postgres database/schema.
func (rdb *RdbManager) bootstrapPostgres(pgconf *PostgresConfig) error {
	// Verify/create instance database.
	err := rdb.assurePostgresDatabase(pgconf)
	if err != nil {
		return err
	}

	// Verify/create functional area schema.
	err = rdb.assurePostgresSchema(pgconf)
	if err != nil {
		return err
	}

	return nil
}

// Initialize a postgres database.
func (rdb *RdbManager) initializePostgres(ctx context.Context) error {
	pgconf, err := convertToPostgresConfig(rdb.InstanceConfig)
	if err != nil {
		return err
	}

	// Bootstrap the postgres database/schema if needed, retrying so a few seconds
	// of Postgres lag on a cluster restart degrades into a retry rather than a
	// crash-loop (A6). bootstrapPostgres only creates what is missing, so a
	// re-attempt after a partial failure is safe.
	if err := core.RetryInfraConnect(ctx, "postgres", func(context.Context) error {
		return rdb.bootstrapPostgres(pgconf)
	}); err != nil {
		return err
	}

	// Connect to database using params from instance configuration (also retried).
	// Note this is deliberately NOT inside the retry: an invalid sslMode is a
	// config verdict, and retrying it just turns a clear startup error into a
	// crash-loop that reads like the database is unreachable.
	dsn, err := rdb.computePostgresDsn(pgconf)
	if err != nil {
		return err
	}
	var db *gorm.DB
	if err := core.RetryInfraConnect(ctx, "postgres", func(context.Context) error {
		var oerr error
		db, oerr = gorm.Open(postgres.New(postgres.Config{
			DSN: dsn,
		}), &gorm.Config{
			NamingStrategy: schema.NamingStrategy{
				TablePrefix:   fmt.Sprintf("%s.", rdb.Microservice.FunctionalArea),
				SingularTable: false,
			}})
		return oerr
	}); err != nil {
		return err
	}

	rdb.Database = db
	if rdb.MicroserviceConfig.SqlDebug {
		rdb.Database = rdb.Database.Debug()
	}

	// Register tenant-scoping callbacks so row-level isolation is enforced for
	// every tenant-scoped model, un-skippable at the call site.
	if err := RegisterTenantScoping(rdb.Database); err != nil {
		return err
	}

	// Register the token-grammar callbacks so every entity token is validated
	// fail-closed at create/update (ADR-042 P2): a token carrying a NATS/MQTT
	// metacharacter can never reach storage, and thus never a subject/topic.
	if err := RegisterTokenGrammar(rdb.Database); err != nil {
		return err
	}

	// Register the audit-journal callbacks and ensure the journal table exists, so
	// every entity mutation in this service is recorded by construction (ADR-019).
	// The table is core-owned and auto-migrated here rather than via each service's
	// migration list, so no per-service wiring is required.
	if err := rdb.Database.AutoMigrate(&AuditEvent{}); err != nil {
		return err
	}
	if err := RegisterAuditJournal(rdb.Database); err != nil {
		return err
	}

	sqldb, err := rdb.Database.DB()
	if err != nil {
		return err
	}

	// Size the connection pool (ADR-022 review E4). The pool must comfortably
	// exceed the owning service's per-service worker count (currently 5
	// persistence/projection workers) plus the GraphQL server's request
	// concurrency, otherwise workers and GraphQL contend for the same handles and
	// per-pod throughput is capped. We deliberately do NOT validate
	// worker_count <= max_connections here: the worker counts live in the service
	// processors, not in this shared package.
	//
	// Pool sizing now comes from the per-microservice config (MaxOpen/MaxIdle).
	// The legacy instance-level PostgresConfig.MaxConnections (default 5) is
	// subsumed by this and no longer drives the pool; it is left on the struct
	// for any other reader but does not size the pool.
	maxOpen, maxIdle := poolSizing(rdb.MicroserviceConfig.MaxOpenConnections,
		rdb.MicroserviceConfig.MaxIdleConnections)
	sqldb.SetMaxOpenConns(maxOpen)
	sqldb.SetMaxIdleConns(maxIdle)
	sqldb.SetConnMaxLifetime(time.Hour)
	log.Info().
		Int("max_open_connections", maxOpen).
		Int("max_idle_connections", maxIdle).
		Msg("Created connection pool.")

	return nil
}

const (
	// defaultMaxOpenConnections is the per-pod cap on open database connections
	// when the service does not configure one. It is intentionally larger than
	// the historical value (5) so the per-service worker pool (5) and the GraphQL
	// server do not contend for the same handles.
	defaultMaxOpenConnections = 20
	// minMaxIdleConnections floors the idle pool so a brief lull does not tear
	// down every connection only to immediately reopen them (connection thrash).
	minMaxIdleConnections = 2
)

// poolSizing resolves the configured open/idle connection counts into the values
// actually applied to the pool, substituting defaults for unset (zero/negative)
// values. A zero/unset value falls back to a default rather than being passed
// through to database/sql, where 0 open means unlimited and 0 idle means "no
// idle connections" — both of which we want to avoid. MaxIdle is kept close to
// MaxOpen (not <<) so idle connections are not constantly closed and reopened,
// and is clamped to never exceed MaxOpen.
func poolSizing(cfgOpen, cfgIdle int) (open, idle int) {
	open = cfgOpen
	if open <= 0 {
		open = defaultMaxOpenConnections
	}

	idle = cfgIdle
	if idle <= 0 {
		// Default idle to half of open (floored) so it tracks open without
		// pinning the full pool open, but never below a small floor.
		idle = open / 2
		if idle < minMaxIdleConnections {
			idle = minMaxIdleConnections
		}
	}
	// Idle can never exceed open; database/sql would silently clamp it anyway.
	if idle > open {
		idle = open
	}
	return open, idle
}
