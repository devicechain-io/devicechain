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
// Individually re-runnable, as the chain requires (UseTransaction:false, replayed from
// the top after a failure): it acts only while the column exists. A replay after the
// drop is a no-op, and a failure between the DELETE and the drop replays both — which
// is also why the DELETE can never reach a row written after the cutover: no such row
// exists while the column does, because the service does not start until the chain
// has run.
//
// On a fresh install the table is empty when this runs, so it only drops the column
// the baseline created.
func NewSigningKeyPrivateHalfMigration() *gormigrate.Migration {
	return &gormigrate.Migration{
		ID: "20260923130100",
		Migrate: func(tx *gorm.DB) error {
			snapshot := &signingKeyCleartextSnapshot{}
			if !tx.Migrator().HasColumn(snapshot, "private_key_pem") {
				return nil
			}
			// Raw SQL, not tx.Delete: a gorm Delete with no condition is refused
			// (ErrMissingWhereClause), and one over a type carrying DeletedAt would be a
			// SOFT delete that leaves every PEM in place until the column is dropped. The
			// bare table name resolves through search_path, as the pinned TableName does.
			if err := tx.Exec("DELETE FROM signing_keys").Error; err != nil {
				return err
			}
			return tx.Migrator().DropColumn(snapshot, "private_key_pem")
		},
		Rollback: func(tx *gorm.DB) error {
			// Unimplemented must fail loudly. The keys this deleted are gone, and a
			// column restored empty would be NOT NULL over no data — or nullable and
			// silently meaningless — neither of which is the schema before this ran.
			return errors.New("the signing-key private-half migration cannot be rolled back: the cleartext keys it removed are gone by design")
		},
	}
}
