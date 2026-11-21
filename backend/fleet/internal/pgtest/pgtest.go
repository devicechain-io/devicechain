// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build fleetpg

package pgtest

import (
	"fmt"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"gorm.io/gorm/schema"
)

var seq atomic.Int64

// Env is one test's private database on the real server.
type Env struct {
	// Gorm is the primary connection to the test's database.
	Gorm *gorm.DB
	// DSN reaches the test's database (as the DSN's own user).
	DSN string
	// AdminDSN reaches the server's maintenance database, for role and database administration.
	AdminDSN string
	// Name is the test database's name.
	Name string
}

// Open creates a fresh, uniquely named database on the server named by DC_FLEET_PG_DSN and
// returns a connection to it. It is dropped (with force) when the test ends. A missing or
// unreachable server FAILS the test.
func Open(t testing.TB) *Env {
	t.Helper()
	admin := MustDSN(t, os.Getenv)
	adminDB, err := gorm.Open(postgres.Open(admin), &gorm.Config{Logger: logger.Discard})
	if err != nil {
		t.Fatalf("pgtest: cannot reach the server named by %s: %v", EnvDSN, err)
	}
	sqlAdmin, err := adminDB.DB()
	if err != nil {
		t.Fatalf("pgtest: %v", err)
	}
	if err := sqlAdmin.Ping(); err != nil {
		t.Fatalf("pgtest: the server named by %s did not answer: %v", EnvDSN, err)
	}

	name := fmt.Sprintf("fleetpg_%d_%d", time.Now().UnixNano()%1_000_000_000, seq.Add(1))
	if err := adminDB.Exec(`CREATE DATABASE ` + name).Error; err != nil {
		t.Fatalf("pgtest: create database: %v", err)
	}
	env := &Env{AdminDSN: admin, Name: name, DSN: withDatabase(t, admin, name)}
	t.Cleanup(func() {
		_ = adminDB.Exec(`DROP DATABASE IF EXISTS ` + name + ` WITH (FORCE)`).Error
		_ = sqlAdmin.Close()
	})
	env.Gorm = env.Connect(t)
	return env
}

// Connect opens a NEW, independent connection pool to the test's database. Concurrency tests
// use it so that two "managers" hold two sessions, not one pool.
func (e *Env) Connect(t testing.TB) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(postgres.Open(e.DSN), &gorm.Config{Logger: logger.Discard})
	if err != nil {
		t.Fatalf("pgtest: connect to %s: %v", e.Name, err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("pgtest: %v", err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	return db
}

// OpenArea is Open plus a schema named area, pinned as the connection's search_path and with
// gorm's table prefix set, which is the shape of a service's own connection in production
// (core/rdb): unqualified DDL and probes land in, and look at, that schema.
func OpenArea(t testing.TB, area string) *Env {
	t.Helper()
	env := Open(t)
	if err := env.Gorm.Exec(`CREATE SCHEMA "` + area + `"`).Error; err != nil {
		t.Fatalf("pgtest: create schema %s: %v", area, err)
	}
	env.Gorm = env.ConnectArea(t, area)
	return env
}

// ConnectArea opens a new independent connection pool pinned to the area's schema.
func (e *Env) ConnectArea(t testing.TB, area string) *gorm.DB {
	t.Helper()
	u, err := url.Parse(e.DSN)
	if err != nil {
		t.Fatalf("pgtest: %v", err)
	}
	q := u.Query()
	q.Set("search_path", area+",public")
	u.RawQuery = q.Encode()
	db, err := gorm.Open(postgres.Open(u.String()), &gorm.Config{
		Logger:         logger.Discard,
		NamingStrategy: schema.NamingStrategy{TablePrefix: area + "."},
	})
	if err != nil {
		t.Fatalf("pgtest: connect to %s/%s: %v", e.Name, area, err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("pgtest: %v", err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	return db
}

// withDatabase returns dsn with its database name replaced. Only URL-form DSNs are supported,
// which is what CI and the documented command use.
func withDatabase(t testing.TB, dsn, name string) string {
	t.Helper()
	if !strings.HasPrefix(dsn, "postgres://") && !strings.HasPrefix(dsn, "postgresql://") {
		t.Fatalf("pgtest: %s must be a postgres:// URL, got %q", EnvDSN, dsn)
	}
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatalf("pgtest: %s is not a URL: %v", EnvDSN, err)
	}
	u.Path = "/" + name
	return u.String()
}
