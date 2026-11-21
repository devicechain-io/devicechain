// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package pgtest is the real-Postgres test harness for the fleet module.
//
// The default unit tests AutoMigrate on SQLite, which cannot test row locks, partial indexes,
// triggers or roles, and those are exactly what the fleet schema and the writer-exclusion
// protocol are made of. Tests that need the real server live behind the build tag `fleetpg`
// and read DC_FLEET_PG_DSN:
//
//	cd backend/fleet && DC_FLEET_PG_DSN='postgres://postgres:postgres@127.0.0.1:5432/postgres?sslmode=disable' \
//	    go test -tags fleetpg -count=1 ./...
//
// 🔴 UNDER THE TAG, A MISSING OR UNREACHABLE DSN FAILS THE TEST. It never skips: a harness that
// skips when its database is absent turns "the exclusion protocol was never run" into a green
// build. Without the tag the files are not compiled, so `go test ./...`, the repository sweep
// and the Windows lanes are unaffected. CI runs the tagged suite on both supported Postgres
// majors (hack/migration-diff.sh verify) and requires a non-zero count of passed TestFleetPG*
// tests (hack/check-fleetpg-count.sh), so a mistyped tag cannot go green over nothing.
package pgtest

import "errors"

// EnvDSN is the environment variable the harness reads.
const EnvDSN = "DC_FLEET_PG_DSN"

// ErrNoDSN is what a missing DSN is: an error the caller must fail on, never skip on.
var ErrNoDSN = errors.New("pgtest: " + EnvDSN + " is not set; under the fleetpg build tag a missing database fails the test rather than skipping it")

// ResolveDSN returns the DSN from getenv, or ErrNoDSN. It is the whole decision the harness
// makes about a missing database, kept in one pure function so that it is tested without one.
func ResolveDSN(getenv func(string) string) (string, error) {
	if dsn := getenv(EnvDSN); dsn != "" {
		return dsn, nil
	}
	return "", ErrNoDSN
}

// Failer is the part of testing.TB the harness uses to refuse. It deliberately has no Skip:
// the harness has no way to skip, by construction.
type Failer interface {
	Helper()
	Fatalf(format string, args ...any)
}

// MustDSN returns the DSN or fails t.
func MustDSN(t Failer, getenv func(string) string) string {
	t.Helper()
	dsn, err := ResolveDSN(getenv)
	if err != nil {
		t.Fatalf("%v", err)
	}
	return dsn
}
