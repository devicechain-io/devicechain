// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/devicechain-io/dc-device-management/model"
	dmtest "github.com/devicechain-io/dc-device-management/test"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/credential"
	"github.com/devicechain-io/dc-microservice/rdb"
	putest "github.com/devicechain-io/dc-microservice/rdb/partialupdatetest"
)

// 🔑 AN UPGRADED INSTANCE KEEPS ITS PROFILES AND ITS OPEN CLAIMS. Rows written before these
// secrets were digested hold them in provision_secret and claim_secret. The startup step
// digests every one in place, across tenants, claim statuses and soft-deleted rows, and
// NULLs the plaintext. The values in the rows are what this test asserts, because
// hack/migration-diff.sh compares the schema and no rows.

func rawColumn(t *testing.T, db *gorm.DB, table, col, token string) any {
	t.Helper()
	var rows []map[string]any
	require.NoError(t, db.Raw(`SELECT `+col+` FROM `+table+` WHERE token = ?`, token).Scan(&rows).Error)
	require.Len(t, rows, 1, token)
	return rows[0][col]
}

func TestTheStartupStepDigestsEveryPlaintextEnrollmentSecretInPlace(t *testing.T) {
	db := putest.NewSQLiteDB(t, &model.ProvisioningProfile{}, &model.DeviceClaim{}, &model.DeviceCredential{})
	// Seeding writes rows of two tenants, so it runs as the instance does.
	seed := db.WithContext(core.WithSystemContext(context.Background()))
	keys := dmtest.EnrollmentKeys()

	legacy := func(s string) sql.NullString { return sql.NullString{String: s, Valid: true} }
	profile := func(tenant, token, key string, secret sql.NullString) {
		p := &model.ProvisioningProfile{ProvisionKey: key, LegacyProvisionSecret: secret, ProvisionSecretIssued: true}
		p.TenantId, p.Token = tenant, token
		require.NoError(t, seed.Create(p).Error)
	}
	profile("acme", "pp-a", "key-a", legacy("fleet-a"))
	profile("acme", "pp-pad", "key-pad", legacy(" fleet pad "))
	profile("acme", "pp-del", "key-del", legacy("fleet-deleted"))
	profile("beta", "pp-b", "key-b", legacy("fleet-b"))
	profile("acme", "pp-blank", "key-blank", legacy(""))
	profile("acme", "pp-none", "key-none", sql.NullString{})
	// An old-version pod's update that does not name the secret saves the column back BLANK,
	// over a row a new pod gave a digest. The digest must survive the next start.
	oldSave := &model.ProvisioningProfile{ProvisionKey: "key-oldsave", LegacyProvisionSecret: legacy("")}
	oldSave.TenantId, oldSave.Token = "acme", "pp-oldsave"
	oldSave.ProvisionSecretDigest = legacy(mustDigest(t, keys.Provisioning, "acme", "fleet-oldsave"))
	require.NoError(t, seed.Create(oldSave).Error)
	require.NoError(t, db.Exec(`UPDATE provisioning_profiles SET deleted_at = ? WHERE token = ?`, time.Now(), "pp-del").Error)

	claim := func(tenant string, deviceId uint, status, secret string) {
		c := &model.DeviceClaim{DeviceId: deviceId, Status: status, LegacyClaimSecret: legacy(secret)}
		c.TenantId = tenant
		require.NoError(t, seed.Create(c).Error)
	}
	claim("acme", 1, string(model.ClaimStatusOpen), "printed-open")
	claim("acme", 2, string(model.ClaimStatusClaimed), "printed-claimed")
	claim("beta", 3, string(model.ClaimStatusOpen), "printed-beta")
	claim("acme", 4, string(model.ClaimStatusCanceled), "")
	oldSaveClaim := &model.DeviceClaim{DeviceId: 5, Status: string(model.ClaimStatusOpen), LegacyClaimSecret: legacy("")}
	oldSaveClaim.TenantId = "acme"
	oldSaveClaim.ClaimSecretDigest = legacy(mustDigest(t, keys.Claim, "acme", "claim-oldsave"))
	require.NoError(t, seed.Create(oldSaveClaim).Error)

	rdbm := &rdb.RdbManager{Database: db}
	require.NoError(t, digestStoredCredentialSecrets(context.Background(), rdbm,
		dmtest.DeviceSecretKey(), keys))

	// Every plaintext is gone, and the digest verifies the secret that was there, bound to
	// the row's own tenant.
	for token, want := range map[string]struct{ tenant, secret string }{
		"pp-a": {"acme", "fleet-a"}, "pp-pad": {"acme", " fleet pad "},
		"pp-del": {"acme", "fleet-deleted"}, "pp-b": {"beta", "fleet-b"},
	} {
		require.Nil(t, rawColumn(t, db, "provisioning_profiles", "provision_secret", token), "%s still holds its plaintext", token)
		digest, _ := rawColumn(t, db, "provisioning_profiles", "provision_secret_digest", token).(string)
		require.NoError(t, credential.VerifyDeviceSecret(keys.Provisioning, want.tenant, digest, want.secret), "%s", token)
		// The negative control: the digest is bound to the tenant and to the secret.
		other := "beta"
		if want.tenant == "beta" {
			other = "acme"
		}
		require.Error(t, credential.VerifyDeviceSecret(keys.Provisioning, other, digest, want.secret), "%s verifies in another tenant", token)
		require.Error(t, credential.VerifyDeviceSecret(keys.Provisioning, want.tenant, digest, want.secret+"x"), token)
	}
	// A secret that arrived as plaintext was a person's choice, not the server's: the profile is
	// no longer marked as holding a generated one, and the endpoint refuses it until rotated.
	for _, token := range []string{"pp-a", "pp-pad", "pp-b"} {
		require.Equal(t, false, rawBool(t, db, token), "%s is still marked as server-issued after its plaintext was digested", token)
	}
	for _, token := range []string{"pp-blank", "pp-none"} {
		require.Nil(t, rawColumn(t, db, "provisioning_profiles", "provision_secret", token), token)
		require.Nil(t, rawColumn(t, db, "provisioning_profiles", "provision_secret_digest", token), "%s was given a digest of nothing", token)
	}

	// The old pod's blank save: plaintext cleared, digest untouched and still verifying.
	require.Nil(t, rawColumn(t, db, "provisioning_profiles", "provision_secret", "pp-oldsave"))
	od, _ := rawColumn(t, db, "provisioning_profiles", "provision_secret_digest", "pp-oldsave").(string)
	require.NoError(t, credential.VerifyDeviceSecret(keys.Provisioning, "acme", od, "fleet-oldsave"),
		"a blank plaintext erased the profile's digest")

	var claims []map[string]any
	require.NoError(t, db.Raw(`SELECT device_id, tenant_id, claim_secret, claim_secret_digest FROM device_claims ORDER BY device_id`).Scan(&claims).Error)
	require.Len(t, claims, 5)
	secrets := []string{"printed-open", "printed-claimed", "printed-beta", "", "claim-oldsave"}
	for i, c := range claims {
		require.Nil(t, c["claim_secret"], "claim %d still holds its plaintext", i+1)
		if secrets[i] == "" {
			require.Nil(t, c["claim_secret_digest"], "claim %d was given a digest of nothing", i+1)
			continue
		}
		digest, _ := c["claim_secret_digest"].(string)
		require.NoError(t, credential.VerifyDeviceSecret(keys.Claim, c["tenant_id"].(string), digest, secrets[i]), "claim %d", i+1)
	}

	// A second start finds nothing to do.
	require.NoError(t, digestStoredCredentialSecrets(context.Background(), rdbm, dmtest.DeviceSecretKey(), keys))
	digest1, _ := rawColumn(t, db, "provisioning_profiles", "provision_secret_digest", "pp-a").(string)
	require.NoError(t, credential.VerifyDeviceSecret(keys.Provisioning, "acme", digest1, "fleet-a"))
}

// mustDigest is secret's digest for tenant under key.
func mustDigest(t *testing.T, key *credential.DeviceSecretKey, tenant, secret string) string {
	t.Helper()
	d, err := key.Digest(tenant, secret)
	require.NoError(t, err)
	return d
}

func rawBool(t *testing.T, db *gorm.DB, token string) bool {
	t.Helper()
	switch v := rawColumn(t, db, "provisioning_profiles", "provision_secret_issued", token).(type) {
	case bool:
		return v
	case int64:
		return v != 0
	}
	t.Fatalf("provision_secret_issued of %s is not a boolean", token)
	return false
}
