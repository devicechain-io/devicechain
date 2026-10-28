// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"crypto/x509"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/x509trust"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// REGISTRATION BY CERTIFICATE, THE DECISION. The callout owns the chain verification and the
// budgets; what is tested here is what ProvisionDevice does with a verified certificate: the same
// precedence as registration by secret, with certificate inputs, every refusal writing NOTHING
// and every one paired with the registration that succeeds when its one input is corrected.
// (Locks and races are the integration tests' on a real Postgres: this fixture drops FOR UPDATE.)

// jitFixture is the provFixture with one CA, its anchor and one certificate profile.
type jitFixture struct {
	*provFixture
	ca     *pkiNode
	anchor *DeviceTrustAnchor
}

func newJitFixture(t *testing.T, strategy ProvisioningStrategy, mutate ...func(*ProvisioningProfileCreateRequest)) *jitFixture {
	t.Helper()
	f := newProvFixture(t)
	ca := newRootCA(t, "Maker Root")
	a := f.anchor("maker", ca)
	f.profile("pp-x", strategy, append([]func(*ProvisioningProfileCreateRequest){withAnchor("maker")}, mutate...)...)
	return &jitFixture{provFixture: f, ca: ca, anchor: a}
}

// registration builds what the callout hands ProvisionDevice for a leaf: verified against the
// anchor exactly as the callout does.
func (f *jitFixture) registration(leaf *pkiNode, inters ...*pkiNode) *X509Registration {
	f.t.Helper()
	chain := []*x509.Certificate{leaf.cert}
	for _, i := range inters {
		chain = append(chain, i.cert)
	}
	verified, err := x509trust.VerifyClientChain(f.ca.cert, chain, time.Now())
	require.NoError(f.t, err)
	return &X509Registration{Leaf: leaf.cert, NotAfter: verified.NotAfter, AnchorId: f.anchor.ID,
		AnchorFingerprint: f.anchor.FingerprintSha256}
}

// profileRow is the profile as the callout reads it.
func (f *jitFixture) profileRow(token string) *ProvisioningProfile {
	f.t.Helper()
	rows, err := f.api.X509ProfileAnchors(f.ctx)
	require.NoError(f.t, err)
	for _, r := range rows {
		if r.Profile.Token == token {
			return r.Profile
		}
	}
	f.t.Fatalf("no certificate profile %q", token)
	return nil
}

func (f *jitFixture) register509(profile string, reg *X509Registration) (*ProvisionResult, error) {
	f.t.Helper()
	return f.api.ProvisionDevice(f.ctx, &ProvisionRequest{Profile: f.profileRow(profile), Certificate: reg})
}

func (f *jitFixture) refused509(want *ProvisioningRefusal, profile string, reg *X509Registration) {
	f.t.Helper()
	before := f.counts()
	res, err := f.register509(profile, reg)
	require.Error(f.t, err, "registered %+v", res)
	require.ErrorIs(f.t, err, want, "got %v", err)
	assert.Equal(f.t, before, f.counts(), "a refused registration wrote rows")
}

// The derived token is ONE definition shared by registration by secret, registration by
// certificate, and the connect side that predicts it.
func TestCertificateDeviceTokenIsTheSharedDerivation(t *testing.T) {
	for _, cn := range []string{"SN-1", "line 4/unit 9", "ünï-cödé", "a.b.c"} {
		assert.Equal(t, core.DeriveDeviceToken(cn, "pv-"), ProvisionedDeviceToken(cn))
		assert.Equal(t, ProvisionedDeviceToken(cn), DeriveX509DeviceToken(cn))
	}
}

func TestProvisioningCodesCoverTheRefusals(t *testing.T) {
	codes := map[string]bool{}
	for _, c := range ProvisioningCodes() {
		assert.False(t, codes[c], "%s listed twice", c)
		codes[c] = true
	}
	for _, r := range []*ProvisioningRefusal{ErrProvisioningFailed, ErrProvisioningNotRegistered, ErrProvisioningNotEarmarked,
		ErrProvisioningDeviceDisabled, ErrProvisioningTypeMismatch, ErrProvisioningTokenCollision,
		ErrProvisioningAlreadyProvisioned, ErrProvisioningRevoked, ErrProvisioningLimitExceeded, ErrProvisioningUnavailable,
		ErrProvisioningMisconfigured, ErrProvisioningCredentialTypeUnsupported, invalidProvisioning("x")} {
		assert.True(t, codes[r.Code], "refusal code %s is not in ProvisioningCodes", r.Code)
	}
	assert.Len(t, codes, 12)
}

// ALLOW_NEW creates the device and its pinned credential in one transaction, and the credential
// is what the connect path resolves.
func TestCertificateRegistrationCreatesTheDeviceAndItsPin(t *testing.T) {
	f := newJitFixture(t, ProvisionAllowNew)
	leaf := f.ca.leaf(t, "SN-0001")
	reg := f.registration(leaf)

	res, err := f.register509("pp-x", reg)
	require.NoError(t, err)
	assert.Equal(t, ProvisionCreated, res.Outcome)
	assert.Equal(t, DeriveX509DeviceToken("SN-0001"), res.DeviceToken)
	assert.Equal(t, "SN-0001", res.ExternalId)
	assert.Equal(t, string(CredentialX509Certificate), res.CredentialType)

	d := f.deviceByExternal("SN-0001")
	assert.Equal(t, "dt-a", d.DeviceType.Token)
	require.NotNil(t, d.ExpectedProvisioningProfileId)
	assert.Equal(t, f.profileID("pp-x"), *d.ExpectedProvisioningProfileId)

	creds := f.credentialsOf(d.Token)
	require.Len(t, creds, 1)
	c := creds[0]
	assert.Equal(t, string(CredentialX509Certificate), c.CredentialType)
	assert.Equal(t, x509trust.Fingerprint(leaf.cert), c.CredentialId, "the credential pins the leaf")
	require.NotNil(t, c.TrustAnchorId)
	assert.Equal(t, f.anchor.ID, *c.TrustAnchorId)
	require.NotNil(t, c.ProvisioningProfileId)
	assert.Equal(t, f.profileID("pp-x"), *c.ProvisioningProfileId)
	assert.True(t, c.Enabled)
	assert.True(t, c.CertificatePem.Valid)
	assert.WithinDuration(t, leaf.cert.NotAfter, c.ExpiresAt.Time, time.Second, "it expires with the chain")

	found, err := f.api.ResolveX509Connect(f.ctx, x509trust.Fingerprint(leaf.cert), time.Now())
	require.NoError(t, err, "the registered credential is what the connect path resolves")
	assert.Equal(t, d.Token, found.Device.Token)
	assert.Equal(t, f.anchor.ID, found.Anchor.ID)
	info := c.X509Info()
	require.NotNil(t, info)
	assert.Equal(t, "SN-0001", info.SubjectCn)

	// It is journalled once, by the profile, and replaying it writes nothing.
	var rows []map[string]any
	audit := func() []map[string]any {
		rows = nil
		require.NoError(t, f.api.RDB.Database.WithContext(core.WithSystemContext(context.Background())).
			Raw(`SELECT operation, actor FROM audit_events WHERE category = 'auth' ORDER BY id`).Scan(&rows).Error)
		return rows
	}
	require.Len(t, audit(), 1)
	assert.Equal(t, "device.provision.created", rows[0]["operation"])
	assert.Equal(t, "provisioning-profile:pp-x", rows[0]["actor"])

	before := f.counts()
	res, err = f.register509("pp-x", reg)
	require.NoError(t, err)
	assert.Equal(t, ProvisionReplayed, res.Outcome, "the same certificate again")
	assert.Equal(t, before, f.counts(), "a replay wrote rows")
	assert.Len(t, audit(), 1, "a replay is counter-only")
}

// G14's precedence, with certificate inputs: disabled, type, earmark, revoked, replay, mismatch,
// cap, then create. A device exists for the cases that need one (an earmarked device that holds a
// certificate credential from this profile, made by registering it).
func TestCertificateRegistrationPrecedence(t *testing.T) {
	t.Run("disabled device", func(t *testing.T) {
		f := newJitFixture(t, ProvisionAllowNew)
		leaf := f.ca.leaf(t, "SN-1")
		_, err := f.register509("pp-x", f.registration(leaf))
		require.NoError(t, err, "the control: it registers")
		f.sql(`UPDATE devices SET disabled_at = ? WHERE external_id = ?`, time.Now(), "SN-1")
		f.refused509(ErrProvisioningDeviceDisabled, "pp-x", f.registration(leaf))
	})
	t.Run("type mismatch", func(t *testing.T) {
		f := newJitFixture(t, ProvisionAllowNew)
		other := f.ca.leaf(t, "SN-2")
		// A device earmarked for this profile and then moved to another type.
		f.device(DeriveX509DeviceToken("SN-2"), "SN-2", "dt-a", "pp-x")
		f.sql(`UPDATE devices SET device_type_id = (SELECT id FROM device_types WHERE token = ?) WHERE external_id = ?`, "dt-b", "SN-2")
		f.refused509(ErrProvisioningTypeMismatch, "pp-x", f.registration(other))
	})
	t.Run("not earmarked", func(t *testing.T) {
		f := newJitFixture(t, ProvisionAllowNew)
		leaf := f.ca.leaf(t, "SN-3")
		f.device("dev-3", "SN-3", "dt-a", "")
		f.refused509(ErrProvisioningNotEarmarked, "pp-x", f.registration(leaf))
	})
	t.Run("revoked sticks", func(t *testing.T) {
		f := newJitFixture(t, ProvisionAllowNew)
		first := f.ca.leaf(t, "SN-4")
		_, err := f.register509("pp-x", f.registration(first))
		require.NoError(t, err)
		n, err := f.api.RevokeProvisionedDevices(f.ctx, "pp-x")
		require.NoError(t, err)
		assert.Equal(t, 1, n, "the operator control revokes the certificate credential")
		// The revoked credential's own certificate AND a newly issued one for the same name.
		f.refused509(ErrProvisioningRevoked, "pp-x", f.registration(first))
		f.refused509(ErrProvisioningRevoked, "pp-x", f.registration(f.ca.leaf(t, "SN-4")))
		// A reset removes the revoked credential, which is what ends the revocation. The device is
		// then an earmarked device with no credential, which only ADOPT can take: that is not part
		// of registration by certificate in this build, so it is refused loudly rather than
		// registered, and the revocation is not what refuses it any more.
		removed, err := f.api.ResetDeviceProvisioning(f.ctx, DeriveX509DeviceToken("SN-4"))
		require.NoError(t, err)
		assert.Equal(t, 1, removed)
		f.refused509(ErrProvisioningUnavailable, "pp-x", f.registration(f.ca.leaf(t, "SN-4")))
	})
	t.Run("replay beats mismatch and cap", func(t *testing.T) {
		f := newJitFixture(t, ProvisionAllowNew, func(r *ProvisioningProfileCreateRequest) { r.MaxDevices = i32p(1) })
		leaf := f.ca.leaf(t, "SN-5")
		reg := f.registration(leaf)
		_, err := f.register509("pp-x", reg)
		require.NoError(t, err)
		// At the cap now; the same certificate is still a replay, not a refusal.
		res, err := f.register509("pp-x", reg)
		require.NoError(t, err)
		assert.Equal(t, ProvisionReplayed, res.Outcome)
	})
	t.Run("a different certificate for a registered device is already provisioned", func(t *testing.T) {
		f := newJitFixture(t, ProvisionAllowNew)
		_, err := f.register509("pp-x", f.registration(f.ca.leaf(t, "SN-6")))
		require.NoError(t, err)
		f.refused509(ErrProvisioningAlreadyProvisioned, "pp-x", f.registration(f.ca.leaf(t, "SN-6")))
	})
	t.Run("another profile's credential is already provisioned", func(t *testing.T) {
		f := newJitFixture(t, ProvisionAllowNew)
		leaf := f.ca.leaf(t, "SN-7")
		// An operator pinned this device by hand (no profile issued it), and earmarked it.
		dev7 := DeriveX509DeviceToken("SN-7")
		f.device(dev7, "SN-7", "dt-a", "pp-x")
		pinned := f.ca.leaf(t, "SN-7")
		_, err := f.api.CreateDeviceCredential(f.ctx, x509Create("c-7", dev7, pinned.pem(), "maker"))
		require.NoError(t, err)
		f.refused509(ErrProvisioningAlreadyProvisioned, "pp-x", f.registration(leaf))
	})
	t.Run("the cap", func(t *testing.T) {
		f := newJitFixture(t, ProvisionAllowNew, func(r *ProvisioningProfileCreateRequest) { r.MaxDevices = i32p(2) })
		for _, cn := range []string{"SN-8", "SN-9"} {
			_, err := f.register509("pp-x", f.registration(f.ca.leaf(t, cn)))
			require.NoError(t, err, "the control: under the cap")
		}
		f.refused509(ErrProvisioningLimitExceeded, "pp-x", f.registration(f.ca.leaf(t, "SN-10")))
	})
	t.Run("check pre-provisioned: an unknown device", func(t *testing.T) {
		f := newJitFixture(t, ProvisionCheckPreProvisioned)
		f.refused509(ErrProvisioningNotRegistered, "pp-x", f.registration(f.ca.leaf(t, "SN-11")))
	})
	t.Run("a derived token held by another device", func(t *testing.T) {
		f := newJitFixture(t, ProvisionAllowNew)
		// A device with another external id whose TOKEN is the one SN-12 would derive.
		f.device(DeriveX509DeviceToken("SN-12"), "someone-else", "dt-a", "")
		f.refused509(ErrProvisioningTokenCollision, "pp-x", f.registration(f.ca.leaf(t, "SN-12")))
		_, err := f.register509("pp-x", f.registration(f.ca.leaf(t, "SN-13")))
		require.NoError(t, err, "the control: another name is free")
	})
}

// ADOPT is not part of registration by certificate in this build: an earmarked device that holds
// no credential is refused loudly (PROVISIONING_UNAVAILABLE), writes nothing, and the same
// device is adopted by a profile that registers by secret.
func TestCertificateRegistrationDoesNotAdopt(t *testing.T) {
	for _, strategy := range []ProvisioningStrategy{ProvisionAllowNew, ProvisionCheckPreProvisioned} {
		t.Run(string(strategy), func(t *testing.T) {
			f := newJitFixture(t, strategy)
			f.device(DeriveX509DeviceToken("SN-1"), "SN-1", "dt-a", "pp-x")
			f.refused509(ErrProvisioningUnavailable, "pp-x", f.registration(f.ca.leaf(t, "SN-1")))
		})
	}
	// The cap still comes before it (step 7 before step 8), as in the order registration by
	// secret has.
	t.Run("the cap first", func(t *testing.T) {
		f := newJitFixture(t, ProvisionAllowNew, func(r *ProvisioningProfileCreateRequest) { r.MaxDevices = i32p(1) })
		_, err := f.register509("pp-x", f.registration(f.ca.leaf(t, "SN-1")))
		require.NoError(t, err)
		f.device(DeriveX509DeviceToken("SN-2"), "SN-2", "dt-a", "pp-x")
		f.refused509(ErrProvisioningLimitExceeded, "pp-x", f.registration(f.ca.leaf(t, "SN-2")))
	})
}

// The gate under the profile's lock: what the callout verified must still be what is there.
func TestCertificateRegistrationRechecksTheProfileAndAnchorUnderTheLock(t *testing.T) {
	leafOf := func(f *jitFixture) *X509Registration { return f.registration(f.ca.leaf(t, "SN-1")) }

	t.Run("the control", func(t *testing.T) {
		f := newJitFixture(t, ProvisionAllowNew)
		_, err := f.register509("pp-x", leafOf(f))
		require.NoError(t, err)
	})
	t.Run("profile disabled since", func(t *testing.T) {
		f := newJitFixture(t, ProvisionAllowNew)
		reg, p := leafOf(f), f.profileRow("pp-x")
		f.sql(`UPDATE provisioning_profiles SET enabled = ? WHERE id = ?`, false, p.ID)
		_, err := f.api.ProvisionDevice(f.ctx, &ProvisionRequest{Profile: p, Certificate: reg})
		require.ErrorIs(t, err, ErrProvisioningFailed)
	})
	t.Run("profile expired", func(t *testing.T) {
		f := newJitFixture(t, ProvisionAllowNew)
		reg, p := leafOf(f), f.profileRow("pp-x")
		f.sql(`UPDATE provisioning_profiles SET expires_at = ? WHERE id = ?`, time.Now().Add(-time.Minute), p.ID)
		_, err := f.api.ProvisionDevice(f.ctx, &ProvisionRequest{Profile: p, Certificate: reg})
		require.ErrorIs(t, err, ErrProvisioningFailed)
	})
	t.Run("anchor disabled since", func(t *testing.T) {
		f := newJitFixture(t, ProvisionAllowNew)
		reg := leafOf(f)
		_, err := f.api.UpdateDeviceTrustAnchor(f.ctx, "maker", &DeviceTrustAnchorUpdateRequest{Enabled: optBool(false)})
		require.NoError(t, err)
		f.refused509(ErrProvisioningFailed, "pp-x", reg)
	})
	t.Run("anchor replaced since", func(t *testing.T) {
		f := newJitFixture(t, ProvisionAllowNew)
		reg := leafOf(f)
		reg.AnchorFingerprint = x509trust.Fingerprint(newRootCA(t, "Other").cert)
		f.refused509(ErrProvisioningFailed, "pp-x", reg)
	})
	t.Run("another anchor than the profile's", func(t *testing.T) {
		f := newJitFixture(t, ProvisionAllowNew)
		reg := leafOf(f)
		reg.AnchorId = f.anchor.ID + 100
		f.refused509(ErrProvisioningFailed, "pp-x", reg)
	})
	t.Run("a profile that is not a certificate profile", func(t *testing.T) {
		f := newJitFixture(t, ProvisionAllowNew)
		f.profile("pp-secret", ProvisionAllowNew)
		p, err := f.api.AuthenticateProvisioningProfile(f.ctx, f.keys["pp-secret"], f.secrets["pp-secret"])
		require.NoError(t, err)
		before := f.counts()
		_, err = f.api.ProvisionDevice(f.ctx, &ProvisionRequest{Profile: p, Certificate: leafOf(f)})
		require.ErrorIs(t, err, ErrProvisioningFailed)
		assert.Equal(t, before, f.counts())
	})
}

// What a certificate registration owes before anything is written.
func TestCertificateRegistrationValidatesItsInput(t *testing.T) {
	f := newJitFixture(t, ProvisionAllowNew)
	leaf := f.ca.leaf(t, "SN-1")
	good := f.registration(leaf)
	p := f.profileRow("pp-x")

	_, err := f.api.ProvisionDevice(f.ctx, &ProvisionRequest{Profile: p, Certificate: good})
	require.NoError(t, err, "the control")

	for name, mutate := range map[string]func(*X509Registration){
		"no leaf":    func(r *X509Registration) { r.Leaf = nil },
		"no anchor":  func(r *X509Registration) { r.AnchorId = 0 },
		"no expiry":  func(r *X509Registration) { r.NotAfter = time.Time{} },
		"no fingerp": func(r *X509Registration) { r.AnchorFingerprint = "" },
	} {
		reg := *good
		mutate(&reg)
		_, err := f.api.ProvisionDevice(f.ctx, &ProvisionRequest{Profile: p, Certificate: &reg})
		assert.Error(t, err, name)
	}
	_, err = f.api.ProvisionDevice(f.ctx, &ProvisionRequest{Profile: p, Certificate: good, ExternalId: "not-the-cn"})
	assert.Error(t, err, "an external id other than the common name")

	for name, cn := range map[string]string{"blank": " ", "padded": " SN-2", "trailing": "SN-2 ", "nul": "SN\x00-2"} {
		bad := f.ca.leaf(t, "x")
		bad.cert.Subject.CommonName = cn
		reg := *good
		reg.Leaf = bad.cert
		before := f.counts()
		_, err := f.api.ProvisionDevice(f.ctx, &ProvisionRequest{Profile: p, Certificate: &reg})
		require.Error(t, err, name)
		var refusal *ProvisioningRefusal
		require.ErrorAs(t, err, &refusal, name)
		assert.Equal(t, ProvisioningCodeInvalid, refusal.Code, name)
		assert.Equal(t, before, f.counts(), name)
	}
	require.Error(t, ValidateCertificateExternalId(""))
	require.NoError(t, ValidateCertificateExternalId("SN-0001"))
}

// Registration by certificate needs none of the keys registration by secret does, and a secret
// profile still needs them.
func TestCertificateRegistrationNeedsNoSecretKeys(t *testing.T) {
	f := newJitFixture(t, ProvisionAllowNew)
	f.api.ProvisioningChecker, f.api.DeviceSecretKey, f.api.EnrollmentKeys = nil, nil, nil
	_, err := f.register509("pp-x", f.registration(f.ca.leaf(t, "SN-1")))
	require.NoError(t, err)
	_, err = f.api.ProvisionDevice(f.ctx, &ProvisionRequest{Profile: &ProvisioningProfile{}, ExternalId: "x", CredentialSecret: material(1)})
	require.ErrorIs(t, err, ErrProvisioningUnavailable, "the secret path still fails closed without its keys")
}

// X509ProfileAnchors is how the connect path finds the profile a chain belongs to.
func TestX509ProfileAnchors(t *testing.T) {
	f := newJitFixture(t, ProvisionAllowNew)
	f.profile("pp-secret", ProvisionAllowNew)
	rows, err := f.api.X509ProfileAnchors(f.ctx)
	require.NoError(t, err)
	require.Len(t, rows, 1, "a secret profile is not listed")
	assert.Equal(t, "pp-x", rows[0].Profile.Token)
	require.NotNil(t, rows[0].Anchor)
	assert.Equal(t, f.anchor.ID, rows[0].Anchor.ID)
	now := time.Now()
	assert.True(t, rows[0].Usable(now))

	t.Run("disabled profile", func(t *testing.T) {
		g := X509ProfileAnchor{Profile: &ProvisioningProfile{TrustAnchorId: rows[0].Profile.TrustAnchorId}, Anchor: rows[0].Anchor}
		assert.False(t, g.Usable(now))
	})
	t.Run("disabled anchor", func(t *testing.T) {
		a := *rows[0].Anchor
		a.Enabled = false
		assert.False(t, X509ProfileAnchor{Profile: rows[0].Profile, Anchor: &a}.Usable(now))
		assert.False(t, X509ProfileAnchor{Profile: rows[0].Profile}.Usable(now), "no anchor row")
	})
	t.Run("expired profile", func(t *testing.T) {
		p := *rows[0].Profile
		p.ExpiresAt.Valid, p.ExpiresAt.Time = true, now.Add(-time.Second)
		assert.False(t, X509ProfileAnchor{Profile: &p, Anchor: rows[0].Anchor}.Usable(now))
		p.ExpiresAt.Time = now.Add(time.Hour)
		assert.True(t, X509ProfileAnchor{Profile: &p, Anchor: rows[0].Anchor}.Usable(now))
	})
	t.Run("another tenant sees none", func(t *testing.T) {
		other := core.WithTenant(context.Background(), "someone-else")
		got, err := f.api.X509ProfileAnchors(other)
		require.NoError(t, err)
		assert.Empty(t, got)
	})
}
