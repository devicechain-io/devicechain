// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/devicechain-io/dc-device-management/model"
	"github.com/devicechain-io/dc-device-management/schema"
	dmtest "github.com/devicechain-io/dc-device-management/test"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/credential"
	"github.com/devicechain-io/dc-microservice/rdb"
	putest "github.com/devicechain-io/dc-microservice/rdb/partialupdatetest"
)

// 🔑 AN UPGRADED INSTANCE KEEPS ITS TOKEN DEVICES. Rows written before access tokens were
// digested hold the token itself in credential_id, the column a lookup matches. The startup
// step replaces each with its keyed digest in place, every tenant and soft-deleted rows
// included, and a device then connects with the token it always had. There is no DDL: the
// column is the same, so no migration is involved and a replayed chain changes nothing.

type legacyTokenFixture struct {
	db   *gorm.DB
	api  *model.Api
	acme context.Context
	beta context.Context
}

// legacyTokens are the credentials the fixture holds, by credential token: the tenant and
// the access token as an old pod stored it.
var legacyTokens = map[string]struct{ tenant, token string }{
	"a-1":   {"acme", "tok-a1"},
	"a-2":   {"acme", " tok a2 "},
	"a-del": {"acme", "tok-deleted"},
	"b-1":   {"beta", "tok-b1"},
	// The same token string in two tenants: two rows, two digests, one tenant each.
	"b-same": {"beta", "tok-a1"},
	// The old column bounded a token in CHARACTERS (256), not bytes, so a stored token can be
	// longer in bytes than a new one may be set to. It must still convert and authenticate:
	// a conversion that refused it would stop the service from starting.
	"a-wide": {"acme", strings.Repeat("漢", 256)},
}

func newLegacyTokenFixture(t *testing.T) legacyTokenFixture {
	t.Helper()
	db := putest.NewSQLiteDB(t, credentialTables()...)
	api := model.NewApi(&rdb.RdbManager{Database: db})
	api.DeviceSecretKey = dmtest.DeviceSecretKey()
	f := legacyTokenFixture{db: db, api: api,
		acme: core.WithTenant(context.Background(), "acme"), beta: core.WithTenant(context.Background(), "beta")}
	for _, ctx := range []context.Context{f.acme, f.beta} {
		_, err := api.CreateDeviceType(ctx, &model.DeviceTypeCreateRequest{Token: "dt"})
		require.NoError(t, err)
		_, err = api.CreateDevice(ctx, &model.DeviceCreateRequest{Token: "dev", DeviceTypeToken: "dt"})
		require.NoError(t, err)
	}
	// Created through the API (so the row is complete), then put back the way the old code
	// stored it: the token itself in credential_id.
	for credToken, c := range legacyTokens {
		_, err := api.CreateDeviceCredential(f.ctxOf(c.tenant), &model.DeviceCredentialCreateRequest{
			Token: credToken, DeviceToken: "dev", CredentialType: string(model.CredentialAccessToken),
			CredentialId: "placeholder-" + credToken, Enabled: true,
		})
		require.NoError(t, err)
		require.NoError(t, db.Exec(`UPDATE device_credentials SET credential_id = ? WHERE token = ?`, c.token, credToken).Error)
	}
	require.NoError(t, db.Exec(`UPDATE device_credentials SET deleted_at = ? WHERE token = ?`, time.Now(), "a-del").Error)
	// An MQTT_BASIC username that is not a token and must be left exactly as it is.
	_, err := api.CreateDeviceCredential(f.acme, &model.DeviceCredentialCreateRequest{
		Token: "a-user", DeviceToken: "dev", CredentialType: string(model.CredentialMqttBasic),
		CredentialId: "user-a1", CredentialValue: strp("pw-0123456789abcdef"), Enabled: true,
	})
	require.NoError(t, err)
	return f
}

func strp(s string) *string { return &s }

func (f legacyTokenFixture) column(t *testing.T, token, col string) any {
	t.Helper()
	var rows []map[string]any
	require.NoError(t, f.db.Raw(`SELECT `+col+` FROM device_credentials WHERE token = ?`, token).Scan(&rows).Error)
	require.Len(t, rows, 1, token)
	return rows[0][col]
}

func (f legacyTokenFixture) ctxOf(tenant string) context.Context {
	if tenant == "beta" {
		return f.beta
	}
	return f.acme
}

func accessToken(token string) *model.PresentedCredential {
	return &model.PresentedCredential{CredentialType: string(model.CredentialAccessToken), CredentialId: token}
}

func TestTheStartupStepDigestsEveryPlaintextAccessTokenInPlace(t *testing.T) {
	f := newLegacyTokenFixture(t)
	key := dmtest.DeviceSecretKey()

	// NEGATIVE CONTROL. Before the step the rows hold plaintext, so no token resolves: this is
	// what an old-version row looks like to this version, and it is what the step exists to
	// fix. If this passed, every check below would pass for the wrong reason.
	for credToken, c := range legacyTokens {
		if credToken == "a-del" {
			continue
		}
		_, err := f.api.AuthenticateDeviceConnect(f.ctxOf(c.tenant), accessToken(c.token), time.Now())
		require.ErrorIs(t, err, model.ErrCredentialNotResolved, "%s authenticates before it was converted", credToken)
	}

	require.NoError(t, digestStoredCredentialSecrets(context.Background(), &rdb.RdbManager{Database: f.db}, key, dmtest.EnrollmentSecretKey()))

	// At rest: no column of any credential row holds a token, soft-deleted rows included.
	var rows []map[string]any
	require.NoError(t, f.db.Raw(`SELECT * FROM device_credentials`).Scan(&rows).Error)
	require.Len(t, rows, len(legacyTokens)+1)
	for _, row := range rows {
		for col, v := range row {
			s, _ := v.(string)
			for credToken, c := range legacyTokens {
				require.NotContains(t, s, strings.TrimSpace(c.token),
					"row %v column %s holds the token of %s", row["token"], col, credToken)
			}
		}
	}
	for credToken, c := range legacyTokens {
		stored, _ := f.column(t, credToken, "credential_id").(string)
		require.True(t, credential.IsAccessTokenDigest(stored), "%s was not converted: %q", credToken, stored)
		require.NoError(t, credential.VerifyAccessToken(key, c.tenant, stored, c.token), credToken)
	}
	require.Equal(t, "user-a1", f.column(t, "a-user", "credential_id"), "an MQTT username was touched")
	require.NotEqual(t, f.column(t, "a-1", "credential_id"), f.column(t, "b-same", "credential_id"),
		"one token in two tenants shares a digest")

	// Devices keep their tokens, on the connect path and the per-event path, in their own tenant.
	for credToken, c := range legacyTokens {
		if credToken == "a-del" {
			continue
		}
		ctx := f.ctxOf(c.tenant)
		_, err := f.api.AuthenticateDeviceConnect(ctx, accessToken(c.token), time.Now())
		require.NoError(t, err, credToken)
		_, err = f.api.AuthenticateDevice(ctx, accessToken(c.token), time.Now())
		require.NoError(t, err, credToken)
		_, err = f.api.AuthenticateDevice(ctx, accessToken(c.token+"x"), time.Now())
		require.ErrorIs(t, err, model.ErrCredentialNotResolved, "%s: a wrong token authenticates", credToken)
	}
	_, err := f.api.AuthenticateDevice(f.acme, accessToken("tok-deleted"), time.Now())
	require.ErrorIs(t, err, model.ErrCredentialNotResolved, "a deleted credential authenticates")

	// Idempotent: the second run finds nothing to do and changes nothing.
	before := f.column(t, "a-1", "credential_id")
	n, err := schema.DigestPlaintextAccessTokens(context.Background(), f.db, credential.IsAccessTokenDigest, key.AccessTokenDigest)
	require.NoError(t, err)
	require.Zero(t, n)
	require.Equal(t, before, f.column(t, "a-1", "credential_id"))
}

// The write is a compare-and-set on the id it read: a row another replica converted between
// this one's read and its write is left as that replica wrote it.
func TestTheStartupStepDoesNotOverwriteAnAccessTokenAnotherReplicaConverted(t *testing.T) {
	f := newLegacyTokenFixture(t)
	key := dmtest.DeviceSecretKey()
	raced := ""
	digest := func(tenant, token string) (string, error) {
		if token == "tok-a1" && tenant == "acme" && raced == "" {
			d, err := key.AccessTokenDigest(tenant, token)
			require.NoError(t, err)
			raced = d
			require.NoError(t, f.db.Exec(`UPDATE device_credentials SET credential_id = ? WHERE token = ?`, d, "a-1").Error)
		}
		return key.AccessTokenDigest(tenant, token)
	}
	n, err := schema.DigestPlaintextAccessTokens(context.Background(), f.db, credential.IsAccessTokenDigest, digest)
	require.NoError(t, err)
	require.Equal(t, len(legacyTokens)-1, n, "every row but the one converted elsewhere")
	require.Equal(t, raced, f.column(t, "a-1", "credential_id"))
}

// Digests this key did not make are counted, so a database restored next to the wrong root
// key is reported at startup, and those devices are refused.
func TestTheStartupStepCountsAccessTokenDigestsMadeUnderAnotherKey(t *testing.T) {
	f := newLegacyTokenFixture(t)
	key := dmtest.DeviceSecretKey()
	require.NoError(t, digestStoredCredentialSecrets(context.Background(), &rdb.RdbManager{Database: f.db}, key, dmtest.EnrollmentSecretKey()))

	n, err := schema.CountForeignAccessTokenDigests(context.Background(), f.db, key.AccessTokenDigestPrefix())
	require.NoError(t, err)
	require.Zero(t, n, "every digest was made by this key")

	other, err := credential.DeriveDeviceSecretKey([]byte("a-different-instance-root-key-32"))
	require.NoError(t, err)
	foreign, err := other.AccessTokenDigest("beta", "tok-b1")
	require.NoError(t, err)
	require.NoError(t, f.db.Exec(`UPDATE device_credentials SET credential_id = ? WHERE token = ?`, foreign, "b-1").Error)
	n, err = schema.CountForeignAccessTokenDigests(context.Background(), f.db, key.AccessTokenDigestPrefix())
	require.NoError(t, err)
	require.Equal(t, int64(1), n)
	_, err = f.api.AuthenticateDeviceConnect(f.beta, accessToken("tok-b1"), time.Now())
	require.ErrorIs(t, err, model.ErrCredentialNotResolved, "a token digested under another key authenticates")
}
