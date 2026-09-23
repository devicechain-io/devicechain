// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package schema

import (
	"errors"

	"github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/gorm"
)

// signingKeyCleartextSnapshot is this migration's own snapshot of signing_keys: the
// one column it removes, and nothing else.
//
// It is a SNAPSHOT, not the live model, per the house rule in migrations.go — and the
// live model could not serve anyway, because it no longer has the column.
//
// 🔴 IT PINS TableName, WHICH THE BASELINE'S signingKey MUST NOT. baseline_snapshot.go
// explains the trap: core/rdb's TablePrefix is applied only when gorm DERIVES a table
// name, so a pinned name resolves through search_path instead and any INDEX gorm
// builds from it comes out unprefixed — `idx_signing_keys_…` rather than the
// `idx_user-management_signing_keys_…` this table has. That hazard is about index
// names, and this migration builds no index: it only asks whether a column exists and
// drops it, and a column name carries no prefix. Pinning is what the other appended
// migrations do, and it is what keeps a rename of this type from retargeting the drop
// at a table that does not exist.
type signingKeyCleartextSnapshot struct {
	PrivateKeyPem string
}

func (signingKeyCleartextSnapshot) TableName() string { return "signing_keys" }

// NewSigningKeyPrivateHalfMigration removes the cleartext private keys from
// signing_keys: every existing row, and then the column that held them.
//
// Until this migration, every JWT signing key — the active one and each retired one —
// was stored as a cleartext PEM in private_key_pem, so anyone who could read this
// database, a backup of it or its WAL archive held a key that signs tokens every
// service accepts. The private half now lives only in the instance secret store,
// sealed under the root key, and only for the active key.
//
// 🔴 THE ROWS ARE DELETED, NOT DEMOTED, and that is the point. Each existing key's
// private half has sat in cleartext in the database and in every backup and archive
// taken since. Demoting those keys would keep their public halves in the JWKS — and
// with no rotation configured, a retired key is never pruned — so anyone holding an old
// backup could keep forging tokens that verify. Deleting them means no key that was
// ever stored in cleartext is trusted again. The cost is one forced sign-in for
// everyone: every access and refresh token signed by a deleted key stops validating,
// and user-management mints a fresh, sealed key when it starts.
//
// Two limits, stated because the migration cannot close them:
//
//   - The guarantee holds once the rollout completes, not at the moment this runs. A
//     pod still running the previous release keeps signing with, and serving a JWKS
//     containing, the key it loaded at its own startup until it terminates, and a
//     validating service drops a key it has cached only when it next refreshes its key
//     set. An upgrade restarts every pod, so the window closes with the rollout.
//   - DELETE and DROP COLUMN do not scrub the old values from dead tuples, from backups
//     already taken, or from the WAL archive. Those copies still hold keys — but no
//     longer trusted ones, because nothing serves their public halves after this.
//
// ONE TRANSACTION, AND THE DROP GOES FIRST. The chain runs with UseTransaction:false,
// so without its own transaction the DELETE and the DROP would be two autocommit
// statements — and a pod still on the previous release that (re)started between them
// would find no active key and mint one with a cleartext private_key_pem, which the
// DROP would then strip. The new service would find an active row with no sealed
// private half and refuse to start until someone edited the table by hand. Postgres
// DDL is transactional, so both statements commit together or not at all; and the
// DROP COLUMN, run first, takes the table's ACCESS EXCLUSIVE lock before anything
// else, so every other reader and writer waits for the commit and then sees both
// changes at once. An old-release pod that then tries to mint fails loudly on the
// missing column instead of leaving a row behind. (DELETE first would still commit
// atomically, but its row locks would come before the table lock, and an old pod
// rotating at the same moment could deadlock the migration.)
//
// Individually re-runnable, as the chain requires (replayed from the top after a
// failure): it acts only while the column exists, and the column exists exactly when
// the transaction has not committed. A replay after the commit is a no-op, and a
// failure inside it rolls both statements back and replays both.
//
// On a fresh install the table is empty when this runs, so it only drops the column
// the baseline created.
func NewSigningKeyPrivateHalfMigration() *gormigrate.Migration {
	return &gormigrate.Migration{
		ID: "20260923130100",
		Migrate: func(db *gorm.DB) error {
			return db.Transaction(func(tx *gorm.DB) error {
				snapshot := &signingKeyCleartextSnapshot{}
				if !tx.Migrator().HasColumn(snapshot, "private_key_pem") {
					return nil
				}
				if err := tx.Migrator().DropColumn(snapshot, "private_key_pem"); err != nil {
					return err
				}
				// Raw SQL, not tx.Delete: a gorm Delete with no condition is refused
				// (ErrMissingWhereClause), and one over a type carrying DeletedAt would be
				// a SOFT delete that leaves every public half in the table. The bare
				// table name resolves through search_path, as the pinned TableName does.
				return tx.Exec("DELETE FROM signing_keys").Error
			})
		},
		Rollback: func(tx *gorm.DB) error {
			// Unimplemented must fail loudly. The keys this deleted are gone, and a
			// column restored empty would be NOT NULL over no data — or nullable and
			// silently meaningless — neither of which is the schema before this ran.
			return errors.New("the signing-key private-half migration cannot be rolled back: the cleartext keys it removed are gone by design")
		},
	}
}
