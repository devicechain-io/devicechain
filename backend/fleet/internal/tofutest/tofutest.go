// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build fleetpg

// Package tofutest is the harness the runner's real-OpenTofu tests share: the tofu binary named by
// DC_FLEET_TOFU, the fixture roots, and a state schema laid out the way a provider's is at
// onboarding. Like pgtest, a missing binary FAILS the test; there is no skip.
package tofutest

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// EnvTofu names the OpenTofu binary the tests run against.
const EnvTofu = "DC_FLEET_TOFU"

// Passphrase is the throwaway state and plan encryption key the tests run with.
const Passphrase = "fleet-test-passphrase-0123456789"

// Encryption is the TF_ENCRYPTION value: a pbkdf2 key provider and an aes_gcm method, applied to
// state and plan. The fixture roots enforce encryption themselves; the key arrives only here.
const Encryption = `key_provider "pbkdf2" "k" {
  passphrase = "` + Passphrase + `"
}
method "aes_gcm" "m" {
  keys = key_provider.pbkdf2.k
}
state {
  method = method.aes_gcm.m
}
plan {
  method = method.aes_gcm.m
}
`

// Tofu returns the binary path or fails the test.
func Tofu(t testing.TB) string {
	t.Helper()
	p := os.Getenv(EnvTofu)
	if p == "" {
		t.Fatalf("tofutest: %s is not set; under the fleetpg build tag a missing OpenTofu fails the test rather than skipping it", EnvTofu)
	}
	if !filepath.IsAbs(p) {
		t.Fatalf("tofutest: %s must be an absolute path", EnvTofu)
	}
	return p
}

// Root is the path of a fixture root under tofurun/testdata/roots.
func Root(t testing.TB, name string) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatalf("tofutest: cannot locate the fixtures")
	}
	dir := filepath.Join(filepath.Dir(file), "..", "..", "tofurun", "testdata", "roots", name)
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		t.Fatalf("tofutest: no fixture root %q", name)
	}
	return dir
}

// Tree is the path of a fixture tree under tofurun/testdata (a directory holding roots and the
// modules they reference by relative path).
func Tree(t testing.TB, name string) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatalf("tofutest: cannot locate the fixtures")
	}
	dir := filepath.Join(filepath.Dir(file), "..", "..", "tofurun", "testdata", name)
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		t.Fatalf("tofutest: no fixture tree %q", name)
	}
	return dir
}

// ConnString is dsn with its user replaced: the connection string a per-run role would be given.
func ConnString(t testing.TB, dsn, user, password string) string {
	t.Helper()
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatalf("tofutest: %v", err)
	}
	u.User = url.UserPassword(user, password)
	q := u.Query()
	q.Del("search_path")
	u.RawQuery = q.Encode()
	return u.String()
}

// PrepareStateSchema lays out a provider's state schema as onboarding does: the schema, the
// states table and its unique index, all OWNED by owner (a role the per-run login roles are members
// of), with the three creation steps therefore switched off in the fixture root.
func PrepareStateSchema(t testing.TB, db *gorm.DB, schema, owner string) {
	t.Helper()
	for _, stmt := range []string{
		fmt.Sprintf(`CREATE SCHEMA %s AUTHORIZATION %s`, schema, owner),
		fmt.Sprintf(`CREATE TABLE %s.states (id SERIAL PRIMARY KEY, name TEXT, data TEXT)`, schema),
		fmt.Sprintf(`CREATE UNIQUE INDEX states_by_name ON %s.states (name)`, schema),
		fmt.Sprintf(`ALTER TABLE %s.states OWNER TO %s`, schema, owner),
		fmt.Sprintf(`ALTER SEQUENCE %s.states_id_seq OWNER TO %s`, schema, owner),
	} {
		if err := db.Exec(stmt).Error; err != nil {
			t.Fatalf("tofutest: %s: %v", stmt, err)
		}
	}
}

// DropRolesAfter registers the removal of cluster-global roles a test creates, after the test's
// database (and the objects the roles own) is gone.
func DropRolesAfter(t *testing.T, adminDSN string, roles ...string) {
	t.Helper()
	t.Cleanup(func() {
		db, err := gorm.Open(postgres.Open(adminDSN), &gorm.Config{Logger: logger.Discard})
		if err != nil {
			return
		}
		sqlDB, _ := db.DB()
		defer sqlDB.Close()
		for _, r := range roles {
			_ = db.Exec(`SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE usename = ?`, r).Error
			_ = db.Exec(fmt.Sprintf(`DROP ROLE IF EXISTS "%s"`, r)).Error
		}
	})
}
