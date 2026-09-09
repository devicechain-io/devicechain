// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package secrets

import (
	"context"
	"errors"
	"fmt"

	"github.com/devicechain-io/dc-microservice/core"
	"gorm.io/gorm"
)

// SelfTestResult is the OUTCOME of the startup root-key check, and it exists
// because the check has two non-failing outcomes that mean very different things.
//
// A boolean "the self-test did not return an error" would collapse them: a store
// with nothing in it yet cannot prove anything about the key, and recording that
// as a pass would make the check report success in exactly the situation where it
// examined nothing. The result is therefore a value the caller logs, so an
// operator reading a startup line can tell "checked against stored ciphertext"
// from "there was nothing to check against".
type SelfTestResult string

const (
	// SelfTestVerified means a stored envelope was actually opened with the
	// configured key: the key in use matches the key the stored secrets were
	// sealed under.
	SelfTestVerified SelfTestResult = "verified-against-stored-ciphertext"
	// SelfTestNoStoredSecrets means the store holds no live secret, so the key
	// could not be checked against anything. Startup proceeds — a fresh instance
	// has to be able to start — but this is NOT a passed check, and Verified
	// reports false for it.
	SelfTestNoStoredSecrets SelfTestResult = "not-verified-no-stored-secrets"
)

// Verified reports whether the result came from an actual decrypt. It is false
// for every outcome that examined no ciphertext, so a caller cannot accidentally
// treat "nothing to check" as "checked and correct".
func (r SelfTestResult) Verified() bool { return r == SelfTestVerified }

// SelfTest checks that kp can open the secrets already stored in db, so a service
// started with a well-formed but WRONG instance root key fails once, immediately,
// and with a message naming the cause.
//
// Without it a wrong key is invisible until first use: every Exists/hasSecret
// surface reads rows and answers "present", and only Resolve fails — so the
// platform advertises credentials it cannot open and the operator sees a healthy
// instance whose secret-backed features fail one at a time, far from the cause.
// This is the "never return a plausible value" rule applied to configuration.
//
// It checks the KEK, not a value: it unwraps the stored DEK through the
// KeyProvider interface and stops there. Nothing is decrypted with that DEK, so
// no secret cleartext is produced, and the DEK itself is zeroed before return.
// Going through the interface (passing the row's own recorded KEK version rather
// than assuming the current one) is what makes this correct for a future
// multi-version provider: online KEK rotation is not implemented today, so a
// failed unwrap is unambiguously a wrong key, and the day a provider holds
// several generations the same call unwraps an older row with the older key
// instead of needing to be revisited here.
//
// Failure modes are kept distinguishable in the error text, because an operator
// must never read "wrong root key" when the truth is "the database was down":
//
//   - the database cannot be reached, or the table cannot be read: terminal, and
//     the message names the DATABASE. It is not skipped. A service that cannot
//     reach its database cannot serve anyway, so failing closed costs nothing and
//     removes the silently-skipped-check shape entirely.
//   - the secrets table does not exist: terminal, and the message says the
//     migrations have not run. It is deliberately NOT folded into "nothing
//     stored", which would make this check vacuous on exactly the installs where
//     it is wired wrongly.
//   - a stored envelope does not open: terminal, and the message names the root
//     key.
//
// The read runs under core.WithSystemContext: it is a startup operation with no
// tenant, and it must consider every live row rather than one tenant's. That is
// the sanctioned path the store itself already uses for instance-scoped rows, not
// a new bypass — the check reads only envelope metadata and returns no row to any
// caller.
func SelfTest(ctx context.Context, db *gorm.DB, kp KeyProvider) (SelfTestResult, error) {
	if db == nil {
		return "", errors.New("secrets: the root-key self-test requires a database handle")
	}
	if kp == nil {
		return "", errors.New("secrets: the root-key self-test requires a key provider")
	}

	// Reachability first, so an unreachable database is reported as an unreachable
	// database. Every later step degrades into a false "table missing" or a false
	// key failure if the connection is down, and those are the two readings that
	// would send an operator after the wrong cause.
	sqlDB, err := db.DB()
	if err != nil {
		return "", fmt.Errorf("secrets: could not obtain a database connection to check the instance root key "+
			"against the stored secrets; this is a database failure, not a root-key failure: %w", err)
	}
	if err := sqlDB.PingContext(ctx); err != nil {
		return "", fmt.Errorf("secrets: could not reach the database to check the instance root key against the "+
			"stored secrets; this is a database failure, not a root-key failure: %w", err)
	}

	sysdb := db.WithContext(core.WithSystemContext(ctx))
	if !sysdb.Migrator().HasTable(&Secret{}) {
		return "", errors.New("secrets: the secrets table does not exist, so the instance root key could not be " +
			"checked against stored secrets; run this service's schema migrations before building the secret store")
	}

	// One row, chosen by lowest primary key. Every live row in a service's secrets
	// table has its DEK wrapped by the same instance KEK, so any one of them
	// exercises the key the same way; what the ordering buys is that the choice is
	// the SAME row on every restart and on every replica, so the check is
	// reproducible and a failure points at one investigable row rather than at
	// whichever row the planner happened to return first.
	//
	// Find into a slice rather than First into a struct: an empty store is then a
	// zero-length result instead of ErrRecordNotFound, which keeps "nothing stored"
	// from having to be recovered out of an error alongside real read failures.
	// Soft-deleted rows are excluded by gorm's own default scope — a deleted secret
	// is not a credential this service can be asked to open.
	var rows []Secret
	if err := sysdb.Model(&Secret{}).Order("id ASC").Limit(1).Find(&rows).Error; err != nil {
		return "", fmt.Errorf("secrets: could not read the secrets table to check the instance root key; this is "+
			"a database failure, not a root-key failure: %w", err)
	}
	if len(rows) == 0 {
		return SelfTestNoStoredSecrets, nil
	}

	dek, err := kp.Unwrap(ctx, rows[0].WrappedDEK, rows[0].KEKVersion)
	if err != nil {
		// The wrapped cause distinguishes "did not authenticate" from "unknown KEK
		// version" without carrying any stored bytes into the message.
		return "", fmt.Errorf("secrets: the configured instance root key does not open this service's stored "+
			"secrets, so every stored credential would report as present and fail at use; check that the "+
			"instance root key is the one these secrets were sealed with: %w", err)
	}
	// The unwrapped DEK is not needed — the unwrap itself is the answer — and is
	// zeroed on the same best-effort basis as the envelope layer.
	zero(dek)
	return SelfTestVerified, nil
}
