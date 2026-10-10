// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/credential"
	dcgraphql "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-microservice/limit"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"
)

// 🔴 A DEVICE SECRET IS NEVER STORED. Whatever a create or update carries, no column of the
// row it writes holds the secret, and the one column that answers for it is a digest that
// verifies it. Read through a raw row (every column, whatever the model maps), so a
// column the model stopped mapping cannot hide a plaintext.
func TestNoColumnOfACredentialRowHoldsTheSecret(t *testing.T) {
	api, ctx := resolveFixture(t) // creates c-1 with "s3cret"
	requireNoColumnHolds(t, api, "c-1", "s3cret")

	_, err := api.UpdateDeviceCredential(ctx, "c-1", &DeviceCredentialUpdateRequest{
		CredentialValue: dcgraphql.OptionalStringOf("r0tated-secret"),
	})
	require.NoError(t, err)
	requireNoColumnHolds(t, api, "c-1", "r0tated-secret")
	d, err := api.AuthenticateDevice(ctx, basic("cred-1", "r0tated-secret"), time.Now())
	require.NoError(t, err)
	require.Equal(t, "dev", d.Token)
}

// requireNoColumnHolds asserts no column of credential token's row contains secret, and
// that secret_digest verifies it.
func requireNoColumnHolds(t *testing.T, api *Api, token, secret string) {
	t.Helper()
	var rows []map[string]any
	require.NoError(t, api.RDB.Database.Raw("SELECT * FROM device_credentials WHERE token = ?", token).Scan(&rows).Error)
	require.Len(t, rows, 1)
	for col, v := range rows[0] {
		require.NotContains(t, fmt.Sprint(v), secret, "column %s holds the secret", col)
	}
	digest, _ := rows[0]["secret_digest"].(string)
	require.True(t, verifies(digest, secret), "secret_digest %q does not verify the secret", digest)
}

// The credential cache copies the row a read returned, so it holds the digest and never
// the secret; and a hit is still checked against it, so a wrong secret is refused from
// memory.
func TestTheCredentialCacheHoldsNoSecret(t *testing.T) {
	f := newCredentialCacheFixture(t)
	f.mustAuthenticate(t, basic("cred-1", "s3cret"), 1, "fill")

	f.cache.mu.Lock()
	held := 0
	for _, el := range f.cache.byKey {
		e := el.Value.(*credentialEntry)
		held++
		require.NotContains(t, fmt.Sprintf("%+v %+v", e.cred, e.device), "s3cret", "a cache entry holds the secret")
		require.True(t, verifies(e.cred.SecretDigest.String, "s3cret"), "the cached row carries no digest of the secret")
	}
	f.cache.mu.Unlock()
	require.Equal(t, 1, held)

	_, err, reads := f.check(basic("cred-1", "wrong"))
	require.ErrorIs(t, err, ErrCredentialSecretMismatch)
	require.Zero(t, reads, "the refusal must come from the cached digest, not a database read")
}

// 🔴 NO KEY, NO SECRET. An Api with no DeviceSecretKey refuses to store a secret rather than
// storing it as sent, and refuses to check a stored digest rather than comparing anything
// else. A credential with no secret is unaffected.
func TestWithNoKeyASecretIsNeitherStoredNorChecked(t *testing.T) {
	api, ctx := resolveFixture(t) // c-1 stored under testSecretKey
	api.DeviceSecretKey = nil

	_, err := api.CreateDeviceCredential(ctx, &DeviceCredentialCreateRequest{
		Token: "c-2", DeviceToken: "dev", CredentialType: string(CredentialMqttBasic),
		CredentialId: "cred-2", CredentialValue: strPtr("s3cret"), Enabled: true,
	})
	require.ErrorIs(t, err, errNoDeviceSecretKey)
	var n int64
	require.NoError(t, api.RDB.DB(ctx).Model(&DeviceCredential{}).Where("token = ?", "c-2").Count(&n).Error)
	require.Zero(t, n, "a refused create wrote a row")

	_, err = api.UpdateDeviceCredential(ctx, "c-1", &DeviceCredentialUpdateRequest{
		CredentialValue: dcgraphql.OptionalStringOf("new"),
	})
	require.ErrorIs(t, err, errNoDeviceSecretKey)

	_, err = api.AuthenticateDevice(ctx, basic("cred-1", "s3cret"), time.Now())
	require.ErrorIs(t, err, ErrCredentialMisconfigured)
	_, _, err = api.ResolveDeviceCredential(ctx, basic("cred-1", "s3cret"), time.Now())
	require.ErrorIs(t, err, ErrCredentialMisconfigured)

	// Control: a credential that carries no secret is created and checked without a key.
	_, err = api.CreateDeviceCredential(ctx, &DeviceCredentialCreateRequest{
		Token: "c-3", DeviceToken: "dev", CredentialType: string(CredentialAccessToken),
		CredentialId: "tok-3", Enabled: true,
	})
	require.NoError(t, err)
	_, err = api.AuthenticateDevice(ctx, accessToken("tok-3"), time.Now())
	require.NoError(t, err)
}

// 🔴 A DIGEST MADE UNDER ANOTHER KEY IS MISCONFIGURED, NOT A WRONG PASSWORD, on the
// per-event path as on the callout's. It is what every MQTT_BASIC row looks like when the
// database is restored next to the wrong root key, and an operator has to see it.
func TestADigestUnderAnotherKeyIsMisconfiguredOnBothPaths(t *testing.T) {
	api, ctx := resolveFixture(t)
	other, err := credential.DeriveDeviceSecretKey([]byte("a-different-instance-root-key-32"))
	require.NoError(t, err)
	api.DeviceSecretKey = other
	counted := prometheus.NewCounterVec(prometheus.CounterOpts{Name: "misconfigured"}, []string{"path"})
	api.MisconfiguredSecrets = counted

	_, err = api.AuthenticateDevice(ctx, basic("cred-1", "s3cret"), time.Now())
	require.ErrorIs(t, err, ErrCredentialMisconfigured)
	require.False(t, errors.Is(err, ErrCredentialSecretMismatch))
	require.False(t, IsCredentialRefusal(err), "a foreign digest must reach an operator, not the wrong-password noise")
	_, _, err = api.ResolveDeviceCredential(ctx, basic("cred-1", "s3cret"), time.Now())
	require.ErrorIs(t, err, ErrCredentialMisconfigured)

	// Counted, by path, apart from wrong passwords.
	require.Equal(t, 1.0, testutil.ToFloat64(counted.WithLabelValues(CredentialPathEvent)))
	require.Equal(t, 1.0, testutil.ToFloat64(counted.WithLabelValues(CredentialPathConnect)))

	// Control: under the key that made it, the same row authenticates, and a wrong password
	// is a mismatch that is not counted as misconfigured.
	api.DeviceSecretKey = testSecretKey
	_, err = api.AuthenticateDevice(ctx, basic("cred-1", "s3cret"), time.Now())
	require.NoError(t, err)
	_, err = api.AuthenticateDevice(ctx, basic("cred-1", "wrong"), time.Now())
	require.ErrorIs(t, err, ErrCredentialSecretMismatch)
	require.Equal(t, 1.0, testutil.ToFloat64(counted.WithLabelValues(CredentialPathEvent)))
}

// 🔴 A DIGEST CANNOT BE TRANSPLANTED ACROSS TENANTS. Copied from one tenant's row into
// another tenant's credential, a digest of a known password verifies nothing there.
func TestADigestCopiedIntoAnotherTenantVerifiesNothing(t *testing.T) {
	api, ctx := resolveFixture(t) // acme's cred-1 stores "s3cret"
	beta := core.WithTenant(context.Background(), "beta")
	_, err := api.CreateDeviceType(beta, &DeviceTypeCreateRequest{Token: "dt"})
	require.NoError(t, err)
	_, err = api.CreateDevice(beta, &DeviceCreateRequest{Token: "dev", DeviceTypeToken: "dt"})
	require.NoError(t, err)
	_, err = api.CreateDeviceCredential(beta, &DeviceCredentialCreateRequest{
		Token: "b-1", DeviceToken: "dev", CredentialType: string(CredentialMqttBasic),
		CredentialId: "cred-b", CredentialValue: strPtr("beta-secret"), Enabled: true,
	})
	require.NoError(t, err)

	var acmeDigest string
	require.NoError(t, api.RDB.Database.Raw("SELECT secret_digest FROM device_credentials WHERE token = ?", "c-1").Scan(&acmeDigest).Error)
	require.NoError(t, api.RDB.Database.Exec("UPDATE device_credentials SET secret_digest = ? WHERE token = ?", acmeDigest, "b-1").Error)

	_, err = api.AuthenticateDevice(beta, basic("cred-b", "s3cret"), time.Now())
	require.ErrorIs(t, err, ErrCredentialSecretMismatch)
	_, stored, err := api.ResolveDeviceCredential(beta, basic("cred-b", "s3cret"), time.Now())
	require.NoError(t, err)
	require.ErrorIs(t, credential.VerifyDeviceSecret(testSecretKey, "beta", stored, "s3cret"), credential.ErrMismatch)
	// Control: in its own tenant the digest still verifies.
	_, err = api.AuthenticateDevice(ctx, basic("cred-1", "s3cret"), time.Now())
	require.NoError(t, err)
}

// A secret over the limit the plaintext column used to enforce is refused with
// LIMIT_EXCEEDED on create and on update; one at the limit is accepted.
func TestAnOverlongSecretIsRefusedWithLimitExceeded(t *testing.T) {
	api, ctx := resolveFixture(t)
	over := strings.Repeat("x", credential.MaxDeviceSecretBytes+1)

	_, err := api.CreateDeviceCredential(ctx, &DeviceCredentialCreateRequest{
		Token: "c-2", DeviceToken: "dev", CredentialType: string(CredentialMqttBasic),
		CredentialId: "cred-2", CredentialValue: &over, Enabled: true,
	})
	le, ok := limit.As(err)
	require.True(t, ok, "create: got %v, want a limit refusal", err)
	require.Equal(t, credential.MaxDeviceSecretBytes, le.Max)

	_, err = api.UpdateDeviceCredential(ctx, "c-1", &DeviceCredentialUpdateRequest{
		CredentialValue: dcgraphql.OptionalStringOf(over),
	})
	_, ok = limit.As(err)
	require.True(t, ok, "update: got %v, want a limit refusal", err)

	atLimit := strings.Repeat("x", credential.MaxDeviceSecretBytes)
	_, err = api.UpdateDeviceCredential(ctx, "c-1", &DeviceCredentialUpdateRequest{
		CredentialValue: dcgraphql.OptionalStringOf(atLimit),
	})
	require.NoError(t, err)
	_, err = api.AuthenticateDevice(ctx, basic("cred-1", atLimit), time.Now())
	require.NoError(t, err)
}
