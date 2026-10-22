// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/credential"
	dcgraphql "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// 🔴 AN ACCESS TOKEN IS NEVER STORED, ONLY ITS KEYED DIGEST. The token IS the credential a
// device presents, so a copy of the database that held it would impersonate every token
// device. Whatever a create or update carries, no column of the row it writes holds the
// token, and the one column a lookup matches is a digest that verifies it. Read through a
// raw row (every column, whatever the model maps), so a column the model stopped mapping
// cannot hide a plaintext.

const bearerToken = "9f6c1d0e-bearer-token-0001"

func createAccessToken(t *testing.T, api *Api, ctx context.Context, credToken, token string) *DeviceCredential {
	t.Helper()
	created, err := api.CreateDeviceCredential(ctx, &DeviceCredentialCreateRequest{
		Token: credToken, DeviceToken: "dev", CredentialType: string(CredentialAccessToken),
		CredentialId: token, Enabled: true,
	})
	require.NoError(t, err)
	return created
}

func requireNoColumnHoldsToken(t *testing.T, api *Api, credToken, token string) {
	t.Helper()
	var rows []map[string]any
	require.NoError(t, api.RDB.Database.Raw("SELECT * FROM device_credentials WHERE token = ?", credToken).Scan(&rows).Error)
	require.Len(t, rows, 1)
	for col, v := range rows[0] {
		require.NotContains(t, fmt.Sprint(v), token, "column %s holds the access token", col)
	}
	stored, _ := rows[0]["credential_id"].(string)
	require.True(t, credential.IsAccessTokenDigest(stored), "credential_id %q is not a digest", stored)
	require.NoError(t, credential.VerifyAccessToken(testSecretKey, partialUpdateTenant, stored, token),
		"credential_id %q does not verify the token", stored)
}

func TestNoColumnOfAnAccessTokenRowHoldsTheToken(t *testing.T) {
	api, ctx := resolveFixture(t)
	created := createAccessToken(t, api, ctx, "t-1", bearerToken)
	requireNoColumnHoldsToken(t, api, "t-1", bearerToken)

	// The write that set the token shows it once; no read does.
	require.Equal(t, bearerToken, created.IssuedCredentialId)
	rows, err := api.DeviceCredentialsByToken(ctx, []string{"t-1"})
	read := requireOne(t, "credential", rows, err)
	require.Empty(t, read.IssuedCredentialId)
	require.NotEqual(t, bearerToken, read.CredentialId)
	byId, err := api.DeviceCredentialsById(ctx, []uint{read.ID})
	require.NoError(t, err)
	require.Len(t, byId, 1)
	require.Empty(t, byId[0].IssuedCredentialId)
	require.NotEqual(t, bearerToken, byId[0].CredentialId)
	page, err := api.DeviceCredentials(ctx, DeviceCredentialSearchCriteria{Pagination: rdb.Pagination{PageNumber: 1, PageSize: 50}})
	require.NoError(t, err)
	for _, c := range page.Results {
		require.NotEqual(t, bearerToken, c.CredentialId)
		require.Empty(t, c.IssuedCredentialId)
	}

	// A rotation is the same: digested, and shown once on its own response.
	rotated, err := api.UpdateDeviceCredential(ctx, "t-1", &DeviceCredentialUpdateRequest{
		CredentialId: dcgraphql.OptionalStringOf("rotated-token-0002"),
	})
	require.NoError(t, err)
	require.Equal(t, "rotated-token-0002", rotated.IssuedCredentialId)
	requireNoColumnHoldsToken(t, api, "t-1", "rotated-token-0002")

	// An edit that does not name the id keeps the digest and shows nothing.
	edited, err := api.UpdateDeviceCredential(ctx, "t-1", &DeviceCredentialUpdateRequest{
		Enabled: dcgraphql.OptionalBoolOf(true),
	})
	require.NoError(t, err)
	require.Empty(t, edited.IssuedCredentialId)
	requireNoColumnHoldsToken(t, api, "t-1", "rotated-token-0002")
}

// The right token authenticates, and a wrong one does not, on every path that compares an
// access token: the per-event check, the MQTT connect, the cached per-event check and the
// finder. The wrong ones include the stored digest itself (what a database leak yields),
// a token that differs in case or by whitespace, and the right token in another tenant.
func TestAnAccessTokenAuthenticatesOnEveryPathAndAWrongOneDoesNot(t *testing.T) {
	f := newCredentialCacheFixture(t)
	other := core.WithTenant(context.Background(), "other")
	_, err := f.api.CreateDeviceType(other, &DeviceTypeCreateRequest{Token: "dt"})
	require.NoError(t, err)
	_, err = f.api.CreateDevice(other, &DeviceCreateRequest{Token: "theirs", DeviceTypeToken: "dt"})
	require.NoError(t, err)
	created := createAccessToken(t, f.api, f.ctx, "t-bearer", bearerToken)
	now := f.t0

	paths := map[string]func(ctx context.Context, token string) error{
		"AuthenticateDevice": func(ctx context.Context, token string) error {
			_, err := f.api.AuthenticateDevice(ctx, accessToken(token), now)
			return err
		},
		"AuthenticateDeviceConnect": func(ctx context.Context, token string) error {
			_, err := f.api.AuthenticateDeviceConnect(ctx, accessToken(token), now)
			return err
		},
		"CachedApi.AuthenticateDevice": func(ctx context.Context, token string) error {
			_, err := f.capi.AuthenticateDevice(ctx, accessToken(token), now)
			return err
		},
		"DeviceCredentialByCredentialId": func(ctx context.Context, token string) error {
			_, err := f.api.DeviceCredentialByCredentialId(ctx, string(CredentialAccessToken), token)
			return err
		},
	}
	for name, check := range paths {
		t.Run(name, func(t *testing.T) {
			require.NoError(t, check(f.ctx, bearerToken), "the right token is refused")
			// Twice: a cached copy must not change the answer.
			require.NoError(t, check(f.ctx, bearerToken), "the right token is refused on the second check")

			for what, wrong := range map[string]string{
				"a different token":        "9f6c1d0e-bearer-token-0002",
				"the token upper-cased":    strings.ToUpper(bearerToken),
				"the token with a suffix":  bearerToken + " ",
				"the token with a prefix":  " " + bearerToken,
				"the stored digest itself": created.CredentialId,
				"a truncated token":        bearerToken[:len(bearerToken)-1],
				"a token over the limit":   strings.Repeat("x", credential.MaxAccessTokenBytes+1),
			} {
				require.ErrorIs(t, check(f.ctx, wrong), wrongTokenRefusal(name),
					"%s: %s authenticates", name, what)
			}
			// The same token in another tenant is another credential, or none.
			require.Error(t, check(other, bearerToken), "the token authenticates in another tenant")
			// No tenant, no lookup.
			require.Error(t, check(context.Background(), bearerToken), "the token authenticates with no tenant")
		})
	}
}

// wrongTokenRefusal is the refusal a wrong token gets: the finder reports
// the row as not found, the authenticate paths as not resolved.
func wrongTokenRefusal(path string) error {
	if path == "DeviceCredentialByCredentialId" {
		return gorm.ErrRecordNotFound
	}
	return ErrCredentialNotResolved
}

// An access token carries no password to compare, so the password finder refuses it rather
// than resolve it with nothing left to check.
func TestAnAccessTokenIsNotResolvedAsAPassword(t *testing.T) {
	api, ctx := resolveFixture(t)
	createAccessToken(t, api, ctx, "t-1", bearerToken)
	_, _, err := api.ResolveDeviceCredential(ctx, accessToken(bearerToken), time.Now())
	require.ErrorIs(t, err, ErrCredentialTypeInvalid)
}

// A token the platform rotated stops working at once, and the new one starts.
func TestARotatedAccessTokenReplacesTheOldOne(t *testing.T) {
	api, ctx := resolveFixture(t)
	createAccessToken(t, api, ctx, "t-1", bearerToken)
	_, err := api.AuthenticateDevice(ctx, accessToken(bearerToken), time.Now())
	require.NoError(t, err)

	_, err = api.UpdateDeviceCredential(ctx, "t-1", &DeviceCredentialUpdateRequest{
		CredentialId: dcgraphql.OptionalStringOf("rotated-token-0002")})
	require.NoError(t, err)
	_, err = api.AuthenticateDevice(ctx, accessToken(bearerToken), time.Now())
	require.ErrorIs(t, err, ErrCredentialNotResolved, "the rotated-away token still authenticates")
	_, err = api.AuthenticateDeviceConnect(ctx, accessToken("rotated-token-0002"), time.Now())
	require.NoError(t, err)
}

// The stored id of an ACCESS_TOKEN is a digest and of any other type the id itself, so a
// change to or from ACCESS_TOKEN has to name the id; carrying the stored one across would
// read a username as a digest or a digest as a username.
func TestChangingATypeToOrFromAccessTokenNeedsACredentialId(t *testing.T) {
	api, ctx := resolveFixture(t) // c-1: MQTT_BASIC, username cred-1
	createAccessToken(t, api, ctx, "t-1", bearerToken)

	_, err := api.UpdateDeviceCredential(ctx, "c-1", &DeviceCredentialUpdateRequest{
		CredentialType: dcgraphql.OptionalStringOf(string(CredentialAccessToken))})
	require.ErrorIs(t, err, ErrCredentialIdRequiredForType)
	require.Equal(t, "INVALID_VALUE", ErrCredentialIdRequiredForType.Extensions()["code"])
	_, err = api.UpdateDeviceCredential(ctx, "t-1", &DeviceCredentialUpdateRequest{
		CredentialType:  dcgraphql.OptionalStringOf(string(CredentialMqttBasic)),
		CredentialValue: dcgraphql.OptionalStringOf("a-long-enough-password")})
	require.ErrorIs(t, err, ErrCredentialIdRequiredForType)

	// Refused changes left both credentials as they were.
	_, err = api.AuthenticateDevice(ctx, basic("cred-1", "s3cret-0123456789"), time.Now())
	require.NoError(t, err)
	_, err = api.AuthenticateDevice(ctx, accessToken(bearerToken), time.Now())
	require.NoError(t, err)

	// With the id named, both directions work and the id is stored for its new type.
	_, err = api.UpdateDeviceCredential(ctx, "c-1", &DeviceCredentialUpdateRequest{
		CredentialType: dcgraphql.OptionalStringOf(string(CredentialAccessToken)),
		CredentialId:   dcgraphql.OptionalStringOf("was-a-username-0003")})
	require.NoError(t, err)
	requireNoColumnHoldsToken(t, api, "c-1", "was-a-username-0003")
	_, err = api.UpdateDeviceCredential(ctx, "t-1", &DeviceCredentialUpdateRequest{
		CredentialType:  dcgraphql.OptionalStringOf(string(CredentialMqttBasic)),
		CredentialId:    dcgraphql.OptionalStringOf("now-a-username"),
		CredentialValue: dcgraphql.OptionalStringOf("a-long-enough-password")})
	require.NoError(t, err)
	_, err = api.AuthenticateDevice(ctx, basic("now-a-username", "a-long-enough-password"), time.Now())
	require.NoError(t, err)
	_, err = api.AuthenticateDevice(ctx, accessToken(bearerToken), time.Now())
	require.ErrorIs(t, err, ErrCredentialNotResolved)
}

// An operator holding a token can still find its credential, by the token or by the
// fingerprint a read returns; a token that is not stored finds nothing.
func TestACredentialIsSearchableByItsTokenOrItsFingerprint(t *testing.T) {
	api, ctx := resolveFixture(t)
	created := createAccessToken(t, api, ctx, "t-1", bearerToken)

	find := func(ctype *string, id string) int {
		page, err := api.DeviceCredentials(ctx, DeviceCredentialSearchCriteria{
			Pagination: rdb.Pagination{PageNumber: 1, PageSize: 50}, CredentialType: ctype, CredentialId: &id})
		require.NoError(t, err)
		return len(page.Results)
	}
	access, basicType := string(CredentialAccessToken), string(CredentialMqttBasic)
	require.Equal(t, 1, find(nil, bearerToken))
	require.Equal(t, 1, find(&access, bearerToken))
	require.Equal(t, 1, find(nil, created.CredentialId), "the fingerprint finds its own credential")
	require.Zero(t, find(&basicType, bearerToken))
	require.Zero(t, find(nil, "not-a-stored-token"))
	require.Equal(t, 1, find(nil, "cred-1"), "an MQTT username is still found as sent")
}

// A token over the column's width is refused with a typed limit, as the plaintext column
// refused it; the device presenting one that long matches nothing.
func TestAnOverlongAccessTokenIsRefused(t *testing.T) {
	api, ctx := resolveFixture(t)
	_, err := api.CreateDeviceCredential(ctx, &DeviceCredentialCreateRequest{
		Token: "t-long", DeviceToken: "dev", CredentialType: string(CredentialAccessToken),
		CredentialId: strings.Repeat("x", credential.MaxAccessTokenBytes+1), Enabled: true})
	require.Error(t, err)
	require.Contains(t, err.Error(), "credentialId")
	_, err = api.CreateDeviceCredential(ctx, &DeviceCredentialCreateRequest{
		Token: "t-edge", DeviceToken: "dev", CredentialType: string(CredentialAccessToken),
		CredentialId: strings.Repeat("x", credential.MaxAccessTokenBytes), Enabled: true})
	require.NoError(t, err, "a token exactly at the limit is accepted")
}

// The credential cache copies the row a read returned. It must hold neither the token nor
// its digest: a hit is keyed by a hash of what the device presented.
func TestTheCredentialCacheHoldsNoAccessToken(t *testing.T) {
	f := newCredentialCacheFixture(t)
	createAccessToken(t, f.api, f.ctx, "t-bearer", bearerToken)
	f.mustAuthenticate(t, accessToken(bearerToken), 1, "fill")

	f.cache.mu.Lock()
	held := 0
	for _, el := range f.cache.byKey {
		e := el.Value.(*credentialEntry)
		held++
		dump := fmt.Sprintf("%+v %+v", e.cred, e.device)
		require.NotContains(t, dump, bearerToken, "a cache entry holds the access token")
		require.NotContains(t, dump, "dctk1$", "a cache entry holds an access token digest")
	}
	f.cache.mu.Unlock()
	require.GreaterOrEqual(t, held, 1)

	_, err, reads := f.check(accessToken(bearerToken + "x"))
	require.ErrorIs(t, err, ErrCredentialNotResolved)
	require.Equal(t, 1, reads, "a wrong token is not answered from memory")
}

// With no key an access token is neither stored nor looked up.
func TestWithNoKeyAnAccessTokenIsNeitherStoredNorChecked(t *testing.T) {
	api, ctx := resolveFixture(t)
	createAccessToken(t, api, ctx, "t-1", bearerToken)
	api.DeviceSecretKey = nil

	_, err := api.CreateDeviceCredential(ctx, &DeviceCredentialCreateRequest{
		Token: "t-2", DeviceToken: "dev", CredentialType: string(CredentialAccessToken),
		CredentialId: "another-token-0004", Enabled: true})
	require.ErrorIs(t, err, errNoDeviceSecretKey)
	_, err = api.AuthenticateDevice(ctx, accessToken(bearerToken), time.Now())
	require.ErrorIs(t, err, errNoDeviceSecretKey)
	require.False(t, IsCredentialRefusal(err), "a missing key is the platform's failure, not a device's wrong answer")
}
