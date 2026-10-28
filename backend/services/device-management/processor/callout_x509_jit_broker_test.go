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
	"fmt"
	"math/big"
	"sync"
	"testing"
	"time"

	"github.com/devicechain-io/dc-device-management/model"
	"github.com/devicechain-io/dc-microservice/core"
	dcgraphql "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-microservice/x509trust"
	"github.com/nats-io/jwt/v2"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// REGISTRATION ON FIRST CONNECT BY CERTIFICATE, END TO END: the embedded broker that requests
// client certificates, the REAL callout, a REAL TLS client presenting a certificate minted the way
// the simulator's --x509-jit mints one, and the REAL device-management Api. A refusal changes ONE
// thing from a connect that succeeds, is asserted against its own counter alone, and writes
// nothing; the wire answer is the generic denial for all of them.

// ---------------------------------------------------------------------------------------------
// Certificates minted as dc-simulator's x509sim does: a P-256 root that may sign only leaves
// (MaxPathLenZero), and leaves with the common name set to the device's external id, a clientAuth
// extended key usage, BasicConstraints present and not a CA, notBefore backdated by 5 minutes.

func simCA(t testing.TB, name string) *testCert {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(x509TestSerial.Add(1) + now.UnixNano()),
		Subject:               pkix.Name{CommonName: name, Organization: []string{"dc-simulator test CA"}},
		NotBefore:             now.Add(-5 * time.Minute),
		NotAfter:              now.Add(5 * 365 * 24 * time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		MaxPathLenZero:        true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	require.NoError(t, err)
	cert, err := x509.ParseCertificate(der)
	require.NoError(t, err)
	return &testCert{cert: cert, key: key}
}

func (ca *testCert) simDevice(t testing.TB, cn string, validity time.Duration) *testCert {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	now := time.Now()
	notAfter := now.Add(validity)
	if notAfter.After(ca.cert.NotAfter) {
		notAfter = ca.cert.NotAfter
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(x509TestSerial.Add(1) + now.UnixNano()),
		Subject:               pkix.Name{CommonName: cn},
		NotBefore:             now.Add(-5 * time.Minute),
		NotAfter:              notAfter,
		BasicConstraintsValid: true,
		IsCA:                  false,
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca.cert, &key.PublicKey, ca.key)
	require.NoError(t, err)
	cert, err := x509.ParseCertificate(der)
	require.NoError(t, err)
	return &testCert{cert: cert, key: key}
}

// ---------------------------------------------------------------------------------------------
// The budgets' test doubles.

// fakeAllower is a bucket the test controls and reads: what it was charged for, and whether it
// admits.
type fakeAllower struct {
	mu    sync.Mutex
	deny  bool
	calls []string
}

func (a *fakeAllower) Allow(key string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.calls = append(a.calls, key)
	return !a.deny
}

func (a *fakeAllower) set(deny bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.deny = deny
}

func (a *fakeAllower) charged() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]string(nil), a.calls...)
}

// gateRegistrar is the Api, optionally holding every registration at its door so a test can
// stack them up.
type gateRegistrar struct {
	JitRegistrar
	mu      sync.Mutex
	hold    chan struct{}
	entered chan struct{}
}

func (g *gateRegistrar) ProvisionDevice(ctx context.Context, r *model.ProvisionRequest) (*model.ProvisionResult, error) {
	g.mu.Lock()
	hold, entered := g.hold, g.entered
	g.mu.Unlock()
	if hold != nil {
		entered <- struct{}{}
		<-hold
	}
	return g.JitRegistrar.ProvisionDevice(ctx, r)
}

func (g *gateRegistrar) holdAtTheDoor() (entered <-chan struct{}, release func()) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.hold, g.entered = make(chan struct{}), make(chan struct{}, 64)
	h := g.hold
	var once sync.Once
	return g.entered, func() { once.Do(func() { close(h) }) }
}

// ---------------------------------------------------------------------------------------------
// The rig.

type jitRigOpts struct {
	fixtureOpts
	maxInFlight int
	sources     bool
	strategy    model.ProvisioningStrategy
	maxDevices  *int32
	noProfile   bool
}

type jitRig struct {
	*x509Fixture
	ca      *testCert
	tenants *fakeAllower
	sources *fakeAllower
	gate    *gateRegistrar
	profile string
}

func newJitRig(t *testing.T, o jitRigOpts) *jitRig {
	t.Helper()
	if o.maxInFlight == 0 {
		o.maxInFlight = 16
	}
	if o.strategy == "" {
		o.strategy = model.ProvisionAllowNew
	}
	r := &jitRig{tenants: &fakeAllower{}, sources: &fakeAllower{}, profile: "pp-x"}
	fo := o.fixtureOpts
	fo.configure = func(t *testing.T, resp *CalloutResponder, api *model.Api) {
		r.gate = &gateRegistrar{JitRegistrar: api}
		cfg := JitConfig{Registrar: r.gate, Tenants: r.tenants, MaxInFlight: o.maxInFlight}
		if o.sources {
			cfg.Sources = r.sources
		}
		_, err := resp.WithCertificateRegistration(cfg)
		require.NoError(t, err)
	}
	r.x509Fixture = newX509FixtureWith(t, fo)
	r.ca = simCA(t, "Maker Root")
	r.anchor("maker", r.ca)
	if !o.noProfile {
		r.addProfile(r.profile, "maker", o.strategy, o.maxDevices)
	}
	return r
}

func (r *jitRig) addProfile(token, anchor string, strategy model.ProvisioningStrategy, maxDevices *int32) {
	r.t.Helper()
	_, err := r.api.CreateProvisioningProfile(r.ctx, &model.ProvisioningProfileCreateRequest{
		Token: token, ProvisionKey: "key-" + token, Strategy: string(strategy), DeviceTypeToken: "dt-a",
		Enabled: true, TrustAnchorToken: &anchor, MaxDevices: maxDevices})
	require.NoError(r.t, err)
}

// setProfileCredentialType rewrites the profile's credential type underneath the service, as a
// defect or an older row would.
func (r *jitRig) setProfileCredentialType(ctype string) {
	r.t.Helper()
	require.NoError(r.t, r.api.RDB.Database.WithContext(core.WithSystemContext(context.Background())).
		Exec(`UPDATE provisioning_profiles SET credential_type = ? WHERE token = ?`, ctype, "pp-x").Error)
}

func (r *jitRig) clientID(cn string) string {
	return "inst-1:" + r.tenant + ":" + model.DeriveX509DeviceToken(cn)
}

func (r *jitRig) user() string { return r.tenant + ":" + X509Username }

// authorize runs the decision for a connect presenting chain under clientID, from a stable
// address.
func (r *jitRig) authorize(clientID string, chain ...*testCert) (string, string) {
	r.t.Helper()
	req := testRequestFromClient(r.t, r.user(), "", clientID)
	req.ClientInformation.Host = "10.1.2.3"
	req.TLS = &jwt.ClientTLS{Certs: pemsOf(chain...)}
	return r.resp.authorize(req)
}

// written is what a registration could have written.
type jitRows struct{ devices, credentials int64 }

func (r *jitRig) rows() jitRows {
	r.t.Helper()
	var out jitRows
	require.NoError(r.t, r.api.RDB.DB(r.ctx).Model(&model.Device{}).Count(&out.devices).Error)
	require.NoError(r.t, r.api.RDB.DB(r.ctx).Model(&model.DeviceCredential{}).Count(&out.credentials).Error)
	return out
}

// grants asserts a connect is granted and counted as result alone.
func (r *jitRig) grants(result, clientID string, chain ...*testCert) {
	r.t.Helper()
	before := r.counts()
	userJWT, why := r.authorize(clientID, chain...)
	require.Empty(r.t, why, "refused: %s", why)
	require.NotEmpty(r.t, userJWT)
	r.moved(before, result)
}

// refuses asserts a connect is refused with the generic denial, counted as result alone, and that
// it wrote NOTHING.
func (r *jitRig) refuses(result, clientID string, chain ...*testCert) {
	r.t.Helper()
	before, rows := r.counts(), r.rows()
	userJWT, why := r.authorize(clientID, chain...)
	require.Empty(r.t, userJWT, "granted a connect that must be refused as %s", result)
	require.Equal(r.t, genericAuthFailure, why, "the wire answer must be the generic denial")
	r.moved(before, result)
	require.Equal(r.t, rows, r.rows(), "a refused %s wrote rows", result)
}

// ---------------------------------------------------------------------------------------------

// 🔑 THE WHOLE PATH ON THE WIRE. A device nobody has registered connects with a certificate its CA
// issued and the client id its certificate derives; it is registered, granted, and the next
// connect with the same certificate is a pinned one.
func TestX509JitRegistersOnTheWireAndThenConnectsPinned(t *testing.T) {
	r := newJitRig(t, jitRigOpts{})
	leaf := r.ca.simDevice(t, "SN-0001", 24*time.Hour)

	// Without the certificate being presented at all, nothing is registered (the control).
	before := r.counts()
	_, err := r.connect(r.clientID("SN-0001"), r.user(), "")
	require.Error(t, err)
	r.moved(before, x509ResultNoCert)
	require.Zero(t, r.rows().devices)

	before = r.counts()
	c, err := r.connect(r.clientID("SN-0001"), r.user(), "", *presenting(leaf))
	require.NoError(t, err, "a device that has never been registered must be registered by its first connect")
	require.True(t, c.IsConnectionOpen())
	r.moved(before, x509ResultJitCreated)

	dev := model.DeriveX509DeviceToken("SN-0001")
	devices, err := r.api.DevicesByToken(r.ctx, []string{dev})
	require.NoError(t, err)
	require.Len(t, devices, 1, "the device was not created")
	assert.Equal(t, "dt-a", devices[0].DeviceType.Token)
	var creds []*model.DeviceCredential
	require.NoError(t, r.api.RDB.DB(r.ctx).Where("credential_type = ?", string(model.CredentialX509Certificate)).Find(&creds).Error)
	require.Len(t, creds, 1)
	assert.Equal(t, string(model.CredentialX509Certificate), creds[0].CredentialType)
	assert.Equal(t, x509trust.Fingerprint(leaf.cert), creds[0].CredentialId)

	// The next connect is the pinned path: ok, not jit.
	c.Disconnect(50)
	before = r.counts()
	_, err = r.connect(r.clientID("SN-0001"), r.user(), "", *presenting(leaf))
	require.NoError(t, err)
	r.moved(before, x509ResultOK)

	// The tenant was charged once, for the registration, and not for the pinned connect.
	assert.Equal(t, []string{r.tenant}, r.tenants.charged())
	assert.Zero(t, testutil.ToFloat64(r.metrics.JitInFlight), "the in-flight gauge is back to zero")
}

// The session a registered device is granted is clamped to its chain like a pinned one.
func TestX509JitSessionIsClampedToTheCertificate(t *testing.T) {
	r := newJitRig(t, jitRigOpts{})
	leaf := r.ca.simDevice(t, "SN-CLAMP", 2*time.Hour)
	userJWT, why := r.authorize(r.clientID("SN-CLAMP"), leaf)
	require.Empty(t, why)
	assert.InDelta(t, leaf.cert.NotAfter.Unix(), expiryOf(t, userJWT), 1, "the session must end with the certificate")
}

// The credential a registration writes, and the session it is granted, expire with the EARLIEST
// notAfter over the chain: here an intermediate that ends long before the device's certificate.
func TestX509JitCredentialAndSessionExpireWithTheEarliestCertificateInTheChain(t *testing.T) {
	r := newJitRig(t, jitRigOpts{})
	// The simulator's root cannot sign an intermediate (path length zero), so this PKI has its own
	// root, anchor and profile.
	root := newTestCert(t, nil, certOpts{cn: "Maker Root 2", ca: true})
	r.anchor("maker-2", root)
	r.addProfile("pp-2", "maker-2", model.ProvisionAllowNew, nil)
	inter := newTestCert(t, root, certOpts{cn: "Line CA", ca: true, notAfter: time.Now().Add(2 * time.Hour)})
	leaf := newTestCert(t, inter, certOpts{cn: "SN-INTER", notAfter: time.Now().Add(48 * time.Hour)})
	userJWT, why := r.authorize(r.clientID("SN-INTER"), leaf, inter)
	require.Empty(t, why)
	assert.InDelta(t, inter.cert.NotAfter.Unix(), expiryOf(t, userJWT), 1, "the session outlived the intermediate")
	var cred model.DeviceCredential
	require.NoError(t, r.api.RDB.DB(r.ctx).Where("credential_type = ?", string(model.CredentialX509Certificate)).First(&cred).Error)
	assert.WithinDuration(t, inter.cert.NotAfter, cred.ExpiresAt.Time, time.Second, "the credential outlives the chain that vouched for it")
	// Without its intermediate the same leaf is refused and nothing more is written (the control above).
	before := r.rows()
	_, why = r.authorize(r.clientID("SN-INTER-2"), newTestCert(t, inter, certOpts{cn: "SN-INTER-2"}))
	assert.Equal(t, genericAuthFailure, why)
	assert.Equal(t, before, r.rows())
}

// 🔑 EVERY WAY A FIRST CONNECT IS REFUSED BEFORE ANYTHING IS WRITTEN, each paired with the same
// connect corrected in its one input.
func TestX509JitRefusalsWriteNothingAndEachHasItsControl(t *testing.T) {
	t.Run("a client id that is not the one the certificate derives", func(t *testing.T) {
		r := newJitRig(t, jitRigOpts{})
		leaf := r.ca.simDevice(t, "SN-1", time.Hour)
		r.refuses(x509ResultClientIDMismatch, r.clientID("SN-OTHER"), leaf)
		r.refuses(x509ResultClientIDMismatch, "inst-1:"+r.tenant+":sensor-001", leaf)
		r.refuses(x509ResultClientIDMismatch, "", leaf)
		// Not even the budgets were consulted: the check is the cheapest and comes first.
		assert.Empty(t, r.tenants.charged())
		r.grants(x509ResultJitCreated, r.clientID("SN-1")+":discriminator", leaf)
	})
	t.Run("a chain that reaches no profile's anchor", func(t *testing.T) {
		r := newJitRig(t, jitRigOpts{})
		stranger := simCA(t, "Stranger Root").simDevice(t, "SN-2", time.Hour)
		r.refuses(x509ResultChainInvalid, r.clientID("SN-2"), stranger)
		assert.Empty(t, r.tenants.charged(), "a chain that verifies to nothing costs the tenant nothing")
		r.grants(x509ResultJitCreated, r.clientID("SN-2"), r.ca.simDevice(t, "SN-2", time.Hour))
	})
	t.Run("an expired certificate", func(t *testing.T) {
		r := newJitRig(t, jitRigOpts{})
		old := newTestCert(t, r.ca, certOpts{cn: "SN-3", notBefore: time.Now().Add(-48 * time.Hour), notAfter: time.Now().Add(-time.Hour)})
		r.refuses(x509ResultChainInvalid, r.clientID("SN-3"), old)
		r.grants(x509ResultJitCreated, r.clientID("SN-3"), r.ca.simDevice(t, "SN-3", time.Hour))
	})
	t.Run("a certificate without the clientAuth usage", func(t *testing.T) {
		r := newJitRig(t, jitRigOpts{})
		server := newTestCert(t, r.ca, certOpts{cn: "SN-4", server: true})
		r.refuses(x509ResultChainInvalid, r.clientID("SN-4"), server)
		r.grants(x509ResultJitCreated, r.clientID("SN-4"), r.ca.simDevice(t, "SN-4", time.Hour))
	})
	t.Run("a tenant with no certificate profile", func(t *testing.T) {
		r := newJitRig(t, jitRigOpts{noProfile: true})
		leaf := r.ca.simDevice(t, "SN-5", time.Hour)
		r.refuses(x509ResultUnknownPin, r.clientID("SN-5"), leaf)
		r.addProfile("pp-late", "maker", model.ProvisionAllowNew, nil)
		r.grants(x509ResultJitCreated, r.clientID("SN-5"), leaf)
	})
	t.Run("a profile that is disabled", func(t *testing.T) {
		r := newJitRig(t, jitRigOpts{})
		leaf := r.ca.simDevice(t, "SN-6", time.Hour)
		_, err := r.api.UpdateProvisioningProfile(r.ctx, "pp-x", &model.ProvisioningProfileUpdateRequest{Enabled: dcgraphql.OptionalBoolOf(false)})
		require.NoError(t, err)
		r.refuses("jit_provisioning_failed", r.clientID("SN-6"), leaf)
		_, err = r.api.UpdateProvisioningProfile(r.ctx, "pp-x", &model.ProvisioningProfileUpdateRequest{Enabled: dcgraphql.OptionalBoolOf(true)})
		require.NoError(t, err)
		r.grants(x509ResultJitCreated, r.clientID("SN-6"), leaf)
	})
	t.Run("an anchor that is disabled", func(t *testing.T) {
		r := newJitRig(t, jitRigOpts{})
		leaf := r.ca.simDevice(t, "SN-7", time.Hour)
		_, err := r.api.UpdateDeviceTrustAnchor(r.ctx, "maker", &model.DeviceTrustAnchorUpdateRequest{Enabled: dcgraphql.OptionalBoolOf(false)})
		require.NoError(t, err)
		r.refuses(x509ResultAnchorDisabled, r.clientID("SN-7"), leaf)
		_, err = r.api.UpdateDeviceTrustAnchor(r.ctx, "maker", &model.DeviceTrustAnchorUpdateRequest{Enabled: dcgraphql.OptionalBoolOf(true)})
		require.NoError(t, err)
		r.grants(x509ResultJitCreated, r.clientID("SN-7"), leaf)
	})
	t.Run("a chain that belongs to two profiles", func(t *testing.T) {
		r := newJitRig(t, jitRigOpts{})
		leaf := r.ca.simDevice(t, "SN-8", time.Hour)
		r.addProfile("pp-twin", "maker", model.ProvisionAllowNew, nil)
		r.refuses(x509ResultJitAmbiguous, r.clientID("SN-8"), leaf)
		assert.Empty(t, r.tenants.charged())
		_, err := r.api.UpdateProvisioningProfile(r.ctx, "pp-twin", &model.ProvisioningProfileUpdateRequest{Enabled: dcgraphql.OptionalBoolOf(false)})
		require.NoError(t, err)
		r.grants(x509ResultJitCreated, r.clientID("SN-8"), leaf)
	})
	t.Run("a common name that cannot be an external id", func(t *testing.T) {
		r := newJitRig(t, jitRigOpts{})
		padded := r.ca.simDevice(t, " SN-9", time.Hour)
		r.refuses("jit_invalid_value", r.clientID(" SN-9"), padded)
		empty := newTestCert(t, r.ca, certOpts{cn: ""})
		r.refuses("jit_invalid_value", r.clientID("x"), empty)
		r.grants(x509ResultJitCreated, r.clientID("SN-9"), r.ca.simDevice(t, "SN-9", time.Hour))
	})
	t.Run("a password with the certificate", func(t *testing.T) {
		r := newJitRig(t, jitRigOpts{})
		leaf := r.ca.simDevice(t, "SN-10", time.Hour)
		req := testRequestFromClient(t, r.user(), "hunter2hunter2", r.clientID("SN-10"))
		req.TLS = &jwt.ClientTLS{Certs: pemsOf(leaf)}
		before, rows := r.counts(), r.rows()
		_, why := r.resp.authorize(req)
		require.Equal(t, genericAuthFailure, why)
		r.moved(before, x509ResultPasswordPresented)
		require.Equal(t, rows, r.rows())
		r.grants(x509ResultJitCreated, r.clientID("SN-10"), leaf)
	})
	t.Run("an unsealed request when an xkey is configured", func(t *testing.T) {
		r := newJitRig(t, jitRigOpts{})
		leaf := r.ca.simDevice(t, "SN-11", time.Hour)
		req := testRequestFromClient(t, r.user(), "", r.clientID("SN-11"))
		req.TLS = &jwt.ClientTLS{Certs: pemsOf(leaf)}
		before, rows := r.counts(), r.rows()
		_, why := r.resp.authorizeRequest(req, false)
		require.Equal(t, genericAuthFailure, why)
		r.moved(before, x509ResultUnsealed)
		require.Equal(t, rows, r.rows(), "a forged certificate request registered a device")
		_, why = r.resp.authorizeRequest(req, true)
		require.Empty(t, why)
	})
}

// 🔑 THE NARROWEST ANCHOR WINS. A profile anchored at an intermediate CA and another at its root
// both match a device that intermediate issued; the intermediate's profile registers it. Two
// profiles at the same depth cannot be told apart and are refused.
func TestX509JitNarrowestAnchorWins(t *testing.T) {
	setup := func(t *testing.T) (r *jitRig, root, inter *testCert) {
		r = newJitRig(t, jitRigOpts{noProfile: true})
		root = newTestCert(t, nil, certOpts{cn: "Nested Root", ca: true})
		inter = newTestCert(t, root, certOpts{cn: "Nested Line CA", ca: true})
		r.anchor("nest-root", root)
		r.anchor("nest-inter", inter)
		return
	}
	earmark := func(r *jitRig, cn string) uint {
		d, err := r.api.DevicesByToken(r.ctx, []string{model.DeriveX509DeviceToken(cn)})
		require.NoError(r.t, err)
		require.Len(r.t, d, 1)
		require.NotNil(r.t, d[0].ExpectedProvisioningProfileId)
		return *d[0].ExpectedProvisioningProfileId
	}
	profileID := func(r *jitRig, token string) uint {
		p, err := r.api.ProvisioningProfilesByToken(r.ctx, []string{token})
		require.NoError(r.t, err)
		require.Len(r.t, p, 1)
		return p[0].ID
	}

	t.Run("the intermediate's profile registers the device", func(t *testing.T) {
		r, _, inter := setup(t)
		r.addProfile("pp-root", "nest-root", model.ProvisionAllowNew, nil)
		r.addProfile("pp-inter", "nest-inter", model.ProvisionAllowNew, nil)
		leaf := newTestCert(t, inter, certOpts{cn: "SN-N1"})
		r.grants(x509ResultJitCreated, r.clientID("SN-N1"), leaf, inter)
		assert.Equal(t, profileID(r, "pp-inter"), earmark(r, "SN-N1"), "the root's profile registered a device the intermediate's should have")
	})
	t.Run("the order the profiles were made in does not matter", func(t *testing.T) {
		r, _, inter := setup(t)
		r.addProfile("pp-inter", "nest-inter", model.ProvisionAllowNew, nil)
		r.addProfile("pp-root", "nest-root", model.ProvisionAllowNew, nil)
		r.grants(x509ResultJitCreated, r.clientID("SN-N2"), newTestCert(t, inter, certOpts{cn: "SN-N2"}), inter)
		assert.Equal(t, profileID(r, "pp-inter"), earmark(r, "SN-N2"))
	})
	t.Run("a disabled narrow profile leaves the root's", func(t *testing.T) {
		r, _, inter := setup(t)
		r.addProfile("pp-root", "nest-root", model.ProvisionAllowNew, nil)
		r.addProfile("pp-inter", "nest-inter", model.ProvisionAllowNew, nil)
		_, err := r.api.UpdateProvisioningProfile(r.ctx, "pp-inter", &model.ProvisioningProfileUpdateRequest{Enabled: dcgraphql.OptionalBoolOf(false)})
		require.NoError(t, err)
		r.grants(x509ResultJitCreated, r.clientID("SN-N3"), newTestCert(t, inter, certOpts{cn: "SN-N3"}), inter)
		assert.Equal(t, profileID(r, "pp-root"), earmark(r, "SN-N3"))
	})
	t.Run("a device issued by the root alone is the root's", func(t *testing.T) {
		r, root, _ := setup(t)
		r.addProfile("pp-root", "nest-root", model.ProvisionAllowNew, nil)
		r.addProfile("pp-inter", "nest-inter", model.ProvisionAllowNew, nil)
		r.grants(x509ResultJitCreated, r.clientID("SN-N4"), newTestCert(t, root, certOpts{cn: "SN-N4"}))
		assert.Equal(t, profileID(r, "pp-root"), earmark(r, "SN-N4"))
	})
	t.Run("two profiles at the same depth are ambiguous", func(t *testing.T) {
		r, _, inter := setup(t)
		r.addProfile("pp-root", "nest-root", model.ProvisionAllowNew, nil)
		r.addProfile("pp-inter-a", "nest-inter", model.ProvisionAllowNew, nil)
		r.addProfile("pp-inter-b", "nest-inter", model.ProvisionAllowNew, nil)
		leaf := newTestCert(t, inter, certOpts{cn: "SN-N5"})
		r.refuses(x509ResultJitAmbiguous, r.clientID("SN-N5"), leaf, inter)
		_, err := r.api.UpdateProvisioningProfile(r.ctx, "pp-inter-b", &model.ProvisioningProfileUpdateRequest{Enabled: dcgraphql.OptionalBoolOf(false)})
		require.NoError(t, err)
		r.grants(x509ResultJitCreated, r.clientID("SN-N5"), leaf, inter)
		assert.Equal(t, profileID(r, "pp-inter-a"), earmark(r, "SN-N5"))
	})
}

// 🔑 G14'S PRECEDENCE THROUGH THE CALLOUT, each refusal counted as jit_<code> alone, answering
// the generic denial, writing nothing, and paired with a registration that succeeds beside it.
func TestX509JitEveryRegistrationRefusalIsCountedByItsCode(t *testing.T) {
	type tc struct {
		name   string
		result string
		opts   jitRigOpts
		// arrange sets up the world for the refused common name cn and returns the chain to present.
		arrange func(r *jitRig, cn string) []*testCert
		// control, when set, replaces the default paired success (a name nothing has touched
		// registering beside the refusal) for a case where none can: it corrects the one input
		// and the SAME connect must then succeed.
		control func(r *jitRig, cn string, chain []*testCert)
	}
	own := func(r *jitRig, cn string) *testCert { return r.ca.simDevice(r.t, cn, time.Hour) }
	earmarked := func(r *jitRig, cn string) {
		_, err := r.api.CreateDevice(r.ctx, &model.DeviceCreateRequest{ExternalId: &cn, DeviceTypeToken: "dt-a",
			ProvisioningProfileToken: &r.profile})
		require.NoError(t, err)
	}
	cases := []tc{
		{name: "device disabled", result: "jit_device_disabled",
			arrange: func(r *jitRig, cn string) []*testCert {
				leaf := own(r, cn)
				r.grants(x509ResultJitCreated, r.clientID(cn), leaf)
				_, err := r.api.UpdateDevice(r.ctx, model.DeriveX509DeviceToken(cn), &model.DeviceUpdateRequest{Enabled: dcgraphql.OptionalBoolOf(false)})
				require.NoError(r.t, err)
				// A certificate the device does not yet hold: the registration, not the pin, decides.
				return []*testCert{own(r, cn)}
			}},
		{name: "type mismatch", result: "jit_device_type_mismatch",
			arrange: func(r *jitRig, cn string) []*testCert {
				earmarked(r, cn)
				_, err := r.api.CreateDeviceType(r.ctx, &model.DeviceTypeCreateRequest{Token: "dt-other-" + cn})
				require.NoError(r.t, err)
				_, err = r.api.UpdateDevice(r.ctx, model.DeriveX509DeviceToken(cn), &model.DeviceUpdateRequest{
					DeviceTypeToken: dcgraphql.OptionalStringOf("dt-other-" + cn)})
				require.NoError(r.t, err)
				return []*testCert{own(r, cn)}
			}},
		{name: "not earmarked", result: "jit_device_not_earmarked",
			arrange: func(r *jitRig, cn string) []*testCert {
				_, err := r.api.CreateDevice(r.ctx, &model.DeviceCreateRequest{Token: "operator-" + cn, ExternalId: &cn, DeviceTypeToken: "dt-a"})
				require.NoError(r.t, err)
				return []*testCert{own(r, cn)}
			}},
		{name: "revoked", result: "jit_revoked",
			arrange: func(r *jitRig, cn string) []*testCert {
				r.grants(x509ResultJitCreated, r.clientID(cn), own(r, cn))
				n, err := r.api.RevokeProvisionedDevices(r.ctx, "pp-x")
				require.NoError(r.t, err)
				require.Positive(r.t, n)
				// The same name with a NEW certificate: revocation sticks.
				return []*testCert{own(r, cn)}
			}},
		{name: "a different certificate for a registered device", result: "jit_already_provisioned",
			arrange: func(r *jitRig, cn string) []*testCert {
				r.grants(x509ResultJitCreated, r.clientID(cn), own(r, cn))
				return []*testCert{own(r, cn)}
			}},
		{name: "at the cap", result: "jit_limit_exceeded", opts: jitRigOpts{maxDevices: func() *int32 { v := int32(1); return &v }()},
			arrange: func(r *jitRig, cn string) []*testCert {
				// The one place is taken.
				r.grants(x509ResultJitCreated, r.clientID("filler"), own(r, "filler"))
				return []*testCert{own(r, cn)}
			},
			control: func(r *jitRig, cn string, chain []*testCert) {
				_, err := r.api.UpdateProvisioningProfile(r.ctx, "pp-x", &model.ProvisioningProfileUpdateRequest{MaxDevices: dcgraphql.OptionalInt32Of(2)})
				require.NoError(r.t, err)
				r.grants(x509ResultJitCreated, r.clientID(cn), chain...)
			}},
		{name: "unknown device under CHECK_PRE_PROVISIONED", result: "jit_device_not_registered",
			opts:    jitRigOpts{strategy: model.ProvisionCheckPreProvisioned},
			arrange: func(r *jitRig, cn string) []*testCert { return []*testCert{own(r, cn)} },
			control: func(r *jitRig, cn string, chain []*testCert) {
				_, err := r.api.UpdateProvisioningProfile(r.ctx, "pp-x", &model.ProvisioningProfileUpdateRequest{
					Strategy: dcgraphql.OptionalStringOf(string(model.ProvisionAllowNew))})
				require.NoError(r.t, err)
				r.grants(x509ResultJitCreated, r.clientID(cn), chain...)
			}},
		{name: "an earmarked device with no credential (adopt)", result: "jit_provisioning_unavailable",
			arrange: func(r *jitRig, cn string) []*testCert {
				earmarked(r, cn)
				return []*testCert{own(r, cn)}
			}},
		{name: "a profile that mints another credential type", result: "jit_credential_type_unsupported",
			arrange: func(r *jitRig, cn string) []*testCert {
				r.setProfileCredentialType("ACCESS_TOKEN")
				return []*testCert{own(r, cn)}
			},
			control: func(r *jitRig, cn string, chain []*testCert) {
				r.setProfileCredentialType(string(model.CredentialX509Certificate))
				r.grants(x509ResultJitCreated, r.clientID(cn), chain...)
			}},
		{name: "the derived token held by another device", result: "jit_device_token_collision",
			arrange: func(r *jitRig, cn string) []*testCert {
				other := "someone-else-" + cn
				_, err := r.api.CreateDevice(r.ctx, &model.DeviceCreateRequest{Token: model.DeriveX509DeviceToken(cn), ExternalId: &other, DeviceTypeToken: "dt-a"})
				require.NoError(r.t, err)
				return []*testCert{own(r, cn)}
			}},
	}
	for i, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := newJitRig(t, c.opts)
			cn := fmt.Sprintf("SN-%02d", i)
			if c.control == nil {
				// The control comes first: a name nothing has touched registers under the same
				// profile, so the refusal below is the world's doing and not the rig's.
				r.grants(x509ResultJitCreated, r.clientID("control-"+cn), own(r, "control-"+cn))
			}
			chain := c.arrange(r, cn)
			r.refuses(c.result, r.clientID(cn), chain...)
			if c.control != nil {
				c.control(r, cn, chain)
			}
		})
	}
	t.Run("the vocabulary has a series for every code", func(t *testing.T) {
		r := newJitRig(t, jitRigOpts{})
		for _, code := range model.ProvisioningCodes() {
			assert.Contains(t, x509Results, jitResultForCode(code))
			assert.NotPanics(t, func() { r.count(jitResultForCode(code)) })
		}
	})
}

// A replay of a registered certificate that the pin lookup missed (two connects racing) is
// answered like a pinned one. Here the pin is hidden from the first lookup to force the path.
func TestX509JitReplayContinuesIntoThePinnedGrant(t *testing.T) {
	r := newJitRig(t, jitRigOpts{})
	leaf := r.ca.simDevice(t, "SN-R", time.Hour)
	r.grants(x509ResultJitCreated, r.clientID("SN-R"), leaf)

	chain, err := x509trust.ParseChainPEM(pemsOf(leaf))
	require.NoError(t, err)
	req := testRequestFromClient(t, r.user(), "", r.clientID("SN-R"))
	req.TLS = &jwt.ClientTLS{Certs: pemsOf(leaf)}
	before := r.counts()
	found, result := r.resp.unknownX509Pin(r.ctx, req, r.tenant, chain)
	require.NotNil(t, found, "a replay must continue into the pinned grant (refused as %s)", result)
	assert.Equal(t, x509ResultJitReplayed, result)
	assert.Equal(t, model.DeriveX509DeviceToken("SN-R"), found.Device.Token)
	// unknownX509Pin counts nothing itself: its caller counts what the granted connect was.
	for k, v := range r.counts() {
		assert.Equal(t, before[k], v, k)
	}
}

// 🔑 THE BUDGETS. Each refusal is paired with the connect that passes when the bucket admits, and
// writes nothing.
func TestX509JitBudgets(t *testing.T) {
	t.Run("the tenant's ceiling", func(t *testing.T) {
		r := newJitRig(t, jitRigOpts{})
		leaf := r.ca.simDevice(t, "SN-T", time.Hour)
		r.tenants.set(true)
		r.refuses(x509ResultJitRateLimited, r.clientID("SN-T"), leaf)
		assert.Equal(t, []string{r.tenant}, r.tenants.charged(), "charged for the authenticated tenant, once")
		r.tenants.set(false)
		r.grants(x509ResultJitCreated, r.clientID("SN-T"), leaf)
		// A pinned connect is never charged.
		n := len(r.tenants.charged())
		r.grants(x509ResultOK, r.clientID("SN-T"), leaf)
		assert.Len(t, r.tenants.charged(), n, "a pinned connect was charged to the registration ceiling")
	})
	t.Run("the per-source bucket is off by default", func(t *testing.T) {
		r := newJitRig(t, jitRigOpts{})
		r.sources.set(true) // would refuse everything, if it were consulted
		r.grants(x509ResultJitCreated, r.clientID("SN-S0"), r.ca.simDevice(t, "SN-S0", time.Hour))
		assert.Empty(t, r.sources.charged())
	})
	t.Run("the per-source bucket when it is on", func(t *testing.T) {
		r := newJitRig(t, jitRigOpts{sources: true})
		leaf := r.ca.simDevice(t, "SN-S1", time.Hour)
		r.sources.set(true)
		r.refuses(x509ResultJitRateLimited, r.clientID("SN-S1"), leaf)
		assert.Equal(t, []string{"10.1.2.3"}, r.sources.charged(), "keyed on the client address the broker saw")
		// A chain that verifies to nothing never reaches the bucket.
		r.sources.set(false)
		before := len(r.sources.charged())
		r.refuses(x509ResultChainInvalid, r.clientID("SN-S2"), simCA(t, "Stranger").simDevice(t, "SN-S2", time.Hour))
		assert.Len(t, r.sources.charged(), before, "an invalid chain was charged to its source")
		r.sources.set(true)
		assert.Empty(t, r.tenants.charged(), "the source bucket is checked before the tenant's is charged")
		r.sources.set(false)
		r.grants(x509ResultJitCreated, r.clientID("SN-S1"), leaf)
	})
	t.Run("the in-flight bound, and a pinned device never waits on it", func(t *testing.T) {
		r := newJitRig(t, jitRigOpts{maxInFlight: 1})
		// A pinned device, registered before the stack-up.
		pinned := r.ca.simDevice(t, "SN-PINNED", time.Hour)
		r.grants(x509ResultJitCreated, r.clientID("SN-PINNED"), pinned)

		entered, release := r.gate.holdAtTheDoor()
		defer release()
		first := r.ca.simDevice(t, "SN-FIRST", time.Hour)
		firstDone := make(chan error, 1)
		go func() {
			_, why := r.authorize(r.clientID("SN-FIRST"), first)
			if why != "" {
				firstDone <- fmt.Errorf("refused: %s", why)
				return
			}
			firstDone <- nil
		}()
		select {
		case <-entered:
		case <-time.After(15 * time.Second):
			t.Fatal("the first registration never reached the door")
		}
		assert.Equal(t, 1.0, testutil.ToFloat64(r.metrics.JitInFlight), "the gauge shows the registration in flight")

		// The slot is taken: the next first connect is busy, writes nothing, and costs the tenant nothing.
		charged := len(r.tenants.charged())
		r.refuses(x509ResultJitBusy, r.clientID("SN-SECOND"), r.ca.simDevice(t, "SN-SECOND", time.Hour))
		assert.Len(t, r.tenants.charged(), charged, "a busy refusal consumed the tenant's allowance")
		// The control: a pinned connect passes straight through while the slot is held.
		r.grants(x509ResultOK, r.clientID("SN-PINNED"), pinned)

		release()
		select {
		case err := <-firstDone:
			require.NoError(t, err, "the held registration must complete once released")
		case <-time.After(15 * time.Second):
			t.Fatal("the held registration never finished")
		}
		assert.Zero(t, testutil.ToFloat64(r.metrics.JitInFlight))
		// And the slot is free again.
		r.grants(x509ResultJitCreated, r.clientID("SN-SECOND"), r.ca.simDevice(t, "SN-SECOND", time.Hour))
	})
}

// Main-independent guard for the configuration: registration needs all of its parts.
func TestWithCertificateRegistrationRefusesAnIncompleteConfig(t *testing.T) {
	r := newJitRig(t, jitRigOpts{})
	ok := JitConfig{Registrar: r.gate, Tenants: r.tenants, MaxInFlight: 1}
	for name, mutate := range map[string]func(*JitConfig){
		"no registrar": func(c *JitConfig) { c.Registrar = nil },
		"no tenants":   func(c *JitConfig) { c.Tenants = nil },
		"no slots":     func(c *JitConfig) { c.MaxInFlight = 0 },
	} {
		c := ok
		mutate(&c)
		_, err := r.resp.WithCertificateRegistration(c)
		assert.Error(t, err, name)
	}
	_, err := r.resp.WithCertificateRegistration(ok)
	assert.NoError(t, err)
	enabled, slots, perSource := r.resp.JitEnabled()
	assert.True(t, enabled)
	assert.Equal(t, 1, slots)
	assert.False(t, perSource)
}
