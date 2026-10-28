// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/integrity"
	"github.com/devicechain-io/dc-microservice/x509trust"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// x509Create is a certificate credential create request.
func x509Create(token, device, certPEM, anchor string) *DeviceCredentialCreateRequest {
	return &DeviceCredentialCreateRequest{
		Token: token, DeviceToken: device, CredentialType: string(CredentialX509Certificate),
		CertificatePem: &certPEM, TrustAnchorToken: &anchor, Enabled: true,
	}
}

func isInvalidRefusal(err error) bool {
	class, ok := integrity.Refused(err)
	return ok && class == integrity.ClassInvalid
}

func (f *provFixture) x509Setup() (*pkiNode, *DeviceTrustAnchor) {
	f.t.Helper()
	ca := newRootCA(f.t, "Maker Root")
	a := f.anchor("maker", ca)
	f.device("dev", "dev-ext", "dt-a", "")
	return ca, a
}

func TestCreateX509CredentialDerivesEverythingFromTheCertificate(t *testing.T) {
	f := newProvFixture(t)
	ca, a := f.x509Setup()
	inter := newPki(t, ca, pkiOpts{cn: "Line CA", ca: true, notAfter: time.Now().Add(10 * time.Hour)})
	leaf := inter.leaf(t, "SN-0001")

	// The text is the device certificate then its intermediate; only the leaf is stored.
	created, err := f.api.CreateDeviceCredential(f.ctx, x509Create("c-1", "dev", leaf.pem()+inter.pem(), "maker"))
	require.NoError(t, err)

	pin := x509trust.Fingerprint(leaf.cert)
	assert.Equal(t, pin, created.CredentialId, "the id is the leaf's SHA-256")
	assert.Equal(t, string(CredentialX509Certificate), created.CredentialType)
	require.NotNil(t, created.TrustAnchorId)
	assert.Equal(t, a.ID, *created.TrustAnchorId)
	assert.Equal(t, leaf.pem(), created.CertificatePem.String, "only the leaf is stored")
	assert.False(t, created.SecretDigest.Valid, "a certificate credential holds no secret")
	assert.Empty(t, created.IssuedCredentialId)
	require.True(t, created.ExpiresAt.Valid)
	assert.WithinDuration(t, inter.cert.NotAfter, created.ExpiresAt.Time, time.Second,
		"the expiry is the chain's earliest notAfter (the intermediate's), not the leaf's")

	stored, err := f.api.DeviceCredentialByCredentialId(f.ctx, string(CredentialX509Certificate), pin)
	require.NoError(t, err)
	assert.Equal(t, "c-1", stored.Token)

	info := created.X509Info()
	require.NotNil(t, info)
	assert.Equal(t, pin, info.FingerprintSha256)
	assert.Equal(t, "SN-0001", info.SubjectCn)
	assert.Equal(t, a.ID, info.TrustAnchorId)
	assert.Equal(t, fmt.Sprintf("%x", leaf.cert.SerialNumber), info.SerialHex)
}

// Each refusal is the control request with ONE thing changed. Nothing is written by any of them.
func TestCreateX509CredentialRefusals(t *testing.T) {
	f := newProvFixture(t)
	ca, _ := f.x509Setup()
	_, err := f.api.CreateDeviceCredential(f.ctx, x509Create("control", "dev", ca.leaf(t, "SN-1").pem(), "maker"))
	require.NoError(t, err, "the control must succeed")
	count := func() int64 {
		var n int64
		require.NoError(t, f.api.RDB.DB(f.ctx).Model(&DeviceCredential{}).Count(&n).Error)
		return n
	}
	before := count()

	f.anchor("other", newRootCA(t, "Other Root"))
	f.anchor("shut", newRootCA(t, "Shut Root"), func(r *DeviceTrustAnchorCreateRequest) { r.Enabled = boolp(false) })

	isNotFound := func(err error) bool { return errors.Is(err, gorm.ErrRecordNotFound) }
	refuse := func(name string, m func(*DeviceCredentialCreateRequest), want func(error) bool) {
		req := x509Create("c-bad", "dev", ca.leaf(t, "SN-x").pem(), "maker")
		m(req)
		_, err := f.api.CreateDeviceCredential(f.ctx, req)
		require.Error(t, err, name)
		require.True(t, want(err), "%s: unexpected error %v", name, err)
	}

	refuse("credential id", func(r *DeviceCredentialCreateRequest) { r.CredentialId = "AA:BB" }, isInvalidRefusal)
	refuse("secret", func(r *DeviceCredentialCreateRequest) { r.CredentialValue = strp("hunter2hunter2") }, isInvalidRefusal)
	refuse("expiry", func(r *DeviceCredentialCreateRequest) { r.ExpiresAt = strp("2099-01-01T00:00:00Z") }, isInvalidRefusal)
	refuse("no anchor", func(r *DeviceCredentialCreateRequest) { r.TrustAnchorToken = nil }, isInvalidRefusal)
	refuse("no certificate", func(r *DeviceCredentialCreateRequest) { r.CertificatePem = nil }, isInvalidRefusal)
	refuse("unknown anchor", func(r *DeviceCredentialCreateRequest) { r.TrustAnchorToken = strp("nope") }, isNotFound)
	refuse("disabled anchor", func(r *DeviceCredentialCreateRequest) { r.TrustAnchorToken = strp("shut") }, isInvalidRefusal)
	refuse("wrong anchor", func(r *DeviceCredentialCreateRequest) { r.TrustAnchorToken = strp("other") }, isInvalidRefusal)
	refuse("private key", func(r *DeviceCredentialCreateRequest) {
		r.CertificatePem = strp(*r.CertificatePem + "-----BEGIN PRIVATE KEY-----\nAAAA\n-----END PRIVATE KEY-----\n")
	}, isInvalidRefusal)
	refuse("not a certificate", func(r *DeviceCredentialCreateRequest) { r.CertificatePem = strp("hello") }, isInvalidRefusal)
	refuse("expired leaf", func(r *DeviceCredentialCreateRequest) {
		r.CertificatePem = strp(newPki(t, ca, pkiOpts{cn: "old", notBefore: time.Now().Add(-48 * time.Hour),
			notAfter: time.Now().Add(-time.Hour)}).pem())
	}, isInvalidRefusal)
	refuse("a server certificate", func(r *DeviceCredentialCreateRequest) {
		r.CertificatePem = strp(newPki(t, ca, pkiOpts{cn: "srv", eku: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}).pem())
	}, isInvalidRefusal)
	refuse("a CA as the device certificate", func(r *DeviceCredentialCreateRequest) {
		r.CertificatePem = strp(newPki(t, ca, pkiOpts{cn: "ca", ca: true}).pem())
	}, isInvalidRefusal)
	refuse("chain missing its intermediate", func(r *DeviceCredentialCreateRequest) {
		inter := newPki(t, ca, pkiOpts{cn: "Line", ca: true})
		r.CertificatePem = strp(inter.leaf(t, "x").pem())
	}, isInvalidRefusal)
	refuse("certificate fields on another type", func(r *DeviceCredentialCreateRequest) {
		r.CredentialType, r.CredentialId = string(CredentialAccessToken), "tok-1"
	}, isInvalidRefusal)

	assert.Equal(t, before, count(), "a refused create stored a credential")
}

func TestADeviceHoldsAtMostThreeCertificateCredentials(t *testing.T) {
	f := newProvFixture(t)
	ca, _ := f.x509Setup()
	for i := 0; i < MaxX509CredentialsPerDevice; i++ {
		_, err := f.api.CreateDeviceCredential(f.ctx, x509Create(fmt.Sprintf("c-%d", i), "dev", ca.leaf(t, "SN").pem(), "maker"))
		require.NoError(t, err)
	}
	_, err := f.api.CreateDeviceCredential(f.ctx, x509Create("c-4", "dev", ca.leaf(t, "SN").pem(), "maker"))
	requireInvalid(t, err, "the fourth certificate credential")

	// Another device is unaffected.
	f.device("dev2", "dev2-ext", "dt-a", "")
	_, err = f.api.CreateDeviceCredential(f.ctx, x509Create("c-5", "dev2", ca.leaf(t, "SN").pem(), "maker"))
	require.NoError(t, err)
}

// A pinned certificate is not something to be presented by id: the pin is public. No id-based
// authentication path accepts the type, however the row came to exist and whatever is cached.
func TestACertificateCredentialNeverAuthenticatesById(t *testing.T) {
	f := newProvFixture(t)
	ca, _ := f.x509Setup()
	created, err := f.api.CreateDeviceCredential(f.ctx, x509Create("c-1", "dev", ca.leaf(t, "SN-1").pem(), "maker"))
	require.NoError(t, err)

	presented := &PresentedCredential{CredentialType: string(CredentialX509Certificate), CredentialId: created.CredentialId}
	now := time.Now()
	d, err := f.api.AuthenticateDevice(f.ctx, presented, now)
	require.ErrorIs(t, err, ErrCredentialTypeInvalid)
	require.Nil(t, d, "a public pin authenticated a device")
	require.True(t, IsCredentialRefusal(err))

	capi := NewCachedApi(f.api, &Caches{})
	d, err = capi.AuthenticateDevice(f.ctx, presented, now)
	require.ErrorIs(t, err, ErrCredentialTypeInvalid)
	require.Nil(t, d)

	dev, stored, err := f.api.ResolveDeviceCredential(f.ctx, presented, now)
	require.ErrorIs(t, err, ErrCredentialTypeInvalid)
	require.Nil(t, dev)
	require.Empty(t, stored)

	// The row evaluates as a refusal even when handed straight to the policy.
	require.ErrorIs(t, evaluateCredential(testSecretKey, created, presented, now), ErrCredentialTypeInvalid)

	// The control: an access token on the same device still authenticates, so the refusals above
	// are the type's and not the fixture's.
	_, err = f.api.CreateDeviceCredential(f.ctx, &DeviceCredentialCreateRequest{
		Token: "c-tok", DeviceToken: "dev", CredentialType: string(CredentialAccessToken), CredentialId: "tok-1", Enabled: true})
	require.NoError(t, err)
	d, err = f.api.AuthenticateDevice(f.ctx, &PresentedCredential{CredentialType: string(CredentialAccessToken), CredentialId: "tok-1"}, now)
	require.NoError(t, err)
	require.NotNil(t, d)
}

func TestUpdateX509CredentialAllowsOnlyWhatIsNotTheCertificate(t *testing.T) {
	f := newProvFixture(t)
	ca, _ := f.x509Setup()
	created, err := f.api.CreateDeviceCredential(f.ctx, x509Create("c-1", "dev", ca.leaf(t, "SN-1").pem(), "maker"))
	require.NoError(t, err)

	// Disabling is allowed, and so is metadata.
	var ok DeviceCredentialUpdateRequest
	ok.Enabled.Set, ok.Enabled.Value = true, boolp(false)
	ok.Metadata.Set, ok.Metadata.Value = true, strp(`{"site":"a"}`)
	got, err := f.api.UpdateDeviceCredential(f.ctx, "c-1", &ok)
	require.NoError(t, err)
	assert.False(t, got.Enabled)
	assert.Equal(t, created.CredentialId, got.CredentialId, "the pin does not move")
	assert.Equal(t, created.TrustAnchorId, got.TrustAnchorId)
	assert.Equal(t, created.ExpiresAt, got.ExpiresAt)

	for name, set := range map[string]func(*DeviceCredentialUpdateRequest){
		"id": func(r *DeviceCredentialUpdateRequest) { r.CredentialId.Set, r.CredentialId.Value = true, strp("AA") },
		"type": func(r *DeviceCredentialUpdateRequest) {
			r.CredentialType.Set, r.CredentialType.Value = true, strp("MQTT_BASIC")
		},
		"secret": func(r *DeviceCredentialUpdateRequest) {
			r.CredentialValue.Set, r.CredentialValue.Value = true, strp("hunter2hunter2")
		},
		"expiry": func(r *DeviceCredentialUpdateRequest) {
			r.ExpiresAt.Set, r.ExpiresAt.Value = true, strp("2099-01-01T00:00:00Z")
		},
	} {
		var req DeviceCredentialUpdateRequest
		set(&req)
		_, err := f.api.UpdateDeviceCredential(f.ctx, "c-1", &req)
		requireInvalid(t, err, "update "+name)
	}

	// Another credential cannot be turned into one: it would have no certificate.
	_, err = f.api.CreateDeviceCredential(f.ctx, &DeviceCredentialCreateRequest{
		Token: "c-tok", DeviceToken: "dev", CredentialType: string(CredentialAccessToken), CredentialId: "tok-1", Enabled: true})
	require.NoError(t, err)
	var to DeviceCredentialUpdateRequest
	to.CredentialType.Set, to.CredentialType.Value = true, strp(string(CredentialX509Certificate))
	to.CredentialId.Set, to.CredentialId.Value = true, strp("AA:BB")
	_, err = f.api.UpdateDeviceCredential(f.ctx, "c-tok", &to)
	requireInvalid(t, err, "update to X509")
	stored, err := f.api.DeviceCredentialByCredentialId(f.ctx, string(CredentialAccessToken), "tok-1")
	require.NoError(t, err)
	assert.Equal(t, string(CredentialAccessToken), stored.CredentialType, "a refused update changed the type")
}

// A credential built anywhere but createDeviceCredential has no anchor to check, so every other
// path refuses the type rather than storing a pin that can never authenticate.
func TestOtherPathsRefuseToBuildACertificateCredential(t *testing.T) {
	_, err := buildDeviceCredential(testSecretKey, &Device{}, &DeviceCredentialCreateRequest{
		Token: "x", CredentialType: string(CredentialX509Certificate), CredentialId: "AA:BB", Enabled: true})
	requireInvalid(t, err, "buildDeviceCredential")
	_, err = buildDeviceCredential(testSecretKey, &Device{}, &DeviceCredentialCreateRequest{
		Token: "x", CredentialType: string(CredentialAccessToken), CredentialId: "tok", Enabled: true,
		TrustAnchorToken: strp("maker")})
	requireInvalid(t, err, "certificate fields on an access token")
}

func TestCredentialTypeVocabulary(t *testing.T) {
	for _, ct := range []CredentialType{CredentialAccessToken, CredentialMqttBasic, CredentialX509Certificate} {
		assert.True(t, ct.Valid(), ct)
	}
	for _, ct := range []CredentialType{"", "BOGUS", "x509_certificate"} {
		assert.False(t, ct.Valid(), ct)
	}
	assert.True(t, CredentialAccessToken.PresentedById())
	assert.True(t, CredentialMqttBasic.PresentedById())
	assert.False(t, CredentialX509Certificate.PresentedById(), "a public pin is not a bearer")
	assert.False(t, CredentialType("BOGUS").PresentedById())
}

// X509Info is nil for anything that is not a readable certificate credential, never a made-up one.
func TestX509InfoIsNilWithoutACertificate(t *testing.T) {
	assert.Nil(t, (&DeviceCredential{CredentialType: string(CredentialAccessToken)}).X509Info())
	assert.Nil(t, (&DeviceCredential{CredentialType: string(CredentialX509Certificate)}).X509Info())
	bad := &DeviceCredential{CredentialType: string(CredentialX509Certificate)}
	bad.CertificatePem.String, bad.CertificatePem.Valid = "-----BEGIN CERTIFICATE-----\n!!\n-----END CERTIFICATE-----\n", true
	assert.Nil(t, bad.X509Info())
}

// A pin is public, so an id-based authentication path refuses the type BEFORE it reads anything:
// the Api here has no database, and a path that looked the pin up would fault on it.
func TestACertificatePinIsRefusedBeforeAnyLookup(t *testing.T) {
	api := &Api{}
	presented := &PresentedCredential{CredentialType: string(CredentialX509Certificate), CredentialId: "AA:BB"}
	d, err := api.AuthenticateDevice(context.Background(), presented, time.Now())
	require.ErrorIs(t, err, ErrCredentialTypeInvalid)
	require.Nil(t, d)
}
