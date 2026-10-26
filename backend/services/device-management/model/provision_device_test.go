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
	"github.com/devicechain-io/dc-microservice/credential/credentialtest"
	dcgraphql "github.com/devicechain-io/dc-microservice/graphql"
	putest "github.com/devicechain-io/dc-microservice/rdb/partialupdatetest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// 🔴 THESE DRIVE THE REAL ENTRY POINTS (AuthenticateProvisioningProfile, ProvisionDevice and
// the operator calls) AND ASSERT WHAT LANDED IN THE TABLES, not what a helper returned. Every
// refusal asserts that it wrote NOTHING: no device, no credential, no audit row of any kind.
//
// The SQLite fixture cannot measure a lock (it drops FOR UPDATE), so what lives here is the
// DECISION: the precedence order, the cap, the replay compare, the earmark rules and the
// bookkeeping. The locks, the unique-violation retry and the races are the integration tests'
// (provision_device_integration_test.go) on a real Postgres.

// provisioningChecker is the Checker the way processor.NewProvisioningChecker builds it, over
// a recording store so a test can see that nothing was written to it.
func provisioningChecker(t testing.TB, device, provisioning *credential.DeviceSecretKey) (*credential.Checker, *credentialtest.Store) {
	t.Helper()
	store := credentialtest.NewStore()
	c, err := credential.NewChecker(store, map[credential.Kind]credential.Policy{
		credential.KindProvisioningProfile:   {Unthrottled: true},
		credential.KindProvisionedCredential: {Unthrottled: true},
	}, credential.WithDeviceSecretKey(device), credential.WithProvisioningSecretKey(provisioning))
	require.NoError(t, err)
	return c, store
}

// provFixture is one tenant ("acme") with two device types and an Api wired as main wires it.
type provFixture struct {
	t     *testing.T
	api   *Api
	ctx   context.Context
	store *credentialtest.Store
	// secrets are the issued provision secrets by profile token.
	secrets map[string]string
	keys    map[string]string
}

func newProvFixture(t *testing.T) *provFixture {
	t.Helper()
	api := newPartialUpdateApi(t, append(append([]any{}, deviceProfileTables...),
		&ProvisioningProfile{}, &DeviceCredential{}, &Integration{}, &IntegrationDeviceType{})...)
	ctx := partialUpdateCtx()
	var store *credentialtest.Store
	api.ProvisioningChecker, store = provisioningChecker(t, testSecretKey, testEnrollmentKeys.Provisioning)
	for _, tok := range []string{"dt-a", "dt-b"} {
		_, err := api.CreateDeviceType(ctx, &DeviceTypeCreateRequest{Token: tok})
		require.NoError(t, err)
	}
	return &provFixture{t: t, api: api, ctx: ctx, store: store, secrets: map[string]string{}, keys: map[string]string{}}
}

// profile creates a profile of the given strategy and type, enabled, uncapped.
func (f *provFixture) profile(token string, strategy ProvisioningStrategy, mutate ...func(*ProvisioningProfileCreateRequest)) *ProvisioningProfile {
	f.t.Helper()
	req := &ProvisioningProfileCreateRequest{
		Token: token, ProvisionKey: "key-" + token, Strategy: string(strategy), DeviceTypeToken: "dt-a", Enabled: true,
	}
	for _, m := range mutate {
		m(req)
	}
	created, err := f.api.CreateProvisioningProfile(f.ctx, req)
	require.NoError(f.t, err)
	f.secrets[token] = created.IssuedSecret
	f.keys[token] = req.ProvisionKey
	return created
}

// device creates a device, earmarked for profile when it is not "".
func (f *provFixture) device(token, external, deviceType, profile string) *Device {
	f.t.Helper()
	req := &DeviceCreateRequest{Token: token, ExternalId: &external, DeviceTypeToken: deviceType}
	if profile != "" {
		req.ProvisioningProfileToken = &profile
	}
	d, err := f.api.CreateDevice(f.ctx, req)
	require.NoError(f.t, err)
	return d
}

// material is a valid registration material, distinct per n.
func material(n int) string { return fmt.Sprintf("material-%030d", n) }

// register authenticates against the profile as the endpoint does and then registers.
func (f *provFixture) register(profile, external, mat string) (*ProvisionResult, error) {
	f.t.Helper()
	p, err := f.api.AuthenticateProvisioningProfile(f.ctx, f.keys[profile], f.secrets[profile])
	if err != nil {
		return nil, err
	}
	return f.api.ProvisionDevice(f.ctx, &ProvisionRequest{Profile: p, ExternalId: external, CredentialSecret: mat})
}

// counts is every row a registration could have written.
type provCounts struct{ devices, credentials, audit int64 }

func (f *provFixture) counts() provCounts {
	f.t.Helper()
	var c provCounts
	db := f.api.RDB.Database.WithContext(core.WithSystemContext(context.Background()))
	require.NoError(f.t, db.Raw(`SELECT count(*) FROM devices`).Scan(&c.devices).Error)
	require.NoError(f.t, db.Raw(`SELECT count(*) FROM device_credentials`).Scan(&c.credentials).Error)
	require.NoError(f.t, db.Raw(`SELECT count(*) FROM audit_events`).Scan(&c.audit).Error)
	return c
}

// refused runs a registration that must be refused with the given refusal, and asserts that it
// wrote nothing at all.
func (f *provFixture) refused(want *ProvisioningRefusal, profile, external, mat string) {
	f.t.Helper()
	before := f.counts()
	res, err := f.register(profile, external, mat)
	require.Error(f.t, err, "registered %+v", res)
	require.ErrorIs(f.t, err, want, "got %v", err)
	var got *ProvisioningRefusal
	require.True(f.t, errors.As(err, &got))
	assert.Equal(f.t, want.Code, got.Code)
	assert.Equal(f.t, want.HTTPStatus, got.HTTPStatus)
	assert.Equal(f.t, want.Reason, got.Reason)
	assert.Equal(f.t, before, f.counts(), "a refused registration wrote rows")
}

func (f *provFixture) sql(query string, args ...any) {
	f.t.Helper()
	require.NoError(f.t, f.api.RDB.Database.WithContext(core.WithSystemContext(context.Background())).Exec(query, args...).Error)
}

func (f *provFixture) credentialsOf(deviceToken string) []DeviceCredential {
	f.t.Helper()
	var out []DeviceCredential
	require.NoError(f.t, f.api.RDB.DB(f.ctx).
		Where("device_id = (?)", f.api.RDB.DB(f.ctx).Model(&Device{}).Select("id").Where("token = ?", deviceToken)).
		Order("id ASC").Find(&out).Error)
	return out
}

func (f *provFixture) deviceByExternal(external string) *Device {
	f.t.Helper()
	rows, err := f.api.DevicesByExternalId(f.ctx, []string{external})
	require.NoError(f.t, err)
	require.Len(f.t, rows, 1, external)
	return rows[0]
}

func (f *provFixture) profileID(token string) uint {
	f.t.Helper()
	rows, err := f.api.ProvisioningProfilesByToken(f.ctx, []string{token})
	require.NoError(f.t, err)
	require.Len(f.t, rows, 1)
	return rows[0].ID
}

// ─── The outcomes that write ───────────────────────────────────────────────

// ALLOW_NEW brings an unknown device into existence: the profile's type, earmarked for the
// profile, under the derived token, with one credential whose id is the digest of the device's
// material and which names the profile that issued it.
func TestProvisionAnUnknownDeviceCreatesItUnderAllowNew(t *testing.T) {
	f := newProvFixture(t)
	f.profile("pp-1", ProvisionAllowNew)
	mat := material(1)

	res, err := f.register("pp-1", "SN-000123", mat)
	require.NoError(t, err)
	assert.Equal(t, ProvisionCreated, res.Outcome)
	assert.Equal(t, core.DeriveDeviceToken("SN-000123", "pv-"), res.DeviceToken)
	assert.Equal(t, "acme", res.Tenant)
	assert.Equal(t, "SN-000123", res.ExternalId)
	assert.Equal(t, "ACCESS_TOKEN", res.CredentialType)

	d := f.deviceByExternal("SN-000123")
	assert.Equal(t, res.DeviceToken, d.Token)
	assert.Equal(t, "dt-a", d.DeviceType.Token)
	require.NotNil(t, d.ExpectedProvisioningProfileId)
	assert.Equal(t, f.profileID("pp-1"), *d.ExpectedProvisioningProfileId)
	assert.False(t, d.IsDisabled())

	creds := f.credentialsOf(d.Token)
	require.Len(t, creds, 1)
	assert.Equal(t, "ACCESS_TOKEN", creds[0].CredentialType)
	assert.True(t, creds[0].Enabled)
	require.NotNil(t, creds[0].ProvisioningProfileId)
	assert.Equal(t, f.profileID("pp-1"), *creds[0].ProvisioningProfileId)
	// The material is never stored: the id is the keyed digest, and it authenticates the device.
	assert.NotContains(t, creds[0].CredentialId, mat)
	assert.Equal(t, testTokenDigest(partialUpdateTenant, mat), creds[0].CredentialId)
	_, err = f.api.AuthenticateDevice(f.ctx, &PresentedCredential{CredentialType: "ACCESS_TOKEN", CredentialId: mat}, time.Now())
	assert.NoError(t, err, "the device cannot connect with the material it registered")

	// Nothing the platform returns carries the material.
	assert.NotContains(t, fmt.Sprintf("%+v", res), mat)
}

// An earmarked device that holds no credential is ADOPTED under either strategy: the device row
// is untouched, and it gets exactly one credential.
func TestProvisionAnEarmarkedDeviceIsAdoptedUnderEitherStrategy(t *testing.T) {
	for _, strategy := range []ProvisioningStrategy{ProvisionAllowNew, ProvisionCheckPreProvisioned} {
		t.Run(string(strategy), func(t *testing.T) {
			f := newProvFixture(t)
			f.profile("pp-1", strategy)
			f.device("dev-1", "SN-1", "dt-a", "pp-1")
			before := f.counts()

			res, err := f.register("pp-1", "SN-1", material(1))
			require.NoError(t, err)
			assert.Equal(t, ProvisionAdopted, res.Outcome)
			assert.Equal(t, "dev-1", res.DeviceToken, "an adopted device keeps the token it has")

			after := f.counts()
			assert.Equal(t, before.devices, after.devices, "adopting created a device")
			assert.Equal(t, before.credentials+1, after.credentials)
			creds := f.credentialsOf("dev-1")
			require.Len(t, creds, 1)
			require.NotNil(t, creds[0].ProvisioningProfileId)
			assert.Equal(t, f.profileID("pp-1"), *creds[0].ProvisioningProfileId)
		})
	}
}

// CREATED and ADOPTED are journalled, once each, by the profile; the device and credential
// inserts carry the same actor; a REPLAYED writes nothing at all.
func TestProvisionJournalsCreateAndAdoptButNeverAReplay(t *testing.T) {
	f := newProvFixture(t)
	f.profile("pp-1", ProvisionAllowNew)
	f.device("dev-2", "SN-2", "dt-a", "pp-1")

	authEvents := func() []map[string]any {
		var rows []map[string]any
		require.NoError(t, f.api.RDB.Database.WithContext(core.WithSystemContext(context.Background())).
			Raw(`SELECT operation, actor, entity_label, tenant_id FROM audit_events WHERE category = 'auth' ORDER BY id`).Scan(&rows).Error)
		return rows
	}
	_, err := f.register("pp-1", "SN-1", material(1))
	require.NoError(t, err)
	_, err = f.register("pp-1", "SN-2", material(2))
	require.NoError(t, err)

	events := authEvents()
	require.Len(t, events, 2)
	assert.Equal(t, "device.provision.created", events[0]["operation"])
	assert.Equal(t, "device.provision.adopted", events[1]["operation"])
	for _, e := range events {
		assert.Equal(t, "provisioning-profile:pp-1", e["actor"])
		assert.Equal(t, "acme", e["tenant_id"])
	}
	assert.Equal(t, core.DeriveDeviceToken("SN-1", "pv-"), events[0]["entity_label"])
	assert.Equal(t, "dev-2", events[1]["entity_label"])

	// The rows the registrations wrote are journalled as the profile, not as nobody.
	var actors []string
	require.NoError(t, f.api.RDB.Database.WithContext(core.WithSystemContext(context.Background())).
		Raw(`SELECT DISTINCT actor FROM audit_events WHERE category = 'mutation' AND table_name IN ('device_credentials') AND operation = 'create'`).
		Scan(&actors).Error)
	assert.Equal(t, []string{"provisioning-profile:pp-1"}, actors, "the credential inserts carry no actor")

	// An exact replay: the same answer, and not one row of any kind.
	before := f.counts()
	res, err := f.register("pp-1", "SN-1", material(1))
	require.NoError(t, err)
	assert.Equal(t, ProvisionReplayed, res.Outcome)
	assert.Equal(t, before, f.counts(), "a replay wrote rows")
	assert.Len(t, authEvents(), 2, "a replay was journalled")
}

// ─── The refusals, in the order the steps are taken ────────────────────────

func TestProvisionAnUnknownDeviceUnderCheckPreProvisionedIsNotRegistered(t *testing.T) {
	f := newProvFixture(t)
	f.profile("pp-1", ProvisionCheckPreProvisioned)
	f.refused(ErrProvisioningNotRegistered, "pp-1", "SN-1", material(1))
	assert.Equal(t, 404, ErrProvisioningNotRegistered.HTTPStatus)
}

// A known device that was not earmarked for the profile is refused under EITHER strategy: ALLOW_NEW
// never takes over a device somebody else made.
func TestProvisionADeviceNotEarmarkedForTheProfileIsRefused(t *testing.T) {
	for _, strategy := range []ProvisioningStrategy{ProvisionAllowNew, ProvisionCheckPreProvisioned} {
		t.Run(string(strategy)+"/no earmark", func(t *testing.T) {
			f := newProvFixture(t)
			f.profile("pp-1", strategy)
			f.device("dev-1", "SN-1", "dt-a", "")
			f.refused(ErrProvisioningNotEarmarked, "pp-1", "SN-1", material(1))
		})
		t.Run(string(strategy)+"/another profile's earmark", func(t *testing.T) {
			f := newProvFixture(t)
			f.profile("pp-1", strategy)
			f.profile("pp-2", strategy)
			f.device("dev-1", "SN-1", "dt-a", "pp-2")
			f.refused(ErrProvisioningNotEarmarked, "pp-1", "SN-1", material(1))
		})
	}
}

func TestProvisionADisabledDeviceIsRefused(t *testing.T) {
	f := newProvFixture(t)
	f.profile("pp-1", ProvisionAllowNew)
	f.device("dev-1", "SN-1", "dt-a", "pp-1")
	_, err := f.api.UpdateDevice(f.ctx, "dev-1", &DeviceUpdateRequest{Enabled: dcgraphql.OptionalBoolOf(false)})
	require.NoError(t, err)
	f.refused(ErrProvisioningDeviceDisabled, "pp-1", "SN-1", material(1))
	assert.Equal(t, 403, ErrProvisioningDeviceDisabled.HTTPStatus)
}

func TestProvisionADeviceOfAnotherTypeIsRefused(t *testing.T) {
	f := newProvFixture(t)
	f.profile("pp-1", ProvisionAllowNew)
	f.device("dev-1", "SN-1", "dt-a", "pp-1")
	// Re-typed after the earmark: the earmark was checked at write time, the registration checks again.
	_, err := f.api.UpdateDevice(f.ctx, "dev-1", &DeviceUpdateRequest{DeviceTypeToken: dcgraphql.OptionalStringOf("dt-b")})
	require.NoError(t, err)
	f.refused(ErrProvisioningTypeMismatch, "pp-1", "SN-1", material(1))
}

// A credential this profile issued that was disabled, or has expired, is REVOKED, and stays so
// until the device's provisioning is reset.
func TestProvisionARevokedDeviceStaysRefusedUntilReset(t *testing.T) {
	for _, how := range []string{"disabled", "expired"} {
		t.Run(how, func(t *testing.T) {
			f := newProvFixture(t)
			f.profile("pp-1", ProvisionAllowNew)
			_, err := f.register("pp-1", "SN-1", material(1))
			require.NoError(t, err)
			cred := f.credentialsOf(core.DeriveDeviceToken("SN-1", "pv-"))[0]
			if how == "disabled" {
				f.sql(`UPDATE device_credentials SET enabled = 0 WHERE id = ?`, cred.ID)
			} else {
				f.sql(`UPDATE device_credentials SET expires_at = ? WHERE id = ?`, time.Now().Add(-time.Hour), cred.ID)
			}
			// Even the exact replay material: revoked beats replayed.
			f.refused(ErrProvisioningRevoked, "pp-1", "SN-1", material(1))
			f.refused(ErrProvisioningRevoked, "pp-1", "SN-1", material(2))
			assert.ErrorIs(t, ErrProvisioningRevoked, ErrProvisioningAlreadyProvisioned,
				"revoked is an ALREADY_PROVISIONED with a reason")

			// The way back: reset, then the unit registers again with fresh material.
			removed, err := f.api.ResetDeviceProvisioning(f.ctx, core.DeriveDeviceToken("SN-1", "pv-"))
			require.NoError(t, err)
			assert.Equal(t, 1, removed)
			res, err := f.register("pp-1", "SN-1", material(3))
			require.NoError(t, err)
			assert.Equal(t, ProvisionAdopted, res.Outcome, "the earmark survived the reset")
		})
	}
}

func TestProvisionAReplayIsIdempotentAndADifferentMaterialIsRefused(t *testing.T) {
	f := newProvFixture(t)
	f.profile("pp-1", ProvisionAllowNew)
	first, err := f.register("pp-1", "SN-1", material(1))
	require.NoError(t, err)
	require.Equal(t, ProvisionCreated, first.Outcome)

	for i := 0; i < 3; i++ {
		before := f.counts()
		again, err := f.register("pp-1", "SN-1", material(1))
		require.NoError(t, err)
		assert.Equal(t, ProvisionReplayed, again.Outcome)
		assert.Equal(t, first.DeviceToken, again.DeviceToken)
		assert.Equal(t, before, f.counts())
	}
	// A different material is somebody else (or the device lost its copy): not a replay.
	f.refused(ErrProvisioningAlreadyProvisioned, "pp-1", "SN-1", material(2))

	// Neither the replay compares nor the mismatches wrote an attempt record or charged anything.
	assert.Empty(t, f.store.Ops(), "a provisioning check touched the attempt store: %v", f.store.Ops())
}

func TestProvisionADeviceHoldingSomeOtherCredentialIsRefused(t *testing.T) {
	t.Run("an operator's credential", func(t *testing.T) {
		f := newProvFixture(t)
		f.profile("pp-1", ProvisionAllowNew)
		f.device("dev-1", "SN-1", "dt-a", "pp-1")
		_, err := f.api.CreateDeviceCredential(f.ctx, &DeviceCredentialCreateRequest{
			Token: "c-1", DeviceToken: "dev-1", CredentialType: "ACCESS_TOKEN", CredentialId: material(9), Enabled: true})
		require.NoError(t, err)
		f.refused(ErrProvisioningAlreadyProvisioned, "pp-1", "SN-1", material(1))
		// Not even its own material: it is the operator's, not this profile's issuance.
		f.refused(ErrProvisioningAlreadyProvisioned, "pp-1", "SN-1", material(9))
	})
	t.Run("another profile's credential", func(t *testing.T) {
		f := newProvFixture(t)
		f.profile("pp-1", ProvisionAllowNew)
		f.profile("pp-2", ProvisionAllowNew)
		_, err := f.register("pp-2", "SN-1", material(1))
		require.NoError(t, err)
		dev := core.DeriveDeviceToken("SN-1", "pv-")
		// Earmark it for pp-1 as well, so only the credential stands in the way.
		f.sql(`UPDATE devices SET expected_provisioning_profile_id = ? WHERE token = ?`, f.profileID("pp-1"), dev)
		f.refused(ErrProvisioningAlreadyProvisioned, "pp-1", "SN-1", material(1))
	})
	t.Run("a disabled operator credential still counts", func(t *testing.T) {
		f := newProvFixture(t)
		f.profile("pp-1", ProvisionAllowNew)
		f.device("dev-1", "SN-1", "dt-a", "pp-1")
		_, err := f.api.CreateDeviceCredential(f.ctx, &DeviceCredentialCreateRequest{
			Token: "c-1", DeviceToken: "dev-1", CredentialType: "ACCESS_TOKEN", CredentialId: material(9), Enabled: false})
		require.NoError(t, err)
		f.refused(ErrProvisioningAlreadyProvisioned, "pp-1", "SN-1", material(1))
	})
}

// ─── maxDevices ────────────────────────────────────────────────────────────

func TestProvisionRefusesPastTheCapForBothACreateAndAnAdopt(t *testing.T) {
	f := newProvFixture(t)
	two := int32(2)
	f.profile("pp-1", ProvisionAllowNew, func(r *ProvisioningProfileCreateRequest) { r.MaxDevices = &two })
	f.device("dev-adopt", "SN-ADOPT", "dt-a", "pp-1")

	for i := 1; i <= 2; i++ {
		res, err := f.register("pp-1", fmt.Sprintf("SN-%d", i), material(i))
		require.NoError(t, err)
		require.Equal(t, ProvisionCreated, res.Outcome)
	}
	// Full. An unknown device (a CREATE) and an earmarked one (an ADOPT) are both refused, and
	// the answer is 422.
	f.refused(ErrProvisioningLimitExceeded, "pp-1", "SN-3", material(3))
	f.refused(ErrProvisioningLimitExceeded, "pp-1", "SN-ADOPT", material(4))
	assert.Equal(t, 422, ErrProvisioningLimitExceeded.HTTPStatus)
	// A replay is not a new registration, so it is not counted against the cap.
	res, err := f.register("pp-1", "SN-1", material(1))
	require.NoError(t, err)
	assert.Equal(t, ProvisionReplayed, res.Outcome)

	// The cap counts revoked devices: revoking frees nothing.
	n, err := f.api.RevokeProvisionedDevices(f.ctx, "pp-1")
	require.NoError(t, err)
	assert.Equal(t, 2, n)
	f.refused(ErrProvisioningLimitExceeded, "pp-1", "SN-3", material(3))
	// Raising it admits more; removing it uncaps.
	_, err = f.api.UpdateProvisioningProfile(f.ctx, "pp-1", &ProvisioningProfileUpdateRequest{
		MaxDevices: dcgraphql.OptionalInt32Of(3)})
	require.NoError(t, err)
	res, err = f.register("pp-1", "SN-3", material(3))
	require.NoError(t, err)
	assert.Equal(t, ProvisionCreated, res.Outcome)
}

// Precedence: the cap is judged last, after the device's own state. A device that holds some
// other credential is ALREADY_PROVISIONED even on a full profile.
func TestProvisionPrecedenceBetweenEachAdjacentPair(t *testing.T) {
	setup := func(t *testing.T) (*provFixture, string) {
		f := newProvFixture(t)
		one := int32(1)
		f.profile("pp-1", ProvisionAllowNew, func(r *ProvisioningProfileCreateRequest) { r.MaxDevices = &one })
		// Fill the cap with another device so the cap condition holds for everything below.
		_, err := f.register("pp-1", "SN-FULL", material(100))
		require.NoError(t, err)
		return f, "SN-X"
	}
	disable := func(f *provFixture, token string) {
		_, err := f.api.UpdateDevice(f.ctx, token, &DeviceUpdateRequest{Enabled: dcgraphql.OptionalBoolOf(false)})
		require.NoError(t, err)
	}

	t.Run("1 disabled beats 2 type mismatch", func(t *testing.T) {
		f, ext := setup(t)
		f.device("dev-x", ext, "dt-b", "")
		// Wrong type AND earmarked: the earmark API refuses this pairing, so it is written below it.
		f.sql(`UPDATE devices SET expected_provisioning_profile_id = ? WHERE token = 'dev-x'`, f.profileID("pp-1"))
		disable(f, "dev-x")
		f.refused(ErrProvisioningDeviceDisabled, "pp-1", ext, material(1))
	})
	t.Run("2 type mismatch beats 3 not earmarked", func(t *testing.T) {
		f, ext := setup(t)
		f.device("dev-x", ext, "dt-b", "") // wrong type and no earmark
		f.refused(ErrProvisioningTypeMismatch, "pp-1", ext, material(1))
	})
	t.Run("3 not earmarked beats 4 revoked", func(t *testing.T) {
		f, _ := setup(t)
		dev := core.DeriveDeviceToken("SN-FULL", "pv-")
		f.sql(`UPDATE device_credentials SET enabled = 0`)
		f.sql(`UPDATE devices SET expected_provisioning_profile_id = NULL WHERE token = ?`, dev)
		f.refused(ErrProvisioningNotEarmarked, "pp-1", "SN-FULL", material(100))
	})
	t.Run("4 revoked beats 5 replayed", func(t *testing.T) {
		f, _ := setup(t)
		f.sql(`UPDATE device_credentials SET enabled = 0`)
		f.refused(ErrProvisioningRevoked, "pp-1", "SN-FULL", material(100))
	})
	t.Run("5 replayed beats 6 another credential", func(t *testing.T) {
		f, _ := setup(t)
		dev := core.DeriveDeviceToken("SN-FULL", "pv-")
		_, err := f.api.CreateDeviceCredential(f.ctx, &DeviceCredentialCreateRequest{
			Token: "c-op", DeviceToken: dev, CredentialType: "ACCESS_TOKEN", CredentialId: material(200), Enabled: true})
		require.NoError(t, err)
		res, err := f.register("pp-1", "SN-FULL", material(100))
		require.NoError(t, err)
		assert.Equal(t, ProvisionReplayed, res.Outcome)
	})
	t.Run("6 already provisioned beats 7 the cap", func(t *testing.T) {
		f, ext := setup(t)
		f.device("dev-x", ext, "dt-a", "pp-1")
		_, err := f.api.CreateDeviceCredential(f.ctx, &DeviceCredentialCreateRequest{
			Token: "c-op", DeviceToken: "dev-x", CredentialType: "ACCESS_TOKEN", CredentialId: material(200), Enabled: true})
		require.NoError(t, err)
		f.refused(ErrProvisioningAlreadyProvisioned, "pp-1", ext, material(1))
	})
	t.Run("7 the cap refuses an otherwise clean adopt", func(t *testing.T) {
		f, ext := setup(t)
		f.device("dev-x", ext, "dt-a", "pp-1")
		f.refused(ErrProvisioningLimitExceeded, "pp-1", ext, material(1))
	})
	t.Run("a disabled device with the replay material is refused, not replayed", func(t *testing.T) {
		f, _ := setup(t)
		disable(f, core.DeriveDeviceToken("SN-FULL", "pv-"))
		f.refused(ErrProvisioningDeviceDisabled, "pp-1", "SN-FULL", material(100))
	})
}

// ─── A profile that cannot register anything ───────────────────────────────

func TestProvisionAProfileWhoseTypeWasDeletedIsMisconfigured(t *testing.T) {
	f := newProvFixture(t)
	f.profile("pp-1", ProvisionAllowNew)
	f.device("dev-1", "SN-1", "dt-a", "pp-1")
	// The type is deleted behind the profile's back (the API refuses this; a restore or a bug
	// does not).
	f.sql(`DELETE FROM devices`)
	f.sql(`DELETE FROM device_types WHERE token = 'dt-a'`)
	for _, ext := range []string{"SN-1", "SN-NEW"} {
		f.refused(ErrProvisioningMisconfigured, "pp-1", ext, material(1))
	}
	assert.Equal(t, 500, ErrProvisioningMisconfigured.HTTPStatus)
}

func TestProvisionACorruptStrategyIsNeverReadAsAllowNew(t *testing.T) {
	f := newProvFixture(t)
	f.profile("pp-1", ProvisionAllowNew)
	f.sql(`UPDATE provisioning_profiles SET strategy = 'allow_new'`)
	f.refused(ErrProvisioningMisconfigured, "pp-1", "SN-1", material(1))
	f.sql(`UPDATE provisioning_profiles SET strategy = ''`)
	f.refused(ErrProvisioningMisconfigured, "pp-1", "SN-1", material(1))
}

func TestProvisionACredentialTypeThisBuildCannotMintIsTerminal(t *testing.T) {
	f := newProvFixture(t)
	f.profile("pp-1", ProvisionAllowNew)
	f.sql(`UPDATE provisioning_profiles SET credential_type = 'MQTT_BASIC'`)
	f.refused(ErrProvisioningCredentialTypeUnsupported, "pp-1", "SN-1", material(1))
	assert.Equal(t, 501, ErrProvisioningCredentialTypeUnsupported.HTTPStatus)
}

// The device token is derived from the external id, and another device may already hold it.
func TestProvisionADerivedTokenHeldByAnotherDeviceIsACollision(t *testing.T) {
	f := newProvFixture(t)
	f.profile("pp-1", ProvisionAllowNew)
	// An operator-made device with NO external id sitting on the token the registration would derive.
	_, err := f.api.CreateDevice(f.ctx, &DeviceCreateRequest{Token: core.DeriveDeviceToken("SN-1", "pv-"), DeviceTypeToken: "dt-a"})
	require.NoError(t, err)
	f.refused(ErrProvisioningTokenCollision, "pp-1", "SN-1", material(1))
	assert.Equal(t, 409, ErrProvisioningTokenCollision.HTTPStatus)
}

// ─── The material and the request ──────────────────────────────────────────

func TestProvisionRefusesMaterialOutsideTheFormatBeforeAnyWrite(t *testing.T) {
	f := newProvFixture(t)
	f.profile("pp-1", ProvisionAllowNew)
	p, err := f.api.AuthenticateProvisioningProfile(f.ctx, f.keys["pp-1"], f.secrets["pp-1"])
	require.NoError(t, err)

	ok := strings.Repeat("a", 22)
	for name, mat := range map[string]string{
		"too short":       strings.Repeat("a", 21),
		"too long":        strings.Repeat("a", 257),
		"padding":         ok + "=",
		"standard base64": ok + "+/",
		"a space":         ok + " ",
		"unicode":         ok + "é",
		"empty":           "",
	} {
		before := f.counts()
		_, err := f.api.ProvisionDevice(f.ctx, &ProvisionRequest{Profile: p, ExternalId: "SN-1", CredentialSecret: mat})
		var refusal *ProvisioningRefusal
		require.ErrorAs(t, err, &refusal, name)
		assert.Equal(t, ProvisioningCodeInvalid, refusal.Code, name)
		assert.Equal(t, 400, refusal.HTTPStatus, name)
		assert.NotContains(t, refusal.Error(), mat+"x", name)
		assert.Equal(t, before, f.counts(), name)
	}
	// The counterweights: the floor and the ceiling themselves are accepted.
	for name, mat := range map[string]string{"22": ok, "256": strings.Repeat("a", 256), "-_": ok + "-_"} {
		_, err := f.api.ProvisionDevice(f.ctx, &ProvisionRequest{Profile: p, ExternalId: "SN-" + name, CredentialSecret: mat})
		assert.NoError(t, err, name)
	}
}

func TestProvisionRefusesABadExternalIdNameOrMetadata(t *testing.T) {
	f := newProvFixture(t)
	f.profile("pp-1", ProvisionAllowNew)
	p, err := f.api.AuthenticateProvisioningProfile(f.ctx, f.keys["pp-1"], f.secrets["pp-1"])
	require.NoError(t, err)
	long := strings.Repeat("x", 300)
	bad := "{not json"
	for name, req := range map[string]*ProvisionRequest{
		"blank external id": {ExternalId: "  "},
		"long external id":  {ExternalId: long},
		"long name":         {ExternalId: "SN-1", Name: &long},
		"bad metadata":      {ExternalId: "SN-1", Metadata: &bad},
	} {
		req.Profile, req.CredentialSecret = p, material(1)
		before := f.counts()
		_, err := f.api.ProvisionDevice(f.ctx, req)
		var refusal *ProvisioningRefusal
		require.ErrorAs(t, err, &refusal, name)
		assert.Equal(t, ProvisioningCodeInvalid, refusal.Code, name)
		assert.Equal(t, before, f.counts(), name)
	}
	// And a name and metadata that are fine land on the device the call creates.
	name, md := "Boiler 7", `{"site":"north"}`
	_, err = f.api.ProvisionDevice(f.ctx, &ProvisionRequest{Profile: p, ExternalId: "SN-OK", CredentialSecret: material(2), Name: &name, Metadata: &md})
	require.NoError(t, err)
	d := f.deviceByExternal("SN-OK")
	assert.Equal(t, name, d.Name.String)
	assert.JSONEq(t, md, string(*d.Metadata))
}

// ─── Authentication: negative controls built from a request that succeeds ──

// Each control mutates ONE field of a request the paired positive proves succeeds, and the
// answer is the same generic refusal for all of them.
func TestAuthenticateProvisioningProfileNegativeControls(t *testing.T) {
	f := newProvFixture(t)
	f.profile("pp-1", ProvisionAllowNew)
	key, secret := f.keys["pp-1"], f.secrets["pp-1"]

	// The paired positive.
	p, err := f.api.AuthenticateProvisioningProfile(f.ctx, key, secret)
	require.NoError(t, err)
	assert.Equal(t, "pp-1", p.Token)
	assert.Empty(t, p.IssuedSecret, "the authenticated profile carries a secret")

	mustFail := func(what string, ctx context.Context, key, secret string) {
		t.Helper()
		_, err := f.api.AuthenticateProvisioningProfile(ctx, key, secret)
		require.ErrorIs(t, err, ErrProvisioningFailed, what)
		var refusal *ProvisioningRefusal
		require.ErrorAs(t, err, &refusal, what)
		assert.Equal(t, 401, refusal.HTTPStatus, what)
		assert.Equal(t, ErrProvisioningFailed.Error(), err.Error(), "%s: the answer differs", what)
	}

	mustFail("wrong secret", f.ctx, key, secret+"x")
	mustFail("a secret of the right length", f.ctx, key, strings.Repeat("A", len(secret)))
	mustFail("empty secret", f.ctx, key, "")
	mustFail("unknown key", f.ctx, "no-such-key", secret)
	mustFail("another tenant's path", core.WithTenant(context.Background(), "beta"), key, secret)

	// A profile that is disabled, expired, or unmarked answers exactly as an unknown key does,
	// even with the right secret.
	f.sql(`UPDATE provisioning_profiles SET enabled = 0`)
	mustFail("disabled", f.ctx, key, secret)
	f.sql(`UPDATE provisioning_profiles SET enabled = 1`)
	_, err = f.api.AuthenticateProvisioningProfile(f.ctx, key, secret)
	require.NoError(t, err, "the control profile no longer authenticates")

	f.sql(`UPDATE provisioning_profiles SET expires_at = ?`, time.Now().Add(-time.Minute))
	mustFail("expired", f.ctx, key, secret)
	f.sql(`UPDATE provisioning_profiles SET expires_at = ?`, time.Now().Add(time.Hour))
	_, err = f.api.AuthenticateProvisioningProfile(f.ctx, key, secret)
	require.NoError(t, err, "a profile that has not expired was refused")

	f.sql(`UPDATE provisioning_profiles SET provision_secret_issued = 0`)
	mustFail("unmarked, with the correct secret", f.ctx, key, secret)
	f.sql(`UPDATE provisioning_profiles SET provision_secret_issued = 1`)
	_, err = f.api.AuthenticateProvisioningProfile(f.ctx, key, secret)
	require.NoError(t, err)

	// A profile with no digest at all matches nothing.
	f.sql(`UPDATE provisioning_profiles SET provision_secret_digest = NULL`)
	mustFail("no digest", f.ctx, key, secret)

	assert.Empty(t, f.store.Ops(), "authentication touched the attempt store")
}

// A digest copied from one tenant's row into another's verifies nothing there.
func TestAProvisioningDigestTransplantedAcrossTenantsVerifiesNothing(t *testing.T) {
	f := newProvFixture(t)
	f.profile("pp-1", ProvisionAllowNew)
	beta := core.WithTenant(context.Background(), "beta")
	_, err := f.api.CreateDeviceType(beta, &DeviceTypeCreateRequest{Token: "dt-a"})
	require.NoError(t, err)
	// Beta's profile has the SAME key; it is its own (a key is unique per tenant, not globally).
	created, err := f.api.CreateProvisioningProfile(beta, &ProvisioningProfileCreateRequest{
		Token: "pp-b", ProvisionKey: f.keys["pp-1"], Strategy: string(ProvisionAllowNew), DeviceTypeToken: "dt-a", Enabled: true})
	require.NoError(t, err)
	_, err = f.api.AuthenticateProvisioningProfile(beta, f.keys["pp-1"], created.IssuedSecret)
	require.NoError(t, err, "a tenant could not use a key another tenant also holds")
	// Acme's secret is not beta's, and the reverse.
	_, err = f.api.AuthenticateProvisioningProfile(beta, f.keys["pp-1"], f.secrets["pp-1"])
	require.ErrorIs(t, err, ErrProvisioningFailed)
	_, err = f.api.AuthenticateProvisioningProfile(f.ctx, f.keys["pp-1"], created.IssuedSecret)
	require.ErrorIs(t, err, ErrProvisioningFailed)

	// Now the transplant: acme's digest written over beta's row.
	var digest string
	require.NoError(t, f.api.RDB.Database.WithContext(core.WithSystemContext(context.Background())).
		Raw(`SELECT provision_secret_digest FROM provisioning_profiles WHERE tenant_id = 'acme'`).Scan(&digest).Error)
	f.sql(`UPDATE provisioning_profiles SET provision_secret_digest = ? WHERE tenant_id = 'beta'`, digest)
	_, err = f.api.AuthenticateProvisioningProfile(beta, f.keys["pp-1"], f.secrets["pp-1"])
	require.ErrorIs(t, err, ErrProvisioningFailed, "acme's digest, copied into beta's row, verified there")
}

// The key guard: a Checker built with the wrong key in either slot fails the same calls that
// succeed under the right ones.
func TestTheCheckerKeysDecideWhatVerifies(t *testing.T) {
	f := newProvFixture(t)
	f.profile("pp-1", ProvisionAllowNew)
	_, err := f.register("pp-1", "SN-1", material(1))
	require.NoError(t, err)

	t.Run("the right keys", func(t *testing.T) {
		res, err := f.register("pp-1", "SN-1", material(1))
		require.NoError(t, err)
		assert.Equal(t, ProvisionReplayed, res.Outcome)
	})
	t.Run("the claim key where the provisioning key belongs", func(t *testing.T) {
		f.api.ProvisioningChecker, _ = provisioningChecker(t, testSecretKey, testEnrollmentKeys.Claim)
		_, err := f.register("pp-1", "SN-1", material(1))
		assert.ErrorIs(t, err, ErrProvisioningFailed)
	})
	t.Run("the device key where the provisioning key belongs", func(t *testing.T) {
		f.api.ProvisioningChecker, _ = provisioningChecker(t, testSecretKey, testSecretKey)
		_, err := f.register("pp-1", "SN-1", material(1))
		assert.ErrorIs(t, err, ErrProvisioningFailed)
	})
	t.Run("the provisioning key where the device key belongs: a replay is not recognised", func(t *testing.T) {
		f.api.ProvisioningChecker, _ = provisioningChecker(t, testEnrollmentKeys.Provisioning, testEnrollmentKeys.Provisioning)
		_, err := f.register("pp-1", "SN-1", material(1))
		assert.ErrorIs(t, err, ErrProvisioningAlreadyProvisioned,
			"the replay compare accepted a digest under the wrong key")
	})
}

func TestProvisioningFailsClosedWithoutItsKeysOrItsChecker(t *testing.T) {
	f := newProvFixture(t)
	f.profile("pp-1", ProvisionAllowNew)
	p, err := f.api.AuthenticateProvisioningProfile(f.ctx, f.keys["pp-1"], f.secrets["pp-1"])
	require.NoError(t, err)

	checker := f.api.ProvisioningChecker
	f.api.ProvisioningChecker = nil
	_, err = f.api.AuthenticateProvisioningProfile(f.ctx, f.keys["pp-1"], f.secrets["pp-1"])
	assert.ErrorIs(t, err, ErrProvisioningUnavailable)
	_, err = f.api.ProvisionDevice(f.ctx, &ProvisionRequest{Profile: p, ExternalId: "SN-1", CredentialSecret: material(1)})
	assert.ErrorIs(t, err, ErrProvisioningUnavailable)
	assert.Equal(t, 503, ErrProvisioningUnavailable.HTTPStatus)

	f.api.ProvisioningChecker = checker
	f.api.DeviceSecretKey = nil
	_, err = f.api.ProvisionDevice(f.ctx, &ProvisionRequest{Profile: p, ExternalId: "SN-1", CredentialSecret: material(1)})
	assert.ErrorIs(t, err, ErrProvisioningUnavailable)
	assert.Zero(t, f.counts().devices)
}

// What was authenticated must still hold under the profile's lock: a profile disabled, expired
// or rotated between the two calls is refused as an unknown key would be.
func TestProvisionRefusesAProfileThatChangedAfterItWasAuthenticated(t *testing.T) {
	setup := func(t *testing.T) (*provFixture, *ProvisioningProfile) {
		f := newProvFixture(t)
		f.profile("pp-1", ProvisionAllowNew)
		p, err := f.api.AuthenticateProvisioningProfile(f.ctx, f.keys["pp-1"], f.secrets["pp-1"])
		require.NoError(t, err)
		return f, p
	}
	attempt := func(f *provFixture, p *ProvisioningProfile) error {
		_, err := f.api.ProvisionDevice(f.ctx, &ProvisionRequest{Profile: p, ExternalId: "SN-1", CredentialSecret: material(1)})
		return err
	}

	t.Run("control: unchanged", func(t *testing.T) {
		f, p := setup(t)
		require.NoError(t, attempt(f, p))
	})
	t.Run("rotated", func(t *testing.T) {
		f, p := setup(t)
		_, err := f.api.RotateProvisioningProfileSecret(f.ctx, "pp-1")
		require.NoError(t, err)
		assert.ErrorIs(t, attempt(f, p), ErrProvisioningFailed)
		assert.Zero(t, f.counts().devices)
	})
	t.Run("disabled", func(t *testing.T) {
		f, p := setup(t)
		_, err := f.api.UpdateProvisioningProfile(f.ctx, "pp-1", &ProvisioningProfileUpdateRequest{Enabled: dcgraphql.OptionalBoolOf(false)})
		require.NoError(t, err)
		assert.ErrorIs(t, attempt(f, p), ErrProvisioningFailed)
	})
	t.Run("expired", func(t *testing.T) {
		f, p := setup(t)
		f.sql(`UPDATE provisioning_profiles SET expires_at = ?`, time.Now().Add(-time.Second))
		assert.ErrorIs(t, attempt(f, p), ErrProvisioningFailed)
	})
	t.Run("deleted", func(t *testing.T) {
		f, p := setup(t)
		f.sql(`DELETE FROM provisioning_profiles`)
		assert.ErrorIs(t, attempt(f, p), ErrProvisioningFailed)
	})
}

// A REPLAYED writes nothing, so the write-time erasure fence never runs; it is checked
// explicitly, and a tenant being erased is told what an unknown key is told.
func TestProvisionAReplayInAnErasedTenantIsRefused(t *testing.T) {
	f := newProvFixture(t)
	f.profile("pp-1", ProvisionAllowNew)
	_, err := f.register("pp-1", "SN-1", material(1))
	require.NoError(t, err)
	res, err := f.register("pp-1", "SN-1", material(1))
	require.NoError(t, err)
	require.Equal(t, ProvisionReplayed, res.Outcome)

	f.sql(`INSERT INTO purged_tenants (token, epoch, planted_at) VALUES ('acme', ?, ?)`, time.Now(), time.Now())
	p, err := f.api.AuthenticateProvisioningProfile(f.ctx, f.keys["pp-1"], f.secrets["pp-1"])
	require.NoError(t, err, "authentication reads no fence; the registration does")
	_, err = f.api.ProvisionDevice(f.ctx, &ProvisionRequest{Profile: p, ExternalId: "SN-1", CredentialSecret: material(1)})
	assert.ErrorIs(t, err, ErrProvisioningFailed, "a replay in an erased tenant was answered")
	// And a write is refused by the admission hook, answered the same way.
	_, err = f.api.ProvisionDevice(f.ctx, &ProvisionRequest{Profile: p, ExternalId: "SN-2", CredentialSecret: material(2)})
	assert.ErrorIs(t, err, ErrProvisioningFailed, "a registration was written in an erased tenant")
}

// ─── Profile bookkeeping ───────────────────────────────────────────────────

func TestTheIssuedMarkerIsSetByCreateAndRotateAndNeverByAnUpdate(t *testing.T) {
	f := newProvFixture(t)
	created := f.profile("pp-1", ProvisionAllowNew)
	assert.True(t, created.ProvisionSecretIssued)
	assert.Len(t, created.IssuedSecret, 43)

	// A profile converted from an operator-chosen secret (what #1899 left behind) is unmarked,
	// and no update marks it.
	f.sql(`UPDATE provisioning_profiles SET provision_secret_issued = 0`)
	_, err := f.api.UpdateProvisioningProfile(f.ctx, "pp-1", &ProvisioningProfileUpdateRequest{
		Name: dcgraphql.OptionalStringOf("renamed"), Enabled: dcgraphql.OptionalBoolOf(true)})
	require.NoError(t, err)
	rows, err := f.api.ProvisioningProfilesByToken(f.ctx, []string{"pp-1"})
	require.False(t, requireOne(t, "profile", rows, err).ProvisionSecretIssued, "an update marked the secret as issued")
	_, err = f.api.AuthenticateProvisioningProfile(f.ctx, f.keys["pp-1"], f.secrets["pp-1"])
	require.ErrorIs(t, err, ErrProvisioningFailed, "an unmarked profile authenticated")

	// Rotation marks it, returns a new secret once, and the old one stops working at once.
	rotated, err := f.api.RotateProvisioningProfileSecret(f.ctx, "pp-1")
	require.NoError(t, err)
	assert.True(t, rotated.ProvisionSecretIssued)
	assert.Len(t, rotated.IssuedSecret, 43)
	assert.NotEqual(t, created.IssuedSecret, rotated.IssuedSecret)
	_, err = f.api.AuthenticateProvisioningProfile(f.ctx, f.keys["pp-1"], created.IssuedSecret)
	assert.ErrorIs(t, err, ErrProvisioningFailed, "the old secret still works after a rotation")
	_, err = f.api.AuthenticateProvisioningProfile(f.ctx, f.keys["pp-1"], rotated.IssuedSecret)
	assert.NoError(t, err)

	// Rotating an unknown profile is not found.
	_, err = f.api.RotateProvisioningProfileSecret(f.ctx, "nope")
	assert.Error(t, err)
}

func TestTwoProfilesNeverShareASecretAndEachSecretIs256Bits(t *testing.T) {
	f := newProvFixture(t)
	seen := map[string]bool{}
	for i := 0; i < 50; i++ {
		p := f.profile(fmt.Sprintf("pp-%d", i), ProvisionAllowNew)
		assert.Len(t, p.IssuedSecret, 43)
		assert.Regexp(t, `^[A-Za-z0-9_-]{43}$`, p.IssuedSecret)
		assert.False(t, seen[p.IssuedSecret], "a generated secret repeated")
		seen[p.IssuedSecret] = true
	}
}

func TestACapBelowOneIsRefused(t *testing.T) {
	f := newProvFixture(t)
	zero := int32(0)
	_, err := f.api.CreateProvisioningProfile(f.ctx, &ProvisioningProfileCreateRequest{
		Token: "pp-1", ProvisionKey: "k", Strategy: string(ProvisionAllowNew), DeviceTypeToken: "dt-a", Enabled: true, MaxDevices: &zero})
	assert.ErrorIs(t, err, ErrProvisioningMaxDevices)
	f.profile("pp-2", ProvisionAllowNew)
	_, err = f.api.UpdateProvisioningProfile(f.ctx, "pp-2", &ProvisioningProfileUpdateRequest{MaxDevices: dcgraphql.OptionalInt32Of(0)})
	assert.ErrorIs(t, err, ErrProvisioningMaxDevices)
	// Null removes the cap.
	_, err = f.api.UpdateProvisioningProfile(f.ctx, "pp-2", &ProvisioningProfileUpdateRequest{MaxDevices: dcgraphql.ClearedInt32()})
	assert.NoError(t, err)
}

func TestProvisionedDeviceCountCountsDevicesNotCredentials(t *testing.T) {
	f := newProvFixture(t)
	f.profile("pp-1", ProvisionAllowNew)
	f.profile("pp-2", ProvisionAllowNew)
	for i := 1; i <= 3; i++ {
		_, err := f.register("pp-1", fmt.Sprintf("SN-%d", i), material(i))
		require.NoError(t, err)
	}
	_, err := f.register("pp-2", "SN-9", material(9))
	require.NoError(t, err)
	// A second credential on one device does not make it two devices.
	dev := core.DeriveDeviceToken("SN-1", "pv-")
	f.sql(`INSERT INTO device_credentials (tenant_id, token, device_id, credential_type, credential_id, enabled, provisioning_profile_id)
	       SELECT tenant_id, 'extra', id, 'ACCESS_TOKEN', 'zzz', 1, ? FROM devices WHERE token = ?`, f.profileID("pp-1"), dev)
	n, err := f.api.ProvisionedDeviceCount(f.ctx, f.profileID("pp-1"))
	require.NoError(t, err)
	assert.EqualValues(t, 3, n)
	n, err = f.api.ProvisionedDeviceCount(f.ctx, f.profileID("pp-2"))
	require.NoError(t, err)
	assert.EqualValues(t, 1, n)
}

// ─── Earmarks ──────────────────────────────────────────────────────────────

func TestAnEarmarkNamesAProfileThatExistsAndSharesTheDevicesType(t *testing.T) {
	f := newProvFixture(t)
	f.profile("pp-a", ProvisionAllowNew) // dt-a

	// The control: the same request with a valid profile succeeds.
	f.device("dev-ok", "SN-OK", "dt-a", "pp-a")
	d := f.deviceByExternal("SN-OK")
	require.NotNil(t, d.ExpectedProvisioningProfileId)
	assert.Equal(t, f.profileID("pp-a"), *d.ExpectedProvisioningProfileId)

	for name, req := range map[string]*DeviceCreateRequest{
		"a profile that does not exist":    {Token: "d1", DeviceTypeToken: "dt-a", ProvisioningProfileToken: strPtr("nope")},
		"a profile of another device type": {Token: "d2", DeviceTypeToken: "dt-b", ProvisioningProfileToken: strPtr("pp-a")},
	} {
		_, err := f.api.CreateDevice(f.ctx, req)
		require.Error(t, err, name)
		assert.ErrorIs(t, err, ErrInvalidEarmark, name)
	}
	rows, err := f.api.DevicesByToken(f.ctx, []string{"d1", "d2"})
	require.NoError(t, err)
	assert.Empty(t, rows, "a refused earmark still created the device")

	// Blank is no earmark.
	blank := "  "
	dev, err := f.api.CreateDevice(f.ctx, &DeviceCreateRequest{Token: "d3", DeviceTypeToken: "dt-a", ProvisioningProfileToken: &blank})
	require.NoError(t, err)
	assert.Nil(t, dev.ExpectedProvisioningProfileId)
}

func TestABulkCreateEarmarksAndRefusesTheWholeBatchOnABadEarmark(t *testing.T) {
	f := newProvFixture(t)
	f.profile("pp-a", ProvisionAllowNew)
	pp := "pp-a"
	made, err := f.api.CreateDevices(f.ctx, []*DeviceCreateRequest{
		{Token: "b1", DeviceTypeToken: "dt-a", ProvisioningProfileToken: &pp},
		{Token: "b2", DeviceTypeToken: "dt-a"},
	})
	require.NoError(t, err)
	require.NotNil(t, made[0].ExpectedProvisioningProfileId)
	assert.Nil(t, made[1].ExpectedProvisioningProfileId)

	_, err = f.api.CreateDevices(f.ctx, []*DeviceCreateRequest{
		{Token: "b3", DeviceTypeToken: "dt-a"},
		{Token: "b4", DeviceTypeToken: "dt-b", ProvisioningProfileToken: &pp}, // wrong type
	})
	require.ErrorIs(t, err, ErrInvalidEarmark)
	rows, err := f.api.DevicesByToken(f.ctx, []string{"b3", "b4"})
	require.NoError(t, err)
	assert.Empty(t, rows, "a batch with a bad earmark was partly written")
}

func TestImportDevicesEarmarksAndComparesTheEarmarkOnARerun(t *testing.T) {
	f := newProvFixture(t)
	f.profile("pp-a", ProvisionAllowNew)
	f.profile("pp-b", ProvisionAllowNew)
	row := func(token, profile string) *DeviceImportRow {
		r := &DeviceImportRow{Token: token, DeviceTypeToken: "dt-a"}
		if profile != "" {
			r.ProvisioningProfileToken = &profile
		}
		return r
	}
	res, err := f.api.ImportDevices(f.ctx, []*DeviceImportRow{row("i1", "pp-a"), row("i2", ""), row("i3", "nope")}, DeviceImportModeNone, false)
	require.NoError(t, err)
	assert.Equal(t, DeviceImportCreated, res.Rows[0].Outcome)
	assert.Equal(t, DeviceImportCreated, res.Rows[1].Outcome)
	assert.Equal(t, DeviceImportInvalid, res.Rows[2].Outcome)
	require.NotNil(t, res.Rows[2].Message)
	assert.Contains(t, *res.Rows[2].Message, "nope")
	d, err := f.api.DevicesByToken(f.ctx, []string{"i1"})
	require.NoError(t, err)
	require.NotNil(t, d[0].ExpectedProvisioningProfileId)
	assert.Equal(t, f.profileID("pp-a"), *d[0].ExpectedProvisioningProfileId)

	// The same file again is all EXISTS; a different earmark on an existing device is a CONFLICT
	// that names the field and never changes it.
	res, err = f.api.ImportDevices(f.ctx, []*DeviceImportRow{row("i1", "pp-a"), row("i2", "")}, DeviceImportModeNone, false)
	require.NoError(t, err)
	assert.Equal(t, DeviceImportExists, res.Rows[0].Outcome)
	assert.Equal(t, DeviceImportExists, res.Rows[1].Outcome)
	res, err = f.api.ImportDevices(f.ctx, []*DeviceImportRow{row("i1", "pp-b"), row("i2", "pp-a"), row("i1b", "")}, DeviceImportModeNone, true)
	require.NoError(t, err)
	assert.Equal(t, DeviceImportConflict, res.Rows[0].Outcome)
	assert.Contains(t, *res.Rows[0].Message, "provisioningProfileToken")
	assert.Equal(t, DeviceImportConflict, res.Rows[1].Outcome)
	assert.Equal(t, DeviceImportCreated, res.Rows[2].Outcome, "a dry run decides the same")

	// A row naming a profile of another type is INVALID, not CONFLICT.
	bad := &DeviceImportRow{Token: "i9", DeviceTypeToken: "dt-b", ProvisioningProfileToken: strPtr("pp-a")}
	res, err = f.api.ImportDevices(f.ctx, []*DeviceImportRow{bad}, DeviceImportModeNone, false)
	require.NoError(t, err)
	assert.Equal(t, DeviceImportInvalid, res.Rows[0].Outcome)
}

func TestDeviceSearchFiltersByProvenanceAndByEarmark(t *testing.T) {
	f := newProvFixture(t)
	f.profile("pp-1", ProvisionAllowNew)
	f.profile("pp-2", ProvisionAllowNew)
	f.device("d-early", "SN-E", "dt-a", "pp-1") // earmarked, not registered
	f.device("d-plain", "SN-P", "dt-a", "")
	_, err := f.register("pp-1", "SN-1", material(1))
	require.NoError(t, err)
	_, err = f.register("pp-2", "SN-2", material(2))
	require.NoError(t, err)

	search := func(c DeviceSearchCriteria) []string {
		c.PageNumber, c.PageSize = 1, 100
		res, err := f.api.Devices(f.ctx, c)
		require.NoError(t, err)
		var tokens []string
		for _, d := range res.Results {
			tokens = append(tokens, d.Token)
		}
		return tokens
	}
	pv1, pv2 := core.DeriveDeviceToken("SN-1", "pv-"), core.DeriveDeviceToken("SN-2", "pv-")
	p1, p2, none := "pp-1", "pp-2", "no-such"

	assert.ElementsMatch(t, []string{pv1}, search(DeviceSearchCriteria{ProvisioningProfile: &p1}))
	assert.ElementsMatch(t, []string{pv2}, search(DeviceSearchCriteria{ProvisioningProfile: &p2}))
	assert.ElementsMatch(t, []string{"d-early", pv1}, search(DeviceSearchCriteria{ExpectedProvisioningProfile: &p1}))
	assert.Empty(t, search(DeviceSearchCriteria{ProvisioningProfile: &none}))
	assert.Empty(t, search(DeviceSearchCriteria{ExpectedProvisioningProfile: &none}))
	assert.Len(t, search(DeviceSearchCriteria{}), 4, "an unfiltered search is unaffected")
}

// ─── Operator controls ─────────────────────────────────────────────────────

func TestRevokeDisablesOnlyTheProfilesCredentialsAndKeepsOthers(t *testing.T) {
	f := newProvFixture(t)
	f.profile("pp-1", ProvisionAllowNew)
	f.profile("pp-2", ProvisionAllowNew)
	for i := 1; i <= 3; i++ {
		_, err := f.register("pp-1", fmt.Sprintf("SN-%d", i), material(i))
		require.NoError(t, err)
	}
	_, err := f.register("pp-2", "SN-9", material(9))
	require.NoError(t, err)
	// An operator credential on a device the profile registered is not the profile's to revoke.
	dev1 := core.DeriveDeviceToken("SN-1", "pv-")
	_, err = f.api.CreateDeviceCredential(f.ctx, &DeviceCredentialCreateRequest{
		Token: "c-op", DeviceToken: dev1, CredentialType: "ACCESS_TOKEN", CredentialId: material(50), Enabled: true})
	require.NoError(t, err)

	n, err := f.api.RevokeProvisionedDevices(f.ctx, "pp-1")
	require.NoError(t, err)
	assert.Equal(t, 3, n)
	enabled := map[string]bool{}
	var all []DeviceCredential
	require.NoError(t, f.api.RDB.DB(f.ctx).Find(&all).Error)
	for _, c := range all {
		enabled[c.Token] = c.Enabled
	}
	assert.False(t, enabled[core.DeriveDeviceToken("SN-1", "pc-")])
	assert.False(t, enabled[core.DeriveDeviceToken("SN-2", "pc-")])
	assert.False(t, enabled[core.DeriveDeviceToken("SN-3", "pc-")])
	assert.True(t, enabled[core.DeriveDeviceToken("SN-9", "pc-")], "another profile's credential was revoked")
	assert.True(t, enabled["c-op"], "an operator's credential was revoked")

	// It is idempotent, and the profile still takes new registrations.
	n, err = f.api.RevokeProvisionedDevices(f.ctx, "pp-1")
	require.NoError(t, err)
	assert.Equal(t, 0, n)
	_, err = f.register("pp-1", "SN-4", material(4))
	assert.NoError(t, err)

	_, err = f.api.RevokeProvisionedDevices(f.ctx, "nope")
	assert.Error(t, err)
}

func TestRevokeAndResetEndTheDevicesSessionsAndEvictTheirCredentials(t *testing.T) {
	f := newProvFixture(t)
	f.profile("pp-1", ProvisionAllowNew)
	var kicked [][]string
	f.api.SessionKicker = kickRecorder(func(_ context.Context, tenant, reason string, tokens []string) error {
		assert.Equal(t, "acme", tenant)
		kicked = append(kicked, append([]string{reason}, tokens...))
		return nil
	})
	for i := 1; i <= 3; i++ {
		_, err := f.register("pp-1", fmt.Sprintf("SN-%d", i), material(i))
		require.NoError(t, err)
	}
	kicked = nil

	n, err := f.api.RevokeProvisionedDevices(f.ctx, "pp-1")
	require.NoError(t, err)
	require.Equal(t, 3, n)
	require.Len(t, kicked, 1, "one broker round trip for three devices")
	assert.Equal(t, "credential-changed", kicked[0][0])
	assert.ElementsMatch(t, []string{
		core.DeriveDeviceToken("SN-1", "pv-"), core.DeriveDeviceToken("SN-2", "pv-"), core.DeriveDeviceToken("SN-3", "pv-"),
	}, kicked[0][1:])

	kicked = nil
	removed, err := f.api.ResetDeviceProvisioning(f.ctx, core.DeriveDeviceToken("SN-2", "pv-"))
	require.NoError(t, err)
	assert.Equal(t, 1, removed)
	require.Len(t, kicked, 1)
	assert.Equal(t, []string{"credential-deleted", core.DeriveDeviceToken("SN-2", "pv-")}, kicked[0])

	// A kick that cannot be confirmed is ErrSessionsNotEnded, with the change saved.
	f.api.SessionKicker = kickRecorder(func(context.Context, string, string, []string) error { return errors.New("no responder") })
	n, err = f.api.RevokeProvisionedDevices(f.ctx, "pp-1")
	require.NoError(t, err, "nothing left to revoke")
	assert.Zero(t, n)
	_, err = f.register("pp-1", "SN-7", material(7))
	require.NoError(t, err)
	n, err = f.api.RevokeProvisionedDevices(f.ctx, "pp-1")
	assert.Equal(t, 1, n, "the revoke is saved")
	assert.ErrorIs(t, err, ErrSessionsNotEnded)
}

type kickRecorder func(ctx context.Context, tenant, reason string, tokens []string) error

func (k kickRecorder) KickDeviceSessions(ctx context.Context, tenant, reason string, tokens []string) error {
	return k(ctx, tenant, reason, tokens)
}

func TestResetOnlyRemovesWhatSelfRegistrationIssued(t *testing.T) {
	f := newProvFixture(t)
	f.profile("pp-1", ProvisionAllowNew)
	f.device("dev-1", "SN-1", "dt-a", "pp-1")
	_, err := f.api.CreateDeviceCredential(f.ctx, &DeviceCredentialCreateRequest{
		Token: "c-op", DeviceToken: "dev-1", CredentialType: "ACCESS_TOKEN", CredentialId: material(50), Enabled: true})
	require.NoError(t, err)

	removed, err := f.api.ResetDeviceProvisioning(f.ctx, "dev-1")
	require.NoError(t, err)
	assert.Zero(t, removed, "a device nothing registered has nothing to reset")
	assert.Len(t, f.credentialsOf("dev-1"), 1, "an operator's credential was removed")

	_, err = f.api.ResetDeviceProvisioning(f.ctx, "no-such-device")
	assert.Error(t, err)
}

// ─── Delete guards ─────────────────────────────────────────────────────────

func TestAProfileThatIssuedCredentialsCannotBeDeletedUntilTheyAreReset(t *testing.T) {
	f := newProvFixture(t)
	f.profile("pp-1", ProvisionAllowNew)
	f.device("dev-early", "SN-E", "dt-a", "pp-1") // earmarked only
	_, err := f.register("pp-1", "SN-1", material(1))
	require.NoError(t, err)
	dev := core.DeriveDeviceToken("SN-1", "pv-")

	for _, state := range []string{"live", "revoked"} {
		deleted, err := f.api.DeleteProvisioningProfile(f.ctx, "pp-1")
		require.ErrorIs(t, err, ErrEntityInUse, state)
		assert.False(t, deleted)
		assert.Contains(t, err.Error(), "1 credential(s)", state)
		assert.Contains(t, err.Error(), "revoke", state)
		if state == "live" {
			_, err = f.api.RevokeProvisionedDevices(f.ctx, "pp-1")
			require.NoError(t, err)
		}
	}
	rows, err := f.api.ProvisioningProfilesByToken(f.ctx, []string{"pp-1"})
	require.NoError(t, err)
	require.Len(t, rows, 1, "a refused delete removed the profile")

	_, err = f.api.ResetDeviceProvisioning(f.ctx, dev)
	require.NoError(t, err)
	deleted, err := f.api.DeleteProvisioningProfile(f.ctx, "pp-1")
	require.NoError(t, err)
	assert.True(t, deleted)

	// Earmarks did not block it, and the earmarked devices fall back to none. (SQLite carries no
	// foreign keys, so the SET NULL is the integration test's; here the profile is simply gone.)
	rows, err = f.api.ProvisioningProfilesByToken(f.ctx, []string{"pp-1"})
	require.NoError(t, err)
	assert.Empty(t, rows)

	deleted, err = f.api.DeleteProvisioningProfile(f.ctx, "pp-1")
	require.NoError(t, err)
	assert.False(t, deleted, "deleting an absent profile")
}

func TestADeviceTypeAProfileRegistersCannotBeDeleted(t *testing.T) {
	f := newProvFixture(t)
	f.profile("pp-1", ProvisionAllowNew) // dt-a
	deleted, err := f.api.DeleteDeviceType(f.ctx, "dt-a")
	require.ErrorIs(t, err, ErrEntityInUse)
	assert.False(t, deleted)
	assert.Contains(t, err.Error(), "1 provisioning profile(s) reference device type")
	// The control: a type nothing refers to is deleted.
	deleted, err = f.api.DeleteDeviceType(f.ctx, "dt-b")
	require.NoError(t, err)
	assert.True(t, deleted)
}

// A database foreign-key refusal of a delete is ErrEntityInUse, not a server error.
func TestAForeignKeyViolationOnADeleteIsInUse(t *testing.T) {
	cause := foreignKeyViolation(t)
	got := inUseOn(fmt.Errorf("delete: %w", cause), "x is still referenced")
	assert.ErrorIs(t, got, ErrEntityInUse)
	assert.Contains(t, got.Error(), "x is still referenced")
	// Anything else is left alone, including nil and a refusal that already says so.
	assert.NoError(t, inUseOn(nil, "x"))
	plain := errors.New("boom")
	assert.Equal(t, plain, inUseOn(plain, "x"))
	already := fmt.Errorf("%w: 2 rows", ErrEntityInUse)
	assert.Equal(t, already, inUseOn(already, "x"))
}

// foreignKeyViolation is a REAL foreign-key refusal from the SQLite driver, the one error type
// the integrity package recognises by its type and code and not by its text.
func foreignKeyViolation(t *testing.T) error {
	t.Helper()
	db := putest.NewSQLiteDB(t)
	var got error
	require.NoError(t, db.Connection(func(tx *gorm.DB) error {
		require.NoError(t, tx.Exec(`PRAGMA foreign_keys = ON`).Error)
		require.NoError(t, tx.Exec(`CREATE TABLE fk_parent (id integer primary key)`).Error)
		require.NoError(t, tx.Exec(`CREATE TABLE fk_child (parent_id integer references fk_parent(id))`).Error)
		got = tx.Exec(`INSERT INTO fk_child (parent_id) VALUES (42)`).Error
		return nil
	}))
	require.Error(t, got)
	return got
}
