// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	dcgraphql "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-microservice/rdb/rdbtest"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func deviceCredentialStaleFixture(t *testing.T) (*Api, context.Context, *rdbtest.StatementCounter) {
	t.Helper()
	family := deviceCredentialFamily()
	api := newPartialUpdateApi(t, family.Migrate...)
	ctx := partialUpdateCtx()
	family.Seed(t, api, ctx)
	statements := rdbtest.NewStatementCounter(" ")
	statements.Record(true)
	api.RDB.Database = api.RDB.Database.Session(&gorm.Session{Logger: statements})
	return api, ctx, statements
}

func TestUpdateDeviceCredentialStaleReadDoesNotRevertConcurrentEdit(t *testing.T) {
	api, ctx, statements := deviceCredentialStaleFixture(t)
	fired := false
	const callback = "test:device_credential_stale_other_writer"
	db := api.RDB.DB(ctx)
	require.NoError(t, db.Callback().Query().After("gorm:after_query").Register(callback, func(tx *gorm.DB) {
		if fired || tx.Statement.Schema == nil || tx.Statement.Schema.Table != "device_credentials" || tx.Error != nil || tx.RowsAffected == 0 {
			return
		}
		// The first entity read has captured the old metadata. Guard before
		// writing so neither nested queries nor the final reload can fire again.
		fired = true
		fresh := api.RDB.DB(ctx).Session(&gorm.Session{NewDB: true})
		tx.AddError(fresh.Model(&DeviceCredential{}).Where("token = ?", "dcred-1").
			Update("metadata", `{"writer":"other"}`).Error)
	}))
	t.Cleanup(func() { require.NoError(t, db.Callback().Query().Remove(callback)) })

	updated, err := api.UpdateDeviceCredential(ctx, "dcred-1", &DeviceCredentialUpdateRequest{
		Enabled: dcgraphql.OptionalBoolOf(false),
	})
	require.NoError(t, err)
	require.True(t, fired, "the stale-read interleaving must execute")
	rows, err := api.DeviceCredentialsByToken(ctx, []string{"dcred-1"})
	stored := requireOne(t, "device_credential", rows, err)
	for _, row := range []*DeviceCredential{stored, updated} {
		require.False(t, row.Enabled)
		require.JSONEq(t, `{"writer":"other"}`, jsonStr(row.Metadata), "return value must be reloaded")
	}
	// The other writer and the requested disable each issue exactly one UPDATE;
	// the disable must include updated_at and omit all other entity columns.
	var updates []string
	for _, sql := range customerPartialWrites(statements) {
		if strings.HasPrefix(sql, "UPDATE ") {
			updates = append(updates, sql)
		} else {
			require.True(t, strings.HasPrefix(sql, "INSERT INTO `audit_events` "), "unexpected write: %s", sql)
		}
	}
	require.Len(t, updates, 2)
	require.Contains(t, updates[1], "`enabled`=")
	require.Contains(t, updates[1], "`updated_at`=")
	for _, column := range []string{"id", "created_at", "deleted_at", "metadata", "secret_digest", "credential_value", "credential_id", "issued_credential_id", "credential_type", "device_id", "expires_at", "provisioning_profile_id", "trust_anchor_id", "certificate_pem", "token", "tenant_id"} {
		require.NotContains(t, updates[1], "`"+column+"`=")
	}
}

func TestUpdateDeviceCredentialEmptyRequestWritesNothing(t *testing.T) {
	api, ctx, statements := deviceCredentialStaleFixture(t)
	var before DeviceCredential
	require.NoError(t, api.RDB.DB(ctx).Where("token = ?", "dcred-1").First(&before).Error)
	rows, err := api.DeviceCredentialsByToken(ctx, []string{"dcred-1"})
	loaded := requireOne(t, "device_credential", rows, err)

	updated, err := api.UpdateDeviceCredential(ctx, "dcred-1", &DeviceCredentialUpdateRequest{})
	require.NoError(t, err)
	require.Empty(t, customerPartialWrites(statements), "an empty request must issue no writes")
	var stored DeviceCredential
	require.NoError(t, api.RDB.DB(ctx).First(&stored, before.ID).Error)
	require.Equal(t, before, stored, "the complete stored row, including updated_at, must stay unchanged")
	require.Equal(t, loaded, updated)
}

func TestUpdateDeviceCredentialInvalidTypeWritesNothing(t *testing.T) {
	api, ctx, statements := deviceCredentialStaleFixture(t)
	var before DeviceCredential
	require.NoError(t, api.RDB.DB(ctx).Where("token = ?", "dcred-1").First(&before).Error)

	_, err := api.UpdateDeviceCredential(ctx, "dcred-1", &DeviceCredentialUpdateRequest{
		Metadata:       dcgraphql.OptionalStringOf(`{"refused":true}`),
		CredentialType: dcgraphql.OptionalStringOf("INVALID"),
	})
	require.Error(t, err)
	require.Empty(t, customerPartialWrites(statements), "a refused request must issue no writes")
	var stored DeviceCredential
	require.NoError(t, api.RDB.DB(ctx).First(&stored, before.ID).Error)
	require.Equal(t, before, stored, "a refusal must preserve the complete row, including updated_at")
}

func TestUpdateDeviceCredentialNamedAuthorityColumns(t *testing.T) {
	for _, tc := range []struct {
		name    string
		request DeviceCredentialUpdateRequest
		columns []string
	}{
		{"secret", DeviceCredentialUpdateRequest{CredentialValue: dcgraphql.OptionalStringOf("rotated-material-0123")}, []string{"secret_digest", "credential_value"}},
		{"id", DeviceCredentialUpdateRequest{CredentialId: dcgraphql.OptionalStringOf("new-username")}, []string{"credential_id"}},
		{"repoint", DeviceCredentialUpdateRequest{DeviceToken: dcgraphql.OptionalStringOf("dev-b")}, []string{"device_id"}},
		{"accessToken", DeviceCredentialUpdateRequest{CredentialType: dcgraphql.OptionalStringOf(string(CredentialAccessToken)), CredentialId: dcgraphql.OptionalStringOf("new-bearer-material-0123")}, []string{"credential_type", "credential_id", "secret_digest", "credential_value"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			api, ctx, statements := deviceCredentialStaleFixture(t)
			rows, err := api.DeviceCredentialsByToken(ctx, []string{"dcred-1"})
			before := requireOne(t, "credential", rows, err)
			updated, err := api.UpdateDeviceCredential(ctx, "dcred-1", &tc.request)
			require.NoError(t, err)
			var updates []string
			for _, sql := range customerPartialWrites(statements) {
				if strings.HasPrefix(sql, "UPDATE ") {
					updates = append(updates, sql)
				} else {
					require.True(t, strings.HasPrefix(sql, "INSERT INTO `audit_events` "), "unexpected write: %s", sql)
				}
			}
			require.Len(t, updates, 1)
			wanted := map[string]bool{"updated_at": true}
			for _, column := range tc.columns {
				wanted[column] = true
				require.Contains(t, updates[0], "`"+column+"`=")
			}
			require.Contains(t, updates[0], "`updated_at`=")
			for _, column := range []string{"id", "created_at", "deleted_at", "metadata", "enabled", "expires_at", "credential_type", "credential_id", "issued_credential_id", "device_id", "secret_digest", "credential_value", "provisioning_profile_id", "trust_anchor_id", "certificate_pem", "token", "tenant_id"} {
				if !wanted[column] {
					require.NotContains(t, updates[0], "`"+column+"`=")
				}
			}
			rows, err = api.DeviceCredentialsById(ctx, []uint{updated.ID})
			stored := requireOne(t, "credential", rows, err)
			require.NotNil(t, updated.Device)
			switch tc.name {
			case "secret":
				require.NotEqual(t, before.SecretDigest, stored.SecretDigest)
				require.Equal(t, stored.SecretDigest, updated.SecretDigest)
			case "id":
				require.Equal(t, "new-username", stored.CredentialId)
				require.Equal(t, stored.CredentialId, updated.CredentialId)
				require.Empty(t, updated.IssuedCredentialId)
			case "repoint":
				require.Equal(t, "dev-b", updated.Device.Token)
				require.Equal(t, stored.DeviceId, updated.DeviceId)
			case "accessToken":
				require.Equal(t, "new-bearer-material-0123", updated.IssuedCredentialId)
				require.Empty(t, stored.IssuedCredentialId)
				require.NotEqual(t, updated.IssuedCredentialId, stored.CredentialId)
				require.Equal(t, stored.CredentialId, updated.CredentialId)
			}
		})
	}
}

func TestUpdateDeviceCredentialAdoptsLegacySecretAndClearsPlaintext(t *testing.T) {
	family := deviceCredentialFamily()
	api := newPartialUpdateApi(t, family.Migrate...)
	ctx := partialUpdateCtx()
	family.Seed(t, api, ctx)
	const legacyPassword = "legacy-rotated-material-0123"
	require.NoError(t, api.RDB.DB(ctx).Exec("UPDATE device_credentials SET credential_value = ? WHERE token = ?", legacyPassword, "dcred-1").Error)
	statements := rdbtest.NewStatementCounter(" ")
	statements.Record(true)
	api.RDB.Database = api.RDB.Database.Session(&gorm.Session{Logger: statements})

	updated, err := api.UpdateDeviceCredential(ctx, "dcred-1", &DeviceCredentialUpdateRequest{})
	require.NoError(t, err)
	require.Equal(t, legacyPassword, credentialValueOf(updated, legacyPassword))
	var legacy []sql.NullString
	require.NoError(t, api.RDB.DB(ctx).Model(&DeviceCredential{}).Where("id = ?", updated.ID).Pluck("credential_value", &legacy).Error)
	require.Len(t, legacy, 1)
	require.False(t, legacy[0].Valid, "adoption must retire the old pod's plaintext")
	var updates []string
	for _, sql := range customerPartialWrites(statements) {
		if strings.HasPrefix(sql, "UPDATE ") {
			updates = append(updates, sql)
		}
	}
	require.Len(t, updates, 1)
	for _, column := range []string{"secret_digest", "credential_value", "updated_at"} {
		require.Contains(t, updates[0], "`"+column+"`=")
	}
	beforeWrites := customerPartialWrites(statements)
	_, err = api.UpdateDeviceCredential(ctx, "dcred-1", &DeviceCredentialUpdateRequest{})
	require.NoError(t, err)
	require.Equal(t, beforeWrites, customerPartialWrites(statements), "after adoption an empty request must issue no writes")
}

// rotateOnFirstRead registers a one-shot callback that, right after the first read of the credential,
// (the afterReads-th read of the table) stores digest (and clears the legacy plaintext) the way another writer's rotation would.
func rotateOnFirstRead(t *testing.T, api *Api, ctx context.Context, digest sql.NullString, afterReads int) *bool {
	t.Helper()
	fired := false
	reads := 0
	const callback = "test:device_credential_secret_rotation"
	db := api.RDB.DB(ctx)
	require.NoError(t, db.Callback().Query().After("gorm:after_query").Register(callback, func(tx *gorm.DB) {
		if fired || tx.Statement.Schema == nil || tx.Statement.Schema.Table != "device_credentials" || tx.Error != nil || tx.RowsAffected == 0 {
			return
		}
		if reads++; reads < afterReads {
			return
		}
		fired = true
		fresh := api.RDB.DB(ctx).Session(&gorm.Session{NewDB: true})
		tx.AddError(fresh.Model(&DeviceCredential{}).Where("token = ?", "dcred-1").
			Updates(map[string]any{"secret_digest": digest, "credential_value": sql.NullString{}}).Error)
	}))
	t.Cleanup(func() { require.NoError(t, db.Callback().Query().Remove(callback)) })
	return &fired
}

// A type-naming update derives the digest from the unlocked read; a rotation between that read and the
// lock must survive it (the retired password must stay refused).
func TestUpdateDeviceCredentialTypeEditDoesNotUndoConcurrentRotation(t *testing.T) {
	api, ctx, _ := deviceCredentialStaleFixture(t)
	var before DeviceCredential
	require.NoError(t, api.RDB.DB(ctx).Where("token = ?", "dcred-1").First(&before).Error)
	newPassword := "rotated-material-0123456"
	rotated, err := digestSecret(api.DeviceSecretKey, before.TenantId, &newPassword, 0)
	require.NoError(t, err)
	require.NotEqual(t, before.SecretDigest, rotated)
	fired := rotateOnFirstRead(t, api, ctx, rotated, 1)

	_, err = api.UpdateDeviceCredential(ctx, "dcred-1", &DeviceCredentialUpdateRequest{
		CredentialType: dcgraphql.OptionalStringOf(string(CredentialMqttBasic)),
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "retry")
	require.True(t, *fired, "the rotation must execute between the read and the lock")
	var stored DeviceCredential
	require.NoError(t, api.RDB.DB(ctx).First(&stored, before.ID).Error)
	require.Equal(t, rotated, stored.SecretDigest, "the rotated secret must survive; the old password must stay refused")
}

// A legacy-plaintext row is adopted from the plaintext read before the lock; a rotation in between must
// not be overwritten with the digest of the retired plaintext.
func TestUpdateDeviceCredentialLegacyAdoptionDoesNotUndoConcurrentRotation(t *testing.T) {
	api, ctx, _ := deviceCredentialStaleFixture(t)
	require.NoError(t, api.RDB.DB(ctx).Exec("UPDATE device_credentials SET credential_value = ? WHERE token = ?",
		"legacy-rotated-material-0123", "dcred-1").Error)
	var before DeviceCredential
	require.NoError(t, api.RDB.DB(ctx).Where("token = ?", "dcred-1").First(&before).Error)
	newPassword := "rotated-material-0123456"
	rotated, err := digestSecret(api.DeviceSecretKey, before.TenantId, &newPassword, 0)
	require.NoError(t, err)
	fired := rotateOnFirstRead(t, api, ctx, rotated, 2) // after the legacy plaintext read

	_, err = api.UpdateDeviceCredential(ctx, "dcred-1", &DeviceCredentialUpdateRequest{
		Enabled: dcgraphql.OptionalBoolOf(false),
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "retry")
	require.True(t, *fired)
	var stored DeviceCredential
	require.NoError(t, api.RDB.DB(ctx).First(&stored, before.ID).Error)
	require.Equal(t, rotated, stored.SecretDigest)
	require.True(t, stored.Enabled, "a refused update writes nothing")
}
