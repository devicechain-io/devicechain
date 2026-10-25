// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package schema

import (
	"context"

	"github.com/devicechain-io/dc-microservice/core"
	"gorm.io/gorm"
)

// An ACCESS_TOKEN credential is stored as the keyed digest of its token in credential_id
// (credential.DeviceSecretKey.AccessTokenDigest), where it used to be stored as the token.
// The column is the same one, so there is no DDL and no migration: the rows are converted
// by DigestPlaintextAccessTokens, which main runs after the migration chain on EVERY start,
// for the reason DigestPlaintextCredentialSecrets gives. A digest needs the key, which is
// derived from the instance root key and is not available to the migration chain.

// accessTokenRow is a snapshot of the device_credentials columns the conversion reads and
// writes. It is not the live model.
type accessTokenRow struct {
	ID             uint
	TenantId       string
	CredentialType string
	CredentialId   string
}

func (accessTokenRow) TableName() string { return "device_credentials" }

// accessTokenType is the credential_type the conversion covers.
const accessTokenType = "ACCESS_TOKEN"

// DigestPlaintextAccessTokens replaces every ACCESS_TOKEN credential_id that is still the
// token as sent with its keyed digest, across every tenant and including soft-deleted rows
// (a deleted credential's token is still a token). isDigest says which stored ids are
// already digests (credential.IsAccessTokenDigest), and it returns how many rows it
// converted.
//
// It runs under a system context, as instance maintenance, and writes each row with a
// compare-and-set on the id it read (id = ? AND credential_id = ?), so replicas starting
// together cannot undo one another: the loser updates nothing, and both would have written
// the same digest, which is deterministic. An empty id is left as it is: it has no digest,
// and a lookup never matches it.
//
// 🔴 IT RUNS ON EVERY START, not once. After the first, the read finds nothing and the step
// costs one query. That is what converts a token an old-version pod wrote while the rolling
// upgrade was still in progress.
func DigestPlaintextAccessTokens(ctx context.Context, db *gorm.DB,
	isDigest func(stored string) bool, digest func(tenant, token string) (string, error)) (int, error) {
	db = db.WithContext(core.WithSystemContext(ctx))
	converted := 0
	var after uint
	for {
		var rows []accessTokenRow
		if err := db.Where("id > ? AND credential_type = ? AND credential_id <> ''", after, accessTokenType).
			Order("id").Limit(digestBatch).Find(&rows).Error; err != nil {
			return converted, err
		}
		if len(rows) == 0 {
			return converted, nil
		}
		for _, row := range rows {
			after = row.ID
			if isDigest(row.CredentialId) {
				continue
			}
			d, err := digest(row.TenantId, row.CredentialId)
			if err != nil {
				return converted, err
			}
			// The model carries the primary key, so gorm adds id = ? and the audit journal
			// records which row changed.
			res := db.Model(&accessTokenRow{ID: row.ID}).
				Where("credential_id = ?", row.CredentialId).
				Updates(map[string]any{"credential_id": d})
			if res.Error != nil {
				return converted, res.Error
			}
			converted += int(res.RowsAffected)
		}
	}
}

// CountForeignAccessTokenDigests counts ACCESS_TOKEN credentials whose stored id does not
// carry prefix (the running key's digest prefix): ids no presented token can match under
// this key. After DigestPlaintextAccessTokens those are digests made under another root
// key, which means the database is not the one this root key's instance wrote, and every
// such device is refused until its credential is set again.
func CountForeignAccessTokenDigests(ctx context.Context, db *gorm.DB, prefix string) (int64, error) {
	var n int64
	err := db.WithContext(core.WithSystemContext(ctx)).Model(&accessTokenRow{}).
		Where("credential_type = ? AND credential_id <> '' AND credential_id NOT LIKE ?", accessTokenType, prefix+"%").
		Count(&n).Error
	return n, err
}
