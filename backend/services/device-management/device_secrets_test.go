// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/devicechain-io/dc-device-management/model"
	"github.com/devicechain-io/dc-device-management/schema"
	dmtest "github.com/devicechain-io/dc-device-management/test"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/credential"
	dcgraphql "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-microservice/rdb"
	putest "github.com/devicechain-io/dc-microservice/rdb/partialupdatetest"
)

// 🔑 AN UPGRADED INSTANCE KEEPS ITS DEVICES. Rows written before secrets were digested hold
// the password in credential_value. The startup step digests every one of them in place —
// every tenant, soft-deleted rows included — and NULLs the plaintext, and a device then
// connects with the password it always had.

// legacySecretFixture is a database as an upgrade finds it: the live tables plus the
// credential_value column the plaintext lived in, and credentials across two tenants whose
// secrets are stored there as plaintext.
type legacySecretFixture struct {
	db   *gorm.DB
	api  *model.Api
	acme context.Context
	beta context.Context
}

func newLegacySecretFixture(t *testing.T) legacySecretFixture {
	t.Helper()
	db := putest.NewSQLiteDB(t, credentialTables()...)
	// credential_value exists because the live model still maps it, write-only.
	api := model.NewApi(&rdb.RdbManager{Database: db})
	api.DeviceSecretKey = dmtest.DeviceSecretKey()
	f := legacySecretFixture{db: db, api: api,
		acme: core.WithTenant(context.Background(), "acme"), beta: core.WithTenant(context.Background(), "beta")}

	for _, ctx := range []context.Context{f.acme, f.beta} {
		_, err := api.CreateDeviceType(ctx, &model.DeviceTypeCreateRequest{Token: "dt"})
		require.NoError(t, err)
		_, err = api.CreateDevice(ctx, &model.DeviceCreateRequest{Token: "dev", DeviceTypeToken: "dt"})
		require.NoError(t, err)
	}
	// Created with no secret, then given a plaintext one the way the old code stored it.
	for _, c := range []struct {
		ctx              context.Context
		token, id, ctype string
		plaintext        any
	}{
		{f.acme, "a-1", "user-a1", string(model.CredentialMqttBasic), "pw-a1"},
		{f.acme, "a-2", "user-a2", string(model.CredentialMqttBasic), " pw a2 "},
		{f.acme, "a-del", "user-del", string(model.CredentialMqttBasic), "pw-deleted"},
		{f.acme, "a-tok", "tok-a", string(model.CredentialAccessToken), "stray-value"},
		{f.acme, "a-empty", "user-empty", string(model.CredentialMqttBasic), ""},
		{f.acme, "a-none", "user-none", string(model.CredentialMqttBasic), nil},
		{f.beta, "b-1", "user-b1", string(model.CredentialMqttBasic), "pw-b1"},
	} {
		_, err := api.CreateDeviceCredential(c.ctx, &model.DeviceCredentialCreateRequest{
			Token: c.token, DeviceToken: "dev", CredentialType: c.ctype, CredentialId: c.id, Enabled: true,
		})
		require.NoError(t, err)
		require.NoError(t, db.Exec(`UPDATE device_credentials SET credential_value = ? WHERE token = ?`,
			c.plaintext, c.token).Error)
	}
	require.NoError(t, db.Exec(`UPDATE device_credentials SET deleted_at = ? WHERE token = ?`, time.Now(), "a-del").Error)
	return f
}

// column reads one raw column of credential token's row, deleted or not.
func (f legacySecretFixture) column(t *testing.T, token, col string) any {
	t.Helper()
	var rows []map[string]any
	require.NoError(t, f.db.Raw(`SELECT `+col+` FROM device_credentials WHERE token = ?`, token).Scan(&rows).Error)
	require.Len(t, rows, 1, token)
	return rows[0][col]
}

func TestTheStartupStepDigestsEveryPlaintextSecretInPlace(t *testing.T) {
	f := newLegacySecretFixture(t)
	key := dmtest.DeviceSecretKey()
	rdbm := &rdb.RdbManager{Database: f.db}

	require.NoError(t, digestStoredCredentialSecrets(context.Background(), rdbm, key))

	tenantOf := map[string]string{"a-1": "acme", "a-2": "acme", "a-del": "acme", "a-tok": "acme", "b-1": "beta"}
	for token, secret := range map[string]string{
		"a-1": "pw-a1", "a-2": " pw a2 ", "a-del": "pw-deleted", "a-tok": "stray-value", "b-1": "pw-b1",
	} {
		require.Nil(t, f.column(t, token, "credential_value"), "%s still holds its plaintext", token)
		digest, _ := f.column(t, token, "secret_digest").(string)
		require.NoError(t, credential.VerifyDeviceSecret(key, tenantOf[token], digest, secret), "%s: the digest does not verify its old secret", token)
	}
	// An empty stored value meant "no secret" and still does; NULL stays NULL.
	for _, token := range []string{"a-empty", "a-none"} {
		require.Nil(t, f.column(t, token, "credential_value"), token)
		require.Nil(t, f.column(t, token, "secret_digest"), token)
	}

	// A device keeps the password it always had: both tenants, both paths.
	for _, c := range []struct {
		ctx                  context.Context
		tenant, user, secret string
	}{{f.acme, "acme", "user-a2", " pw a2 "}, {f.beta, "beta", "user-b1", "pw-b1"}} {
		ctx, tenant, secret := c.ctx, c.tenant, c.secret
		presented := &model.PresentedCredential{CredentialType: string(model.CredentialMqttBasic), CredentialId: c.user, Secret: &secret}
		_, err := f.api.AuthenticateDevice(ctx, presented, time.Now())
		require.NoError(t, err)
		_, stored, err := f.api.ResolveDeviceCredential(ctx, presented, time.Now())
		require.NoError(t, err)
		require.NoError(t, credential.VerifyDeviceSecret(key, tenant, stored, secret))
	}

	// It runs on every start: the second finds nothing to do and changes nothing.
	before := f.column(t, "a-1", "secret_digest")
	n, err := schema.DigestPlaintextCredentialSecrets(context.Background(), f.db, key.Digest)
	require.NoError(t, err)
	require.Zero(t, n)
	require.Equal(t, before, f.column(t, "a-1", "secret_digest"))
}

// The write is a compare-and-set on the plaintext it read: a row another replica converted
// between this one's read and its write is left as that replica wrote it, not overwritten.
func TestTheStartupStepDoesNotOverwriteARowAnotherReplicaConverted(t *testing.T) {
	f := newLegacySecretFixture(t)
	key := dmtest.DeviceSecretKey()
	raced := ""
	digest := func(tenant, secret string) (string, error) {
		if secret == "pw-a1" && raced == "" {
			// Another replica gets there first.
			d, err := key.Digest(tenant, secret)
			require.NoError(t, err)
			raced = d
			require.NoError(t, f.db.Exec(`UPDATE device_credentials SET secret_digest = ?, credential_value = NULL WHERE token = ?`, d, "a-1").Error)
		}
		return key.Digest(tenant, secret)
	}
	n, err := schema.DigestPlaintextCredentialSecrets(context.Background(), f.db, digest)
	require.NoError(t, err)
	require.Equal(t, 5, n, "a-2, a-del, a-tok and b-1 are converted and a-empty cleared here; a-1 was converted elsewhere")
	require.Equal(t, raced, f.column(t, "a-1", "secret_digest"), "the other replica's digest was overwritten")
}

// Digests this key did not make are counted, so a database restored next to the wrong root
// key is reported at startup.
func TestTheStartupStepCountsDigestsMadeUnderAnotherKey(t *testing.T) {
	f := newLegacySecretFixture(t)
	key := dmtest.DeviceSecretKey()
	require.NoError(t, digestStoredCredentialSecrets(context.Background(), &rdb.RdbManager{Database: f.db}, key))

	prefix := "v1$" + key.KeyId() + "$"
	n, err := schema.CountForeignCredentialDigests(context.Background(), f.db, prefix)
	require.NoError(t, err)
	require.Zero(t, n, "every digest was made by this key")

	other, err := credential.DeriveDeviceSecretKey([]byte("a-different-instance-root-key-32"))
	require.NoError(t, err)
	foreign, err := other.Digest("beta", "pw-b1")
	require.NoError(t, err)
	require.NoError(t, f.db.Exec(`UPDATE device_credentials SET secret_digest = ? WHERE token = ?`, foreign, "b-1").Error)
	n, err = schema.CountForeignCredentialDigests(context.Background(), f.db, prefix)
	require.NoError(t, err)
	require.Equal(t, int64(1), n)
}

// 🔴 A ROLLING UPGRADE CANNOT UNDO A ROTATION. During the overlap an old-version pod writes
// a rotated password into credential_value and leaves secret_digest alone. A later
// rotation on a new pod must clear that column, or the next start's digest step would
// digest the older password over the newer one and the rotated-away password would
// authenticate again. And the other way round: when the old pod's rotation is the LATER
// one, the step must keep it.
func TestARotationDuringTheRollingUpgradeIsNotUndoneByTheNextStart(t *testing.T) {
	presented := func(secret string) *model.PresentedCredential {
		return &model.PresentedCredential{CredentialType: string(model.CredentialMqttBasic), CredentialId: "user-a1", Secret: &secret}
	}
	oldPodRotates := func(f legacySecretFixture, secret string) {
		require.NoError(t, f.db.Exec(`UPDATE device_credentials SET credential_value = ? WHERE token = ?`, secret, "a-1").Error)
	}
	newPodRotates := func(f legacySecretFixture, secret string) {
		_, err := f.api.UpdateDeviceCredential(f.acme, "a-1", &model.DeviceCredentialUpdateRequest{
			CredentialValue: dcgraphql.OptionalStringOf(secret),
		})
		require.NoError(t, err)
	}
	restart := func(f legacySecretFixture) {
		require.NoError(t, digestStoredCredentialSecrets(context.Background(), &rdb.RdbManager{Database: f.db}, dmtest.DeviceSecretKey()))
	}
	authenticates := func(f legacySecretFixture, secret string) bool {
		_, err := f.api.AuthenticateDevice(f.acme, presented(secret), time.Now())
		return err == nil
	}

	t.Run("old pod, then new pod", func(t *testing.T) {
		f := newLegacySecretFixture(t)
		restart(f)
		oldPodRotates(f, "P1")
		newPodRotates(f, "P2")
		require.Nil(t, f.column(t, "a-1", "credential_value"), "the new pod's write left the old pod's plaintext behind")
		restart(f)
		require.True(t, authenticates(f, "P2"), "the latest password must authenticate")
		require.False(t, authenticates(f, "P1"), "the rotated-away password authenticates again")
		require.False(t, authenticates(f, "pw-a1"))
	})

	t.Run("new pod, then old pod", func(t *testing.T) {
		f := newLegacySecretFixture(t)
		restart(f)
		newPodRotates(f, "P2")
		oldPodRotates(f, "P1")
		restart(f)
		require.True(t, authenticates(f, "P1"), "the old pod's later rotation must be the one kept")
		require.False(t, authenticates(f, "P2"))
	})

	// A new-version save that does not name a secret (a metadata edit) after an old pod's
	// rotation keeps the old pod's password: it is digested, not discarded, so the
	// rotated-away password does not come back either.
	t.Run("old pod rotates, new pod edits metadata", func(t *testing.T) {
		f := newLegacySecretFixture(t)
		restart(f)
		oldPodRotates(f, "P1")
		read, err := f.api.DeviceCredentialsByToken(f.acme, []string{"a-1"})
		require.NoError(t, err)
		require.False(t, read[0].LegacyCredentialValue.Valid, "a read filled the write-only plaintext column")
		_, err = f.api.UpdateDeviceCredential(f.acme, "a-1", &model.DeviceCredentialUpdateRequest{
			Metadata: dcgraphql.OptionalStringOf(`{"k":"v"}`),
		})
		require.NoError(t, err)
		require.Nil(t, f.column(t, "a-1", "credential_value"), "the save left plaintext behind")
		require.True(t, authenticates(f, "P1"), "the old pod's rotation was discarded")
		require.False(t, authenticates(f, "pw-a1"), "the rotated-away password authenticates again")
		restart(f)
		require.True(t, authenticates(f, "P1"))
	})
}
