// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"reflect"
	"runtime"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	dcgraphql "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-microservice/rdb"
	putest "github.com/devicechain-io/dc-microservice/rdb/partialupdatetest"
	"github.com/devicechain-io/dc-microservice/x509trust"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ResolveX509Connect is the callout's whole read of a certificate pin. Each refusal is the
// control (a resolvable pin) with ONE thing changed.
func TestResolveX509Connect(t *testing.T) {
	f := newProvFixture(t)
	ca, anchor := f.x509Setup()
	leaf := ca.leaf(t, "SN-0001")
	cred, err := f.api.CreateDeviceCredential(f.ctx, x509Create("c-1", "dev", leaf.pem(), "maker"))
	require.NoError(t, err)
	pin := x509trust.Fingerprint(leaf.cert)
	now := time.Now()

	found, err := f.api.ResolveX509Connect(f.ctx, pin, now)
	require.NoError(t, err, "the control: a pin that resolves")
	require.NotNil(t, found.Device)
	assert.Equal(t, "dev", found.Device.Token)
	assert.False(t, found.Device.IsDisabled())
	require.NotNil(t, found.Anchor)
	assert.Equal(t, anchor.ID, found.Anchor.ID)
	assert.True(t, found.Anchor.Enabled)
	assert.Equal(t, cred.ExpiresAt.Time.UTC(), found.Device.ConnectCredentialExpiresAt.Time.UTC(),
		"the device carries the credential's expiry for the session clamp")
	cert, err := found.Anchor.Certificate()
	require.NoError(t, err)
	assert.Equal(t, ca.cert.Raw, cert.Raw, "the anchor is the credential's own CA")

	t.Run("an unknown pin", func(t *testing.T) {
		_, err := f.api.ResolveX509Connect(f.ctx, x509trust.Fingerprint(ca.leaf(t, "SN-9").cert), now)
		assert.ErrorIs(t, err, ErrCredentialNotResolved)
	})
	t.Run("another credential type that happens to carry the pin as its id", func(t *testing.T) {
		// An MQTT username is chosen by the operator, so one can be spelled exactly like a pin.
		// It is a different credential and must not resolve as a certificate.
		f.device("dev-b", "dev-b-ext", "dt-a", "")
		stranger := ca.leaf(t, "SN-LOOKALIKE")
		secret := "a-long-enough-secret-0123456789"
		_, err := f.api.CreateDeviceCredential(f.ctx, &DeviceCredentialCreateRequest{
			Token: "c-basic", DeviceToken: "dev-b", CredentialType: string(CredentialMqttBasic),
			CredentialId: x509trust.Fingerprint(stranger.cert), CredentialValue: &secret, Enabled: true})
		require.NoError(t, err)
		_, err = f.api.ResolveX509Connect(f.ctx, x509trust.Fingerprint(stranger.cert), now)
		assert.ErrorIs(t, err, ErrCredentialNotResolved, "an MQTT_BASIC credential resolved as a certificate pin")
	})
	t.Run("a read that escapes the tenant predicate still does not cross tenants", func(t *testing.T) {
		// Under a system context the callback adds no tenant predicate, so the statement returns
		// this tenant's row to a caller whose context names another tenant. The explicit tenant
		// checks are what refuse it.
		other := core.WithSystemContext(core.WithTenant(context.Background(), "someone-else"))
		_, err := f.api.ResolveX509Connect(other, pin, now)
		assert.ErrorIs(t, err, ErrCredentialNotResolved, "another tenant's credential resolved")
		same := core.WithSystemContext(core.WithTenant(context.Background(), partialUpdateTenant))
		_, err = f.api.ResolveX509Connect(same, pin, now)
		assert.NoError(t, err, "the control: the same unscoped read for the owning tenant resolves")
	})
	t.Run("a credential pointing at another tenant's device does not resolve", func(t *testing.T) {
		// The device join is not scoped by the query; a corrupt or hostile device_id must not hand
		// the caller a device of another tenant. The control is the restored row.
		foreign := putest.TenantContext("someone-else")()
		_, err := f.api.CreateDeviceType(foreign, &DeviceTypeCreateRequest{Token: "dt-x"})
		require.NoError(t, err)
		fd, err := f.api.CreateDevice(foreign, &DeviceCreateRequest{Token: "foreign-dev", DeviceTypeToken: "dt-x"})
		require.NoError(t, err)
		devId := cred.DeviceId
		require.NoError(t, f.api.RDB.DB(f.ctx).Model(&DeviceCredential{}).Where("id = ?", cred.ID).Update("device_id", fd.ID).Error)
		_, err = f.api.ResolveX509Connect(f.ctx, pin, now)
		assert.ErrorIs(t, err, ErrCredentialNotResolved, "a device of another tenant was returned")
		require.NoError(t, f.api.RDB.DB(f.ctx).Model(&DeviceCredential{}).Where("id = ?", cred.ID).Update("device_id", devId).Error)
		_, err = f.api.ResolveX509Connect(f.ctx, pin, now)
		require.NoError(t, err, "the control: the restored row resolves")
	})
	t.Run("nothing presented", func(t *testing.T) {
		_, err := f.api.ResolveX509Connect(f.ctx, "", now)
		assert.ErrorIs(t, err, ErrCredentialNotPresented)
	})
	t.Run("another tenant cannot resolve it", func(t *testing.T) {
		_, err := f.api.ResolveX509Connect(putest.TenantContext("someone-else")(), pin, now)
		assert.ErrorIs(t, err, ErrCredentialNotResolved, "a pin must resolve inside its own tenant only")
	})
	t.Run("past its expiry", func(t *testing.T) {
		_, err := f.api.ResolveX509Connect(f.ctx, pin, cred.ExpiresAt.Time.Add(time.Second))
		assert.ErrorIs(t, err, ErrCredentialExpired)
	})
	t.Run("the device is disabled: the lookup still resolves it, and says so", func(t *testing.T) {
		_, err := f.api.UpdateDevice(f.ctx, "dev", &DeviceUpdateRequest{Enabled: dcgraphql.OptionalBoolOf(false)})
		require.NoError(t, err)
		found, err := f.api.ResolveX509Connect(f.ctx, pin, now)
		require.NoError(t, err)
		assert.True(t, found.Device.IsDisabled(), "a disabled device read as enabled would be admitted")
		_, err = f.api.UpdateDevice(f.ctx, "dev", &DeviceUpdateRequest{Enabled: dcgraphql.OptionalBoolOf(true)})
		require.NoError(t, err)
	})
	t.Run("the anchor is disabled: the lookup still resolves it, and says so", func(t *testing.T) {
		_, err := f.api.UpdateDeviceTrustAnchor(f.ctx, "maker", &DeviceTrustAnchorUpdateRequest{Enabled: dcgraphql.OptionalBoolOf(false)})
		require.NoError(t, err)
		found, err := f.api.ResolveX509Connect(f.ctx, pin, now)
		require.NoError(t, err)
		require.NotNil(t, found.Anchor)
		assert.False(t, found.Anchor.Enabled, "a disabled anchor read as enabled would trust a withdrawn CA")
		_, err = f.api.UpdateDeviceTrustAnchor(f.ctx, "maker", &DeviceTrustAnchorUpdateRequest{Enabled: dcgraphql.OptionalBoolOf(true)})
		require.NoError(t, err)
	})
	t.Run("a credential stored without an anchor never authenticates", func(t *testing.T) {
		require.NoError(t, f.api.RDB.DB(f.ctx).Model(&DeviceCredential{}).Where("id = ?", cred.ID).
			Update("trust_anchor_id", nil).Error)
		_, err := f.api.ResolveX509Connect(f.ctx, pin, now)
		assert.ErrorIs(t, err, ErrCredentialMisconfigured)
		require.NoError(t, f.api.RDB.DB(f.ctx).Model(&DeviceCredential{}).Where("id = ?", cred.ID).
			Update("trust_anchor_id", anchor.ID).Error)
	})
	t.Run("revoked", func(t *testing.T) {
		_, err := f.api.UpdateDeviceCredential(f.ctx, "c-1", &DeviceCredentialUpdateRequest{Enabled: dcgraphql.OptionalBoolOf(false)})
		require.NoError(t, err)
		_, err = f.api.ResolveX509Connect(f.ctx, pin, now)
		assert.ErrorIs(t, err, ErrCredentialRevoked, "a disabled credential must be a revocation, not an unknown pin")
		_, err = f.api.UpdateDeviceCredential(f.ctx, "c-1", &DeviceCredentialUpdateRequest{Enabled: dcgraphql.OptionalBoolOf(true)})
		require.NoError(t, err)
		_, err = f.api.ResolveX509Connect(f.ctx, pin, now)
		require.NoError(t, err, "re-enabled, it resolves again: nothing is remembered")
	})
}

// The type gate: a certificate credential is never reachable by presenting its id, the way an
// access token is. The pin is public, so a path that took it as a bearer would authenticate on a
// readable value.
func TestAPinIsNotAnIdBasedCredential(t *testing.T) {
	f := newProvFixture(t)
	ca, _ := f.x509Setup()
	leaf := ca.leaf(t, "SN-0001")
	_, err := f.api.CreateDeviceCredential(f.ctx, x509Create("c-1", "dev", leaf.pem(), "maker"))
	require.NoError(t, err)
	presented := &PresentedCredential{CredentialType: string(CredentialX509Certificate), CredentialId: x509trust.Fingerprint(leaf.cert)}
	_, err = f.api.AuthenticateDeviceConnect(f.ctx, presented, time.Now())
	assert.ErrorIs(t, err, ErrCredentialTypeInvalid)
	_, err = f.api.AuthenticateDevice(f.ctx, presented, time.Now())
	assert.ErrorIs(t, err, ErrCredentialTypeInvalid)
}

// THE AUDIT JOURNAL SEES EVERY CHANGE TO A TRUST ANCHOR AND TO A CERTIFICATE CREDENTIAL, and
// records no certificate content: anchor create, disable and delete; credential create and
// disable. These are the changes that decide who may connect, so an operator reading the
// journal after an incident can say who withdrew which CA and when. (Connect outcomes are
// counted, never journaled.)
func TestAuditJournalRecordsTrustChanges(t *testing.T) {
	f := newProvFixture(t)
	require.NoError(t, f.api.RDB.Database.WithContext(core.WithSystemContext(context.Background())).AutoMigrate(&rdb.AuditEvent{}))
	ca := newRootCA(t, "Maker Root")
	f.device("dev", "dev-ext", "dt-a", "")
	leaf := ca.leaf(t, "SN-0001")

	_, err := f.api.CreateDeviceTrustAnchor(f.ctx, &DeviceTrustAnchorCreateRequest{Token: "maker", CertificatePem: ca.pem()})
	require.NoError(t, err)
	_, err = f.api.CreateDeviceCredential(f.ctx, x509Create("c-1", "dev", leaf.pem(), "maker"))
	require.NoError(t, err)
	_, err = f.api.UpdateDeviceCredential(f.ctx, "c-1", &DeviceCredentialUpdateRequest{Enabled: dcgraphql.OptionalBoolOf(false)})
	require.NoError(t, err)
	_, err = f.api.UpdateDeviceTrustAnchor(f.ctx, "maker", &DeviceTrustAnchorUpdateRequest{Enabled: dcgraphql.OptionalBoolOf(false)})
	require.NoError(t, err)
	// A second anchor nothing relies on, so the delete is allowed.
	other := newRootCA(t, "Other Root")
	_, err = f.api.CreateDeviceTrustAnchor(f.ctx, &DeviceTrustAnchorCreateRequest{Token: "other", CertificatePem: other.pem()})
	require.NoError(t, err)
	deleted, err := f.api.DeleteDeviceTrustAnchor(f.ctx, "other")
	require.NoError(t, err)
	require.True(t, deleted)

	var rows []rdb.AuditEvent
	require.NoError(t, f.api.RDB.DB(f.ctx).Model(&rdb.AuditEvent{}).Where("table_name IN ?", []string{"device_trust_anchors", "device_credentials"}).
		Order("id").Find(&rows).Error)
	type entry struct{ table, op, label string }
	var got []entry
	for _, r := range rows {
		got = append(got, entry{r.TableName, r.Operation, r.EntityLabel})
		assert.NotContains(t, r.EntityLabel, "BEGIN CERTIFICATE", "the journal must not carry certificate text")
		assert.NotContains(t, r.EntityLabel, x509trust.Fingerprint(leaf.cert), "the journal must not carry the pin")
	}
	assert.Contains(t, got, entry{"device_trust_anchors", "create", "maker"})
	assert.Contains(t, got, entry{"device_credentials", "create", "c-1"})
	assert.Contains(t, got, entry{"device_credentials", "update", "c-1"}, "disabling a certificate credential")
	assert.Contains(t, got, entry{"device_trust_anchors", "update", "maker"}, "disabling a trust anchor")
	assert.Contains(t, got, entry{"device_trust_anchors", "create", "other"})
	var deletes int
	for _, e := range got {
		if e.table == "device_trust_anchors" && e.op == "delete" {
			deletes++
		}
	}
	assert.Equal(t, 1, deletes, "deleting a trust anchor")
}

// The lookup is never cached: CachedApi embeds *Api and PROMOTES ResolveX509Connect. A method
// declared on CachedApi would put a cache in front of a revocation. A promoted method is a
// compiler-generated wrapper, so its code is attributed to "<autogenerated>"; a declared one to
// the file that declares it.
func TestResolveX509ConnectIsPromotedNotOverridden(t *testing.T) {
	m, ok := reflect.TypeOf(&CachedApi{}).MethodByName("ResolveX509Connect")
	require.True(t, ok, "CachedApi must carry ResolveX509Connect (promoted from Api)")
	fn := runtime.FuncForPC(m.Func.Pointer())
	require.NotNil(t, fn)
	file, _ := fn.FileLine(fn.Entry())
	assert.Equal(t, "<autogenerated>", file, "CachedApi declares its own ResolveX509Connect (%s): a cache in front of a revocation", file)

	// The control: a method CachedApi does override is attributed to its source file.
	d, ok := reflect.TypeOf(&CachedApi{}).MethodByName("AuthenticateDevice")
	require.True(t, ok)
	dfile, _ := runtime.FuncForPC(d.Func.Pointer()).FileLine(runtime.FuncForPC(d.Func.Pointer()).Entry())
	assert.NotEqual(t, "<autogenerated>", dfile, "the control must be able to tell a declared method from a promoted one")
}
