// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package rdb

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/devicechain-io/dc-microservice/config"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/rs/zerolog/log"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

// Guest is a connection to an instance database this service does not own.
//
// # Why this is not an RdbManager
//
// RdbManager is the handle a service holds on ITS OWN storage, and everything it does
// on the way up says so: it CREATEs the instance database and a schema named after the
// caller's functional area, pins search_path to that schema, prefixes every model with
// it, auto-migrates the audit journal into it, registers the tenant-scope, token-grammar
// and audit callbacks against the caller's models, and runs the caller's migration chain.
// Every one of those is correct for the database a service owns and WRONG for one it
// merely visits: pointing an RdbManager at the telemetry cluster would create a
// `user-management` schema and an `audit_events` table inside it, then replay
// user-management's migrations there. None of that would fail loudly. It would just be
// permanently true.
//
// A Guest does none of it. It resolves the same sslMode, builds the same escaped DSN,
// retries the same way and sizes the pool with the same rules — because those are
// properties of the HOP, not of ownership — and then stops. It creates nothing, migrates
// nothing, and registers no callbacks.
//
// # 🔴 It carries no tenant scoping, so do not run models on it
//
// The absence of RegisterTenantScoping is the whole reason this type is dangerous in the
// wrong hands: a tenant-scoped model queried through a Guest would return every tenant's
// rows with no error, because the fail-closed callback that would have rejected it was
// never installed. That is acceptable here only because a Guest's caller is not doing
// data access — it is doing catalog-driven work against a database whose models it does
// not have, naming the tenant in its own predicate. If you find yourself reaching for a
// model type with a Guest handle, the thing you actually want is an API call to the
// service that owns the database.
//
// The one caller today is the ADR-077 tenant purge, which erases a deleted tenant from
// the telemetry cluster (backend/services/user-management/purge). It is the shape that
// forced this type: the erasure is organised by STORE, and the telemetry store is a
// database no service that runs the coordinator owns.
//
// # Connecting is LAZY, and that is an availability decision
//
// Initialize resolves and validates the configuration — so a bad sslMode is still a
// startup verdict rather than a surprise during a purge — but opens no socket. The
// connection is made on first use, by Connect.
//
// Eager connection is right for a service's own database, where nothing the service does
// works without it. It is wrong here: user-management is the login path for the entire
// instance, and a Guest is consulted only while a tenant is being erased. Connecting at
// startup would make every login in the instance depend on the availability of a cluster
// that has nothing to do with logging in. A caller that needs the connection is already
// on a retry loop of its own (the purge coordinator's ticker), which is why Connect makes
// ONE attempt and reports the failure rather than retrying inside itself.
type Guest struct {
	// name distinguishes this connection in logs and in the lifecycle component
	// name. A service may hold more than one Guest, so it cannot be derived.
	name string

	// microservice supplies the instance id, which is the database name on EVERY
	// cluster — each one hosts one database per instance, and the areas inside it are
	// schemas. That is why a guest needs no separate database name.
	microservice *core.Microservice

	instanceConfig     config.DatastoreConfiguration
	microserviceConfig config.MicroserviceDatastoreConfiguration

	// mu guards the fields below, which are written on first use rather than at
	// startup. Connect may be called from any goroutine that needs the connection.
	mu       sync.Mutex
	resolved *PostgresConfig
	database *gorm.DB
}

// NewGuest builds a connection to an instance database owned by another service.
//
// name is a short label for the database being visited ("tsdb"), used in the lifecycle
// component name and in log lines; it is not persisted anywhere and does not name a
// schema. icfg is the instance-level datastore block for the cluster (e.g.
// InstanceConfiguration.Persistence.Tsdb), which every pod mounts regardless of which
// areas it runs; cfg supplies this service's own pool sizing for the connection.
func NewGuest(ms *core.Microservice, name string, icfg config.DatastoreConfiguration,
	cfg config.MicroserviceDatastoreConfiguration) *Guest {
	return &Guest{
		name:               name,
		microservice:       ms,
		instanceConfig:     icfg,
		microserviceConfig: cfg,
	}
}

// Connect opens the connection if it is not already open, and is safe to call before
// every use. A caller must call it before DB.
//
// It makes a single attempt. See the type doc for why the retry belongs to the caller.
func (g *Guest) Connect(ctx context.Context) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.database != nil {
		return nil
	}
	if g.resolved == nil {
		return fmt.Errorf("guest connection %q was not initialized", g.name)
	}
	pg := g.resolved
	dsn, err := g.computeGuestDsn(pg)
	if err != nil {
		return err
	}

	log.Info().Str("guest", g.name).Str("username", pg.Username).
		Str("hostname", pg.Hostname).Int32("port", pg.Port).Str("ssl_mode", pg.SslMode).
		Str("database", g.microservice.InstanceId).
		Msg("Connecting to an instance database owned by another service.")

	// No NamingStrategy: a Guest resolves no models, and a TablePrefix here would
	// silently qualify a raw-SQL caller's table into this service's own schema name in
	// a database where that schema does not exist.
	//
	// DisableAutomaticPing, then ping explicitly with the caller's context. gorm's own
	// ping is `Ping()`, which takes no context — so on a cluster that BLACKHOLES rather
	// than refuses (a node gone, a NetworkPolicy dropping packets) it blocks for the OS
	// TCP timeout, measured at ~127s here. That happens on the purge coordinator's single
	// goroutine, so it starves every other purging tenant in the pass, and because the
	// coordinator stops by cancelling a context and joining, it stalls pod SHUTDOWN for
	// the same two minutes. A context-less wait is not something a caller can get out of.
	db, err := gorm.Open(postgres.New(postgres.Config{DSN: dsn}),
		&gorm.Config{DisableAutomaticPing: true})
	if err != nil {
		return fmt.Errorf("connecting to the %q database: %w", g.name, err)
	}
	sqldb, err := db.DB()
	if err != nil {
		return err
	}
	if err := sqldb.PingContext(ctx); err != nil {
		// Close the pool we are abandoning; otherwise every failed pass leaks one.
		_ = sqldb.Close()
		return fmt.Errorf("connecting to the %q database: %w", g.name, err)
	}
	if g.microserviceConfig.SqlDebug {
		db = db.Debug()
	}

	// Same sizing policy as an owned connection, through the same function: a guest's
	// pool is used by the same kind of caller and must not become a second,
	// differently-behaved knob.
	if err := applyPoolSizing(db, g.microserviceConfig, log.Info().Str("guest", g.name)); err != nil {
		return err
	}
	g.database = db
	return nil
}

// guestConnectTimeoutSeconds bounds a single connection attempt. See computeGuestDsn.
const guestConnectTimeoutSeconds = "5"

// computeGuestDsn builds the connection string, and the two ways it differs from an owned
// connection's are the two ways an RdbManager pointed at another service's cluster would
// be wrong.
//
// The DATABASE is the instance id, on every cluster — each one hosts one database per
// instance and the functional areas inside it are schemas — so a guest needs no database
// name of its own and must not invent one.
//
// The SEARCH_PATH is public, not this service's functional area. An owned connection pins
// its own schema so unqualified statements resolve there; a guest has no schema in this
// database, so pinning the same value would name one that does not exist. Everything a
// guest's caller issues is schema-qualified, and public stays on the path so extension
// functions installed there remain resolvable. Pinning it rather than leaving the server
// default of `"$user", public` also means a schema sharing the connecting role's name
// cannot silently shadow anything.
//
// It resolves sslMode itself even though ExecuteInitialize already normalised it, and the
// redundancy is deliberate: this package's connection-string coverage gate holds every
// builder to the same fail-closed contract, and a builder that trusts its caller to have
// validated is one refactor away from being the builder that quietly omits sslMode. That
// is the defect the gate was written for, so being a builder like the others is worth more
// than saving the call.
func (g *Guest) computeGuestDsn(pg *PostgresConfig) (string, error) {
	sslMode, err := resolveSslMode(pg.SslMode)
	if err != nil {
		return "", fmt.Errorf("guest connection %q: %w", g.name, err)
	}
	return postgresKeywordDSN(pg.Username, pg.Password, pg.Hostname, pg.Port,
		g.microservice.InstanceId, sslMode, map[string]string{
			"search_path": "public",
			// connect_timeout bounds the DIAL, which the context above cannot: a
			// cancelled context unblocks this pod's shutdown but a pass that is merely
			// slow still has to give up on its own. Five seconds is generous for a
			// same-cluster hop and short against the coordinator's 60s tick.
			"connect_timeout": guestConnectTimeoutSeconds,
		}), nil
}

// DB returns a gorm handle bound to ctx. Connect must have succeeded first.
//
// This is the same signature RdbManager.DB carries so that a caller able to work against
// either can accept a narrow interface rather than a concrete type. The context binding
// still matters even with no callbacks registered — it is what cancels a statement when
// the caller's context is cancelled.
func (g *Guest) DB(ctx context.Context) *gorm.DB {
	g.mu.Lock()
	db := g.database
	g.mu.Unlock()
	return db.WithContext(ctx)
}

// Initialize resolves and validates the configuration. It opens no connection — see the
// type doc.
//
// 🔴 A GUEST HAS NO LIFECYCLE MANAGER, AND THAT IS THE FIX FOR A REAL DEFECT RATHER THAN
// a simplification. It first copied RdbManager's eight-method lifecycle surface, whose
// state machine requires Stopped before Terminate. Nothing ever started a guest — there is
// nothing to start, since connecting is lazy and ExecuteStart/ExecuteStop were both
// `return nil` — so the shutdown path's Terminate returned "attempting to terminate
// component that is not stopped", and its caller returned early, leaving the broker and
// the service's own database un-terminated.
//
// Note what let that ship: the tests called the INNER ExecuteInitialize/ExecuteTerminate
// callbacks directly, so they drove right past the state machine that rejected the
// sequence. Two methods with no phase between them cannot be sequenced wrongly.
func (g *Guest) Initialize(context.Context) error {
	// convertToPostgresConfig rejects unknown keys and normalises sslMode to the value
	// every later reader sees, so an unusable posture is a verdict at startup rather
	// than something the first purge discovers.
	pgconf, err := convertToPostgresConfig(g.instanceConfig)
	if err != nil {
		return fmt.Errorf("guest connection %q: %w", g.name, err)
	}
	g.mu.Lock()
	g.resolved = pgconf
	g.mu.Unlock()
	return nil
}

// Close releases the pool. A guest that never connected has nothing to close, which is
// the common case: on every instance where no tenant was ever purged, the connection was
// resolved and never opened.
func (g *Guest) Close() error {
	g.mu.Lock()
	db := g.database
	g.database = nil
	g.mu.Unlock()
	if db == nil {
		return nil
	}
	sqldb, err := db.DB()
	if err != nil {
		return err
	}
	return sqldb.Close()
}

// IsMissingDatabase reports whether err is Postgres telling us the database does not
// exist (SQLSTATE 3D000, invalid_catalog_name).
//
// It is worth distinguishing from every other connection failure because it is an ANSWER
// rather than a silence: the server was reachable, the credentials were accepted, and it
// replied that there is no such database. On a cluster where the instance database is
// created by whichever service owns it, that is how a caller learns the owning area was
// never deployed here — the `ingest-only` profile ships no event-management, so nothing
// ever creates the instance database on the telemetry cluster.
//
// 🔴 It is not a proof of absence for all time. A cluster that is up but has not yet been
// restored answers 3D000 too. A caller drawing a conclusion from it should say so where
// an operator can see it, rather than treating it as routine.
func IsMissingDatabase(err error) bool {
	var pgerr *pgconn.PgError
	return errors.As(err, &pgerr) && pgerr.Code == "3D000"
}
