// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/devicechain-io/dc-device-management/model"
	"github.com/devicechain-io/dc-microservice/x509trust"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// X.509 REVOCATION ON THE CONNECT PATH: the REAL callout, the REAL Api over a database, the
// embedded broker for the cases that must be seen on the wire. Every refusal is the control (the
// connect that is granted) with ONE thing changed, is asserted against its own counter alone, and
// the JIT cases assert that NOTHING was written and no registration budget was charged.

func (r *jitRig) revokeCert(c *testCert) {
	r.t.Helper()
	fp := x509trust.Fingerprint(c.cert)
	_, err := r.api.RevokeDeviceCertificate(r.ctx, &model.DeviceCertificateRevokeRequest{AnchorToken: "maker", FingerprintSha256: &fp})
	require.NoError(r.t, err)
}

func (r *jitRig) unrevokeCert(c *testCert) {
	r.t.Helper()
	_, err := r.api.UnrevokeDeviceCertificate(r.ctx, "maker", x509trust.Fingerprint(c.cert))
	require.NoError(r.t, err)
}

func (r *jitRig) setMode(mode string) {
	r.t.Helper()
	require.NoError(r.t, r.api.RDB.DB(r.ctx).Model(&model.DeviceTrustAnchor{}).Where("token = ?", "maker").
		Update("revocation_mode", mode).Error)
}

// 🔑 A PINNED DEVICE WHOSE CERTIFICATE IS REVOKED IS REFUSED ON THE WIRE, ITS SIBLING IS NOT, AND
// UNREVOKING ADMITS IT AGAIN. Counted as cert_revoked, never as revoked (a disabled credential).
func TestX509ManualRevocationRefusesAPinnedDeviceOnTheWire(t *testing.T) {
	r := newJitRig(t, jitRigOpts{})
	r.device("sensor-001")
	r.device("sensor-002")
	leaf := r.ca.simDevice(t, "SN-0001", 24*time.Hour)
	sibling := r.ca.simDevice(t, "SN-0002", 24*time.Hour)
	r.pin("c-1", "sensor-001", "maker", leaf)
	r.pin("c-2", "sensor-002", "maker", sibling)

	before := r.counts()
	c, err := r.connect(clientIDOf("sensor-001"), certUser(), "", *presenting(leaf))
	require.NoError(t, err, "the control: before the revocation it connects")
	c.Disconnect(50)
	r.moved(before, x509ResultOK)

	r.revokeCert(leaf)

	before = r.counts()
	_, err = r.connect(clientIDOf("sensor-001"), certUser(), "", *presenting(leaf))
	require.Error(t, err, "a revoked certificate connected")
	r.moved(before, x509ResultCertRevoked)

	before = r.counts()
	_, err = r.connect(clientIDOf("sensor-002"), certUser(), "", *presenting(sibling))
	require.NoError(t, err, "the sibling under the same CA is untouched")
	r.moved(before, x509ResultOK)

	r.unrevokeCert(leaf)
	before = r.counts()
	_, err = r.connect(clientIDOf("sensor-001"), certUser(), "", *presenting(leaf))
	require.NoError(t, err, "unrevoked, it connects again")
	r.moved(before, x509ResultOK)
}

func TestX509RevokedIntermediateRefusesItsLeavesAndOnlyThose(t *testing.T) {
	r := newJitRig(t, jitRigOpts{})
	// The simulator's root cannot sign an intermediate (path length zero), so this PKI has its own.
	root := newTestCert(t, nil, certOpts{cn: "Maker Root 2", ca: true})
	r.anchor("maker-2", root)
	interA := newTestCert(t, root, certOpts{cn: "Line A", ca: true})
	interB := newTestCert(t, root, certOpts{cn: "Line B", ca: true})
	a := newTestCert(t, interA, certOpts{cn: "SN-A"})
	b := newTestCert(t, interB, certOpts{cn: "SN-B"})
	r.device("dev-a")
	r.device("dev-b")
	r.pin("c-a", "dev-a", "maker-2", a, interA)
	r.pin("c-b", "dev-b", "maker-2", b, interB)

	r.grants(x509ResultOK, clientIDOf("dev-a"), a, interA)
	r.grants(x509ResultOK, clientIDOf("dev-b"), b, interB)

	fp := x509trust.Fingerprint(interA.cert)
	_, err := r.api.RevokeDeviceCertificate(r.ctx, &model.DeviceCertificateRevokeRequest{AnchorToken: "maker-2", FingerprintSha256: &fp})
	require.NoError(t, err)

	r.refuses(x509ResultCertRevoked, clientIDOf("dev-a"), a, interA)
	r.grants(x509ResultOK, clientIDOf("dev-b"), b, interB)
}

// 🔑 THE JIT GATE. A revoked, never-registered certificate is refused BEFORE anything is spent or
// written: no device, no credential, no tenant allowance charged. Its unrevoked twin registers.
func TestX509JitRefusesARevokedCertificateBeforeAnythingIsSpent(t *testing.T) {
	r := newJitRig(t, jitRigOpts{sources: true})
	revoked := r.ca.simDevice(t, "SN-REVOKED", time.Hour)
	twin := r.ca.simDevice(t, "SN-TWIN", time.Hour)
	r.revokeCert(revoked)

	r.refuses(x509ResultCertRevoked, r.clientID("SN-REVOKED"), revoked)
	assert.Empty(t, r.tenants.charged(), "a revoked certificate costs the tenant no registration allowance")
	assert.Empty(t, r.sources.charged(), "and the source no allowance")
	assert.Zero(t, r.count("jit_rate_limited"))

	r.grants(x509ResultJitCreated, r.clientID("SN-TWIN"), twin)
	assert.Equal(t, []string{r.tenant}, r.tenants.charged(), "the control: the twin was charged")

	// Deleting what the revoked certificate never had changes nothing; unrevoking admits it.
	r.refuses(x509ResultCertRevoked, r.clientID("SN-REVOKED"), revoked)
	r.unrevokeCert(revoked)
	r.grants(x509ResultJitCreated, r.clientID("SN-REVOKED"), revoked)
}

// 🔑 DELETING THE DEVICE OR THE CREDENTIAL DOES NOT LET A REVOKED CERTIFICATE REGISTER AGAIN.
func TestX509JitDeleteIsNotRevocationButRevocationSurvivesDelete(t *testing.T) {
	r := newJitRig(t, jitRigOpts{})
	leaf := r.ca.simDevice(t, "SN-DEL", time.Hour)
	r.grants(x509ResultJitCreated, r.clientID("SN-DEL"), leaf)
	require.EqualValues(t, 1, r.rows().devices)

	r.revokeCert(leaf)
	r.refuses(x509ResultCertRevoked, r.clientID("SN-DEL"), leaf)

	// The device goes: without a revocation the certificate would register a NEW device.
	// (A hard delete of the rows stands for DeleteDevice, which needs the whole relationship schema
	// this fixture does not carry.)
	require.NoError(t, r.api.RDB.DB(r.ctx).Unscoped().Where("credential_type = ?", string(model.CredentialX509Certificate)).
		Delete(&model.DeviceCredential{}).Error)
	require.NoError(t, r.api.RDB.DB(r.ctx).Unscoped().Where("token = ?", model.DeriveX509DeviceToken("SN-DEL")).
		Delete(&model.Device{}).Error)
	require.Zero(t, r.rows().devices)
	r.refuses(x509ResultCertRevoked, r.clientID("SN-DEL"), leaf)
	require.Zero(t, r.rows().devices, "the revoked certificate registered a device again")

	// The control: the same delete without a revocation re-registers.
	r.unrevokeCert(leaf)
	r.grants(x509ResultJitCreated, r.clientID("SN-DEL"), leaf)
}

// revokeBeforeProvision commits a revocation in the gap between the gate and ProvisionDevice: the
// race the in-transaction recheck exists for.
type revokeBeforeProvision struct {
	JitRegistrar
	revoke func()
}

func (g *revokeBeforeProvision) ProvisionDevice(ctx context.Context, r *model.ProvisionRequest) (*model.ProvisionResult, error) {
	if g.revoke != nil {
		g.revoke()
	}
	return g.JitRegistrar.ProvisionDevice(ctx, r)
}

// 🔑 A REVOCATION THAT COMMITS BETWEEN THE GATE AND THE REGISTRATION REFUSES THE REGISTRATION WITH
// NOTHING WRITTEN: no device, no credential, no chain link. Without the recheck under the anchor
// lock the device would be registered (and only then refused by the connect check, too late).
func TestX509JitRevocationBetweenTheGateAndTheRegistrationWritesNothing(t *testing.T) {
	r := newJitRig(t, jitRigOpts{})
	racer := r.ca.simDevice(t, "SN-RACE", time.Hour)
	twin := r.ca.simDevice(t, "SN-OK", time.Hour)
	inject := &revokeBeforeProvision{JitRegistrar: r.api}
	r.gate.JitRegistrar = inject

	inject.revoke = func() { r.revokeCert(racer); inject.revoke = nil }
	r.refuses(x509ResultCertRevoked, r.clientID("SN-RACE"), racer)
	var links int64
	require.NoError(t, r.api.RDB.DB(r.ctx).Model(&model.DeviceCredentialChainLink{}).Count(&links).Error)
	assert.Zero(t, links, "a refused registration left chain links")

	// The control: the same injection point with nothing to revoke registers.
	r.grants(x509ResultJitCreated, r.clientID("SN-OK"), twin)
	require.NoError(t, r.api.RDB.DB(r.ctx).Model(&model.DeviceCredentialChainLink{}).Count(&links).Error)
	assert.EqualValues(t, 1, links, "a registered credential carries the link of its path")
}

// sameKeyIntermediate is a CA certificate for key under root: two of these with one key and one
// subject are the cross-signed (re-issued) intermediates a leaf verifies through twice.
func sameKeyIntermediate(t *testing.T, root *testCert, key *ecdsa.PrivateKey, cn string, notAfter time.Time) *testCert {
	t.Helper()
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(x509TestSerial.Add(1) + time.Now().UnixNano()), Subject: pkix.Name{CommonName: cn},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: notAfter,
		IsCA: true, BasicConstraintsValid: true, MaxPathLen: -1, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, root.cert, &key.PublicKey, root.key)
	require.NoError(t, err)
	cert, err := x509.ParseCertificate(der)
	require.NoError(t, err)
	return &testCert{cert: cert, key: key}
}

// 🔑 THE SURVIVING PATH IS THE ONE USED. A leaf verifies through two cross-signed intermediates; the
// longer-lived one is revoked. The device is admitted through the other, and what registration
// records (the credential's expiry, its chain links) and what the session is clamped to are the
// SURVIVOR's, not the path VerifyClientChain alone would have picked.
func TestX509JitRegistersWithTheSurvivingPathOfACrossSignedLeaf(t *testing.T) {
	r := newJitRig(t, jitRigOpts{})
	root := newTestCert(t, nil, certOpts{cn: "Maker Root X", ca: true})
	r.anchor("maker-x", root)
	r.addProfile("pp-x2", "maker-x", model.ProvisionAllowNew, nil)
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	ixOld := sameKeyIntermediate(t, root, key, "Line CA", time.Now().Add(10*time.Hour))
	ixNew := sameKeyIntermediate(t, root, key, "Line CA", time.Now().Add(48*time.Hour))
	leaf := newTestCert(t, ixNew, certOpts{cn: "SN-X", notAfter: time.Now().Add(20 * time.Hour)})

	// The control: with nothing revoked the longest-lived path (through ixNew, 20 h) is the one.
	probe := newTestCert(t, ixNew, certOpts{cn: "SN-PROBE", notAfter: time.Now().Add(20 * time.Hour)})
	userJWT, why := r.authorize(r.clientID("SN-PROBE"), probe, ixOld, ixNew)
	require.Empty(t, why)
	assert.Greater(t, expiryOf(t, userJWT), ixOld.cert.NotAfter.Unix()+3600,
		"the control: the unrevoked chain is bounded by its longest-lived path (the session lifetime), not by the shorter intermediate")

	fp := x509trust.Fingerprint(ixNew.cert)
	_, err = r.api.RevokeDeviceCertificate(r.ctx, &model.DeviceCertificateRevokeRequest{AnchorToken: "maker-x", FingerprintSha256: &fp})
	require.NoError(t, err)

	userJWT, why = r.authorize(r.clientID("SN-X"), leaf, ixOld, ixNew)
	require.Empty(t, why, "the leaf has a valid alternative path and must be admitted")
	assert.InDelta(t, ixOld.cert.NotAfter.Unix(), expiryOf(t, userJWT), 1, "the session outlived the surviving path's intermediate")
	var cred model.DeviceCredential
	require.NoError(t, r.api.RDB.DB(r.ctx).Where("credential_id = ?", x509trust.Fingerprint(leaf.cert)).First(&cred).Error)
	assert.WithinDuration(t, ixOld.cert.NotAfter, cred.ExpiresAt.Time, time.Second,
		"the credential was registered with the revoked path's expiry")
	var links []model.DeviceCredentialChainLink
	require.NoError(t, r.api.RDB.DB(r.ctx).Where("credential_id = ?", cred.ID).Order("depth").Find(&links).Error)
	require.Len(t, links, 2)
	assert.Equal(t, x509trust.Fingerprint(ixOld.cert), links[1].Fingerprint, "the chain link records the revoked intermediate, not the surviving one")

	// And with both paths revoked the same leaf is refused.
	fp2 := x509trust.Fingerprint(ixOld.cert)
	_, err = r.api.RevokeDeviceCertificate(r.ctx, &model.DeviceCertificateRevokeRequest{AnchorToken: "maker-x", FingerprintSha256: &fp2})
	require.NoError(t, err)
	r.refuses(x509ResultCertRevoked, r.clientID("SN-X"), leaf, ixOld, ixNew)
}

// REQUIRE_CURRENT is decided from the anchor row: no list at all is revocation_unknown, on the
// pinned path and the JIT path alike (the latter writing nothing); the same chain under ENTRIES is
// admitted.
func TestX509RequireCurrentWithoutAListIsRevocationUnknown(t *testing.T) {
	r := newJitRig(t, jitRigOpts{})
	r.device("sensor-001")
	pinned := r.ca.simDevice(t, "SN-PIN", time.Hour)
	fresh := r.ca.simDevice(t, "SN-NEW", time.Hour)
	r.pin("c-1", "sensor-001", "maker", pinned)

	r.grants(x509ResultOK, clientIDOf("sensor-001"), pinned)
	r.setMode("REQUIRE_CURRENT")
	r.refuses(x509ResultRevocationUnknown, clientIDOf("sensor-001"), pinned)
	r.refuses(x509ResultRevocationUnknown, r.clientID("SN-NEW"), fresh)
	assert.Empty(t, r.tenants.charged(), "an unknown revocation state costs no registration allowance")
	r.setMode("ENTRIES")
	r.grants(x509ResultOK, clientIDOf("sensor-001"), pinned)
	r.grants(x509ResultJitCreated, r.clientID("SN-NEW"), fresh)
}

// faultyRevocationApi makes the revocation read fail or answer something ApplyRevocation cannot
// judge, to prove both refuse.
type faultyRevocationApi struct {
	model.DeviceManagementApi
	err   error
	state *x509trust.RevocationState
	calls int
}

func (a *faultyRevocationApi) ResolveX509Revocation(ctx context.Context, anchor *model.DeviceTrustAnchor,
	keys x509trust.RevocationQuery, now time.Time) (x509trust.RevocationState, error) {
	a.calls++
	if a.err != nil {
		return x509trust.RevocationState{}, a.err
	}
	if a.state != nil {
		return *a.state, nil
	}
	return a.DeviceManagementApi.ResolveX509Revocation(ctx, anchor, keys, now)
}

// 🔴 FAIL CLOSED: a revocation lookup that errors refuses (error), and an ApplyRevocation error
// that is not one of the two classified reasons refuses (error) too. Each is paired with the
// passthrough that is granted.
func TestX509RevocationFailsClosed(t *testing.T) {
	r := newJitRig(t, jitRigOpts{})
	r.device("sensor-001")
	pinned := r.ca.simDevice(t, "SN-PIN", time.Hour)
	fresh := r.ca.simDevice(t, "SN-NEW", time.Hour)
	r.pin("c-1", "sensor-001", "maker", pinned)
	real := r.resp.api
	faulty := &faultyRevocationApi{DeviceManagementApi: real}
	r.resp.api = faulty

	r.grants(x509ResultOK, clientIDOf("sensor-001"), pinned)
	require.Equal(t, 1, faulty.calls, "the check ran on the pinned connect")

	faulty.err = errors.New("the database is unreachable")
	r.refuses(x509ResultError, clientIDOf("sensor-001"), pinned)
	r.refuses(x509ResultError, r.clientID("SN-NEW"), fresh)
	assert.Empty(t, r.tenants.charged(), "a JIT refused on a failed lookup was charged")

	faulty.err = nil
	// A state ApplyRevocation cannot judge (no mode): an error whose ReasonOf is empty.
	faulty.state = &x509trust.RevocationState{Now: time.Now()}
	r.refuses(x509ResultError, clientIDOf("sensor-001"), pinned)
	r.refuses(x509ResultError, r.clientID("SN-NEW"), fresh)

	faulty.state = nil
	r.grants(x509ResultOK, clientIDOf("sensor-001"), pinned)
	r.grants(x509ResultJitCreated, r.clientID("SN-NEW"), fresh)
}

// A DB failure on the one read of the real Api refuses too (not only a stub's error).
func TestX509RevocationDatabaseFailureRefusesTheConnect(t *testing.T) {
	r := newJitRig(t, jitRigOpts{})
	r.device("sensor-001")
	pinned := r.ca.simDevice(t, "SN-PIN", time.Hour)
	r.pin("c-1", "sensor-001", "maker", pinned)
	r.revokeCert(r.ca.simDevice(t, "SN-OTHER", time.Hour)) // so an ENTRIES anchor reads on a connect
	r.grants(x509ResultOK, clientIDOf("sensor-001"), pinned)

	require.NoError(t, r.api.RDB.DB(r.ctx).Migrator().RenameTable("device_revoked_certificates", "device_revoked_certificates_x"))
	r.refuses(x509ResultError, clientIDOf("sensor-001"), pinned)
	require.NoError(t, r.api.RDB.DB(r.ctx).Migrator().RenameTable("device_revoked_certificates_x", "device_revoked_certificates"))
	r.grants(x509ResultOK, clientIDOf("sensor-001"), pinned)
}

func TestRevocationRefusalMapsReasonsAndRefusesByDefault(t *testing.T) {
	assert.Equal(t, x509ResultCertRevoked, revocationRefusal(&x509trust.ChainError{Reason: x509trust.ReasonRevoked}))
	assert.Equal(t, x509ResultRevocationUnknown, revocationRefusal(&x509trust.ChainError{Reason: x509trust.ReasonRevocationUnknown}))
	assert.Equal(t, x509ResultError, revocationRefusal(&x509trust.ChainError{Reason: x509trust.ReasonExpired}), "another reason")
	assert.Equal(t, x509ResultError, revocationRefusal(&x509trust.ChainError{}), "an unclassified chain error")
	assert.Equal(t, x509ResultError, revocationRefusal(errors.New("anything else")))
	assert.Equal(t, x509ResultError, revocationRefusal(x509trust.ErrRevocationState))
}

// The new results are in the closed vocabulary, so they exist at zero from the start.
func TestRevocationResultsAreInTheClosedVocabulary(t *testing.T) {
	assert.Contains(t, x509Results, x509ResultCertRevoked)
	assert.Contains(t, x509Results, x509ResultRevocationUnknown)
	assert.NotEqual(t, x509ResultRevoked, x509ResultCertRevoked, "a CA's revocation is not a disabled credential")
	r := newJitRig(t, jitRigOpts{})
	assert.Zero(t, r.count(x509ResultCertRevoked))
	assert.Zero(t, r.count(x509ResultRevocationUnknown))
}

// 🔴 NO CRL PARSING OR SIGNATURE VERIFICATION IS REACHABLE FROM THE CALLOUT. The connect path reads
// stored rows; the CRL is parsed and verified once, at upload.
func TestNoCRLParsingIsReachableFromTheProcessorPackage(t *testing.T) {
	files, err := filepath.Glob("*.go")
	require.NoError(t, err)
	require.NotEmpty(t, files)
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		src, err := os.ReadFile(f)
		require.NoError(t, err)
		for _, banned := range []string{"ParseRevocationList", "VerifyCAChain", "CheckCRLSignature", "ParseRevocationListLimits"} {
			assert.NotContains(t, string(src), banned, "%s: the callout must not parse or verify a CRL", f)
		}
	}
}

// revokeAfterProvision commits a revocation right after ProvisionDevice returns, before the
// credential is read back and the connect is granted.
type revokeAfterProvision struct {
	JitRegistrar
	revoke func()
}

func (g *revokeAfterProvision) ProvisionDevice(ctx context.Context, r *model.ProvisionRequest) (*model.ProvisionResult, error) {
	res, err := g.JitRegistrar.ProvisionDevice(ctx, r)
	if err == nil && g.revoke != nil {
		g.revoke()
		g.revoke = nil
	}
	return res, err
}

// 🔑 THE CHECK AFTER REGISTRATION. A certificate revoked after its device registered, but before the
// connect was granted, is refused by the connect check on the registered credential, and the device
// stays registered (the revocation, not the registration, is what refuses it).
func TestX509RevocationAfterRegistrationRefusesTheGrant(t *testing.T) {
	r := newJitRig(t, jitRigOpts{})
	leaf := r.ca.simDevice(t, "SN-LATE", time.Hour)
	inject := &revokeAfterProvision{JitRegistrar: r.api, revoke: func() { r.revokeCert(leaf) }}
	r.gate.JitRegistrar = inject

	before := r.counts()
	userJWT, why := r.authorize(r.clientID("SN-LATE"), leaf)
	require.Empty(t, userJWT, "a certificate revoked after registration was granted a session")
	require.Equal(t, genericAuthFailure, why)
	r.moved(before, x509ResultCertRevoked)
	assert.EqualValues(t, 1, r.rows().devices, "the device stays registered")
	assert.EqualValues(t, 1, r.rows().credentials)
}

// 🔑 THE SURVIVING PATH CLAMPS A PINNED CONNECT TOO. A pinned leaf verifies through two cross-signed
// intermediates; the longer-lived one is revoked; the session ends with the survivor's notAfter.
func TestX509PinnedSessionIsClampedToTheSurvivingPath(t *testing.T) {
	r := newJitRig(t, jitRigOpts{})
	root := newTestCert(t, nil, certOpts{cn: "Maker Root P", ca: true})
	r.anchor("maker-p", root)
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	ixOld := sameKeyIntermediate(t, root, key, "Line CA", time.Now().Add(10*time.Hour))
	ixNew := sameKeyIntermediate(t, root, key, "Line CA", time.Now().Add(48*time.Hour))
	leaf := newTestCert(t, ixNew, certOpts{cn: "SN-P", notAfter: time.Now().Add(20 * time.Hour)})
	r.device("sensor-p")
	r.pin("c-p", "sensor-p", "maker-p", leaf, ixOld, ixNew)

	userJWT, why := r.authorize(clientIDOf("sensor-p"), leaf, ixOld, ixNew)
	require.Empty(t, why)
	require.Greater(t, expiryOf(t, userJWT), ixOld.cert.NotAfter.Unix()+3600, "the control: before the revoke the longer path bounds the session")

	fp := x509trust.Fingerprint(ixNew.cert)
	_, err = r.api.RevokeDeviceCertificate(r.ctx, &model.DeviceCertificateRevokeRequest{AnchorToken: "maker-p", FingerprintSha256: &fp})
	require.NoError(t, err)
	userJWT, why = r.authorize(clientIDOf("sensor-p"), leaf, ixOld, ixNew)
	require.Empty(t, why, "the surviving path admits it")
	assert.InDelta(t, ixOld.cert.NotAfter.Unix(), expiryOf(t, userJWT), 1, "the session must end with the surviving path's notAfter")
}

// 🔑 THE JIT GATE ASKS EVERY USABLE MATCHED ANCHOR. A chain reaches a root-anchored profile and an
// intermediate-anchored one; the narrowest (the intermediate's) is chosen to register, but a
// revocation recorded under EITHER anchor refuses it, before anything is spent or written. With
// nothing recorded under any anchor no revocation query runs.
func TestX509JitGateAsksEveryMatchedAnchor(t *testing.T) {
	r := newJitRig(t, jitRigOpts{sources: true})
	root := newTestCert(t, nil, certOpts{cn: "Maker Root G", ca: true})
	inter := newTestCert(t, root, certOpts{cn: "Line CA G", ca: true})
	// The ROOT's profile is created first, so a gate that looked at usable[0] (not at every
	// anchor, nor at the chosen one) would ask the wrong store.
	r.anchor("root-g", root)
	r.anchor("inter-g", inter)
	r.addProfile("pp-root-g", "root-g", model.ProvisionAllowNew, nil)
	r.addProfile("pp-inter-g", "inter-g", model.ProvisionAllowNew, nil)
	revokeUnder := func(anchor string, c *testCert) {
		fp := x509trust.Fingerprint(c.cert)
		_, err := r.api.RevokeDeviceCertificate(r.ctx, &model.DeviceCertificateRevokeRequest{AnchorToken: anchor, FingerprintSha256: &fp})
		require.NoError(r.t, err)
	}

	// Nothing recorded under either anchor: registers, and no revocation table is read.
	var reads atomic.Int64
	name := "test:count-jit-gate-reads"
	require.NoError(t, r.api.RDB.Database.Callback().Query().Before("gorm:query").Register(name, func(d *gorm.DB) {
		if d.Statement.Table == "device_revoked_certificates" || d.Statement.Table == "device_revocation_lists" {
			reads.Add(1)
		}
	}))
	clean := newTestCert(t, inter, certOpts{cn: "SN-G0"})
	r.grants(x509ResultJitCreated, r.clientID("SN-G0"), clean, inter)
	require.NoError(t, r.api.RDB.Database.Callback().Query().Remove(name))
	assert.Zero(t, reads.Load(), "a query ran for ENTRIES anchors with nothing recorded")

	// Revoked under the CHOSEN (intermediate) anchor, and under the other (root) one.
	viaChosen := newTestCert(t, inter, certOpts{cn: "SN-G1"})
	viaOther := newTestCert(t, inter, certOpts{cn: "SN-G2"})
	revokeUnder("inter-g", viaChosen)
	revokeUnder("root-g", viaOther)
	charged, sourceCharged := len(r.tenants.charged()), len(r.sources.charged())
	r.refuses(x509ResultCertRevoked, r.clientID("SN-G1"), viaChosen, inter)
	r.refuses(x509ResultCertRevoked, r.clientID("SN-G2"), viaOther, inter)
	assert.Len(t, r.tenants.charged(), charged, "a revoked certificate was charged against the registration allowance")
	assert.Len(t, r.sources.charged(), sourceCharged, "and the source allowance")

	// The control: an unrevoked twin still registers.
	r.grants(x509ResultJitCreated, r.clientID("SN-G3"), newTestCert(t, inter, certOpts{cn: "SN-G3"}), inter)
}
