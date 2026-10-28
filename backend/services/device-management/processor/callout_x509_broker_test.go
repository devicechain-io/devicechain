// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/devicechain-io/dc-device-management/model"
	dmtest "github.com/devicechain-io/dc-device-management/test"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/credential/credentialtest"
	dcgraphql "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-microservice/natsauth"
	"github.com/devicechain-io/dc-microservice/rdb"
	putest "github.com/devicechain-io/dc-microservice/rdb/partialupdatetest"
	dctest "github.com/devicechain-io/dc-microservice/test"
	"github.com/devicechain-io/dc-microservice/x509trust"
	mqtt "github.com/eclipse/paho.mqtt.golang"
	"github.com/nats-io/jwt/v2"
	natsserver "github.com/nats-io/nats-server/v2/server"
	nats "github.com/nats-io/nats.go"
	"github.com/nats-io/nkeys"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/bcrypt"
)

// X.509 CONNECTS, END TO END: a REAL TLS client presenting REAL certificates to an embedded
// broker that requests client certificates exactly as the DeviceChain broker image does
// (tls.RequestClientCert on the MQTT listener), the REAL callout responder, and the REAL
// device-management Api over a database. Nothing about the certificate path is faked: the pin
// lookup, the anchor, the chain verification, the clamp and the kick are the production ones.
//
// Every refusal is paired with the case that is accepted when its ONE input is corrected, and
// is asserted against its own counter: a refusal that happened for a different reason than the
// one under test fails the test, which is what keeps these from passing on a connect that
// failed for an unrelated cause.

// ---------------------------------------------------------------------------------------------
// A throwaway PKI.

type testCert struct {
	cert *x509.Certificate
	key  *ecdsa.PrivateKey
}

func (n *testCert) pem() string {
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: n.cert.Raw}))
}

type certOpts struct {
	cn        string
	ca        bool
	notAfter  time.Time
	notBefore time.Time
	server    bool
}

var x509TestSerial atomic.Int64

func newTestCert(t testing.TB, parent *testCert, o certOpts) *testCert {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	now := time.Now()
	if o.notBefore.IsZero() {
		o.notBefore = now.Add(-time.Hour)
	}
	if o.notAfter.IsZero() {
		o.notAfter = now.Add(48 * time.Hour)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(x509TestSerial.Add(1) + now.UnixNano()),
		Subject:      pkix.Name{CommonName: o.cn},
		NotBefore:    o.notBefore,
		NotAfter:     o.notAfter,
	}
	switch {
	case o.ca:
		tmpl.IsCA, tmpl.BasicConstraintsValid, tmpl.MaxPathLen = true, true, -1
		tmpl.KeyUsage = x509.KeyUsageCertSign | x509.KeyUsageCRLSign
	case o.server:
		tmpl.KeyUsage = x509.KeyUsageDigitalSignature
		tmpl.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}
		tmpl.IPAddresses = []net.IP{net.ParseIP("127.0.0.1")}
	default:
		tmpl.KeyUsage = x509.KeyUsageDigitalSignature
		tmpl.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}
	}
	issuer, signer := tmpl, key
	if parent != nil {
		issuer, signer = parent.cert, parent.key
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, issuer, &key.PublicKey, signer)
	require.NoError(t, err)
	cert, err := x509.ParseCertificate(der)
	require.NoError(t, err)
	return &testCert{cert: cert, key: key}
}

// presenting is a TLS client certificate: the leaf, then the intermediates the device sends.
func presenting(leaf *testCert, intermediates ...*testCert) *tls.Certificate {
	c := &tls.Certificate{Certificate: [][]byte{leaf.cert.Raw}, PrivateKey: leaf.key}
	for _, i := range intermediates {
		c.Certificate = append(c.Certificate, i.cert.Raw)
	}
	return c
}

// pemsOf is a chain as the broker hands it to the callout: PEM, leaf first.
func pemsOf(nodes ...*testCert) []string {
	out := make([]string, 0, len(nodes))
	for _, n := range nodes {
		out = append(out, n.pem())
	}
	return out
}

// ---------------------------------------------------------------------------------------------
// The broker: MQTT over TLS requesting (not requiring) client certificates, the callout, and the
// system-account login the kick uses.

type x509Fixture struct {
	t        *testing.T
	srv      *natsserver.Server
	port     int
	api      *model.Api
	ctx      context.Context
	resp     *CalloutResponder
	metrics  *CalloutMetrics
	serverCA *testCert
	creds    natsauth.Credentials
	tenant   string
}

// fixtureOpts vary the fixture without forking it. The zero value is the SQLite fixture of the
// pinned-path tests.
type fixtureOpts struct {
	// tenant is the tenant the fixture creates its device type in; x509Tenant when empty.
	tenant string
	// newApi builds the Api over the database to test against; SQLite when nil. The server is
	// given so a kicker can reach it.
	newApi func(t *testing.T, srv *natsserver.Server) *model.Api
	// configure runs on the responder BEFORE it starts, so a test can turn registration on
	// without racing a request.
	configure func(t *testing.T, resp *CalloutResponder, api *model.Api)
}

const x509Tenant = "acme-corp"

// brokerKicker ends a device's MQTT sessions the way event-sources does: CONNZ on the server,
// then KICK by connection id, over the system login.
type brokerKicker struct {
	srv *natsserver.Server
	t   *testing.T
}

func (k brokerKicker) KickDeviceSessions(_ context.Context, tenant, _ string, tokens []string) error {
	sys, err := nats.Connect(k.srv.ClientURL(), nats.UserInfo(natsauth.SysUser, "sys"))
	if err != nil {
		return err
	}
	defer sys.Close()
	msg, err := sys.Request(fmt.Sprintf("$SYS.REQ.SERVER.%s.CONNZ", k.srv.ID()), []byte(`{"limit":1024}`), 5*time.Second)
	if err != nil {
		return err
	}
	var reply struct {
		Data struct {
			Connections []struct {
				Cid        uint64 `json:"cid"`
				MQTTClient string `json:"mqtt_client"`
			} `json:"connections"`
		} `json:"data"`
	}
	if err := json.Unmarshal(msg.Data, &reply); err != nil {
		return err
	}
	for _, c := range reply.Data.Connections {
		for _, tok := range tokens {
			id := "inst-1:" + tenant + ":" + tok
			if c.MQTTClient != id && !strings.HasPrefix(c.MQTTClient, id+":") {
				continue
			}
			if _, err := sys.Request(fmt.Sprintf("$SYS.REQ.SERVER.%s.KICK", k.srv.ID()),
				[]byte(fmt.Sprintf(`{"cid":%d}`, c.Cid)), 5*time.Second); err != nil {
				return err
			}
		}
	}
	return nil
}

func newX509Fixture(t *testing.T) *x509Fixture { return newX509FixtureWith(t, fixtureOpts{}) }

func newX509FixtureWith(t *testing.T, o fixtureOpts) *x509Fixture {
	t.Helper()
	tenant := o.tenant
	if tenant == "" {
		tenant = x509Tenant
	}
	creds, err := natsauth.GenerateCredentials()
	require.NoError(t, err)

	// The broker's own server certificate, and the CA a client trusts it by.
	serverCA := newTestCert(t, nil, certOpts{cn: "Broker CA", ca: true})
	serverLeaf := newTestCert(t, serverCA, certOpts{cn: "broker", server: true})
	dir := dctest.JetStreamStoreDir(t)
	certFile, keyFile := filepath.Join(dir, "server.crt"), filepath.Join(dir, "server.key")
	require.NoError(t, os.WriteFile(certFile, []byte(serverLeaf.pem()), 0o600))
	keyDER, err := x509.MarshalECPrivateKey(serverLeaf.key)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0o600))

	serviceHash, err := bcrypt.GenerateFromPassword([]byte(creds.ServicePassword), bcrypt.MinCost)
	require.NoError(t, err)
	q := func(subjects []string) string {
		out := make([]string, 0, len(subjects))
		for _, s := range subjects {
			out = append(out, strconv.Quote(s))
		}
		return strings.Join(out, ", ")
	}
	conf := filepath.Join(dir, "nats.conf")
	body := fmt.Sprintf(`
		port: -1
		server_name: "callout-x509"
		jetstream { store_dir: %q }
		mqtt { host: "127.0.0.1", port: -1, tls { cert_file: %q, key_file: %q } }
		system_account: SYS
		accounts {
		  APP { jetstream: enabled, users: [{user: %q, password: %q}] }
		  SYS { users: [{user: %q, password: "sys", permissions: { publish: { allow: [%s] }, subscribe: { allow: [%s] } }}] }
		}
		authorization {
		  timeout: 5
		  auth_callout { issuer: %q, auth_users: [%q, %q], account: "APP" }
		}
	`, filepath.Join(dir, "js"), certFile, keyFile, natsauth.ServiceUser, string(serviceHash),
		natsauth.SysUser, q(natsauth.SysPublishAllow()), q(natsauth.SysSubscribeAllow()),
		creds.IssuerPublic, natsauth.ServiceUser, natsauth.SysUser)
	require.NoError(t, os.WriteFile(conf, []byte(body), 0o600))
	opts, err := natsserver.ProcessConfigFile(conf)
	require.NoError(t, err)
	opts.NoLog, opts.NoSigs = true, true
	// What the DeviceChain broker image does after it reads its configuration: ask a client for a
	// certificate without requiring one. Without it the broker hands the callout no certificate
	// at all, and the control below (a token client with no certificate) would pass for the
	// wrong reason.
	require.NotNil(t, opts.MQTT.TLSConfig, "the MQTT listener must be configured for TLS")
	opts.MQTT.TLSConfig.ClientAuth = tls.RequestClientCert
	srv, err := natsserver.NewServer(opts)
	require.NoError(t, err)
	go srv.Start()
	require.True(t, srv.ReadyForConnections(20*time.Second), "embedded nats server not ready")
	t.Cleanup(srv.Shutdown)

	nc, err := nats.Connect(srv.ClientURL(), nats.UserInfo(natsauth.ServiceUser, creds.ServicePassword))
	require.NoError(t, err)
	t.Cleanup(nc.Close)

	var api *model.Api
	if o.newApi != nil {
		api = o.newApi(t, srv)
	} else {
		tables := []any{&model.Device{}, &model.DeviceType{}, &model.DeviceProfile{}, &model.DeviceProfileVersion{},
			&model.DeviceProfileActivation{}, &model.MetricDefinition{}, &model.CommandDefinition{}, &model.DetectionRule{},
			&model.DetectionRuleScopeRef{}, &model.ProvisioningProfile{}, &model.DeviceCredential{},
			&model.PendingSessionKick{}, &model.DeviceTrustAnchor{}}
		api = model.NewApi(&rdb.RdbManager{Database: putest.NewSQLiteDB(t, tables...)})
	}
	api.DeviceSecretKey = dmtest.DeviceSecretKey()
	api.SessionKicker = brokerKicker{srv: srv, t: t}
	ctx := putest.TenantContext(tenant)()
	_, err = api.CreateDeviceType(ctx, &model.DeviceTypeCreateRequest{Token: "dt-a"})
	require.NoError(t, err)

	ms := &core.Microservice{InstanceId: "inst-1", FunctionalArea: "device-management"}
	ms.UseMetricsRegistry(prometheus.NewRegistry())
	metrics := NewCalloutMetrics(ms)
	resp := mustResponder(t, nc, api, testChecker(t, credentialtest.NewStore()), creds.IssuerSeed, nil)
	resp.Configure(0, 0, 0, metrics)
	if o.configure != nil {
		o.configure(t, resp, api)
	}
	require.NoError(t, resp.Start())
	t.Cleanup(func() { _ = resp.Stop() })

	return &x509Fixture{t: t, srv: srv, port: dctest.MQTTPort(t, srv), api: api, ctx: ctx,
		resp: resp, metrics: metrics, serverCA: serverCA, creds: creds, tenant: tenant}
}

// device creates a device in the tenant.
func (f *x509Fixture) device(token string) {
	f.t.Helper()
	_, err := f.api.CreateDevice(f.ctx, &model.DeviceCreateRequest{Token: token, DeviceTypeToken: "dt-a"})
	require.NoError(f.t, err)
}

// anchor stores a CA as a trust anchor.
func (f *x509Fixture) anchor(token string, ca *testCert) {
	f.t.Helper()
	_, err := f.api.CreateDeviceTrustAnchor(f.ctx, &model.DeviceTrustAnchorCreateRequest{Token: token, CertificatePem: ca.pem()})
	require.NoError(f.t, err)
}

// pin pins leaf (with its intermediates) to a device against an anchor.
func (f *x509Fixture) pin(credToken, device, anchor string, chain ...*testCert) *model.DeviceCredential {
	f.t.Helper()
	text := strings.Join(pemsOf(chain...), "")
	created, err := f.api.CreateDeviceCredential(f.ctx, &model.DeviceCredentialCreateRequest{
		Token: credToken, DeviceToken: device, CredentialType: string(model.CredentialX509Certificate),
		CertificatePem: &text, TrustAnchorToken: &anchor, Enabled: true})
	require.NoError(f.t, err)
	return created
}

func (f *x509Fixture) count(result string) float64 {
	return testutil.ToFloat64(f.metrics.X509.WithLabelValues(result))
}

// counts snapshots every result so a case can assert exactly what moved.
func (f *x509Fixture) counts() map[string]float64 {
	out := map[string]float64{}
	for _, r := range x509Results {
		out[r] = f.count(r)
	}
	return out
}

// moved asserts that, since before, exactly the named result moved and it moved by one.
func (f *x509Fixture) moved(before map[string]float64, result string) {
	f.t.Helper()
	for _, r := range x509Results {
		want := before[r]
		if r == result {
			want++
		}
		assert.Equal(f.t, want, f.count(r), "callout_x509_total{result=%q}", r)
	}
}

// connect dials the broker over TLS and returns the CONNECT error, nil on a granted session.
// certs is what the client presents (none when empty). It does not reconnect on its own.
func (f *x509Fixture) connect(clientID, username, password string, certs ...tls.Certificate) (mqtt.Client, error) {
	f.t.Helper()
	pool := x509.NewCertPool()
	pool.AddCert(f.serverCA.cert)
	opts := mqtt.NewClientOptions().AddBroker(fmt.Sprintf("ssl://127.0.0.1:%d", f.port)).
		SetClientID(clientID).SetUsername(username).SetPassword(password).
		SetTLSConfig(&tls.Config{RootCAs: pool, Certificates: certs, MinVersion: tls.VersionTLS12}).
		SetAutoReconnect(false).SetConnectRetry(false).SetConnectTimeout(10 * time.Second)
	c := mqtt.NewClient(opts)
	tok := c.Connect()
	if !tok.WaitTimeout(15 * time.Second) {
		return nil, errors.New("connect timed out")
	}
	if err := tok.Error(); err != nil {
		return nil, err
	}
	f.t.Cleanup(func() { c.Disconnect(50) })
	return c, nil
}

func clientIDOf(device string) string { return "inst-1:" + x509Tenant + ":" + device }

func certUser() string { return x509Tenant + ":" + X509Username }

// ---------------------------------------------------------------------------------------------

// 🔑 THE PINNED PATH, REFUSAL BY REFUSAL. One root, one intermediate, one registered device; each
// case changes ONE thing from the connect that succeeds first, and must move ITS counter alone.
func TestX509PinnedConnectAndEveryRefusal(t *testing.T) {
	f := newX509Fixture(t)
	root := newTestCert(t, nil, certOpts{cn: "Maker Root", ca: true})
	inter := newTestCert(t, root, certOpts{cn: "Line CA", ca: true})
	f.anchor("maker", root)
	f.device("sensor-001")
	f.device("sensor-002")
	leaf := newTestCert(t, inter, certOpts{cn: "SN-0001"})
	f.pin("c-1", "sensor-001", "maker", leaf, inter)

	t.Run("a pinned device connects, and only ok moves", func(t *testing.T) {
		before := f.counts()
		c, err := f.connect(clientIDOf("sensor-001"), certUser(), "", *presenting(leaf, inter))
		require.NoError(t, err, "the control: the registered certificate with its intermediate must connect")
		require.True(t, c.IsConnectionOpen())
		f.moved(before, x509ResultOK)
	})

	t.Run("no certificate", func(t *testing.T) {
		before := f.counts()
		_, err := f.connect(clientIDOf("sensor-001"), certUser(), "")
		require.Error(t, err, "@x509 with no client certificate connected")
		f.moved(before, x509ResultNoCert)
	})

	t.Run("a password with @x509", func(t *testing.T) {
		before := f.counts()
		_, err := f.connect(clientIDOf("sensor-001"), certUser(), "hunter2hunter2", *presenting(leaf, inter))
		require.Error(t, err, "@x509 with a password connected")
		f.moved(before, x509ResultPasswordPresented)
	})

	t.Run("a chain that does not build to the anchor", func(t *testing.T) {
		before := f.counts()
		// The pinned leaf, WITHOUT the intermediate that links it to the anchor.
		_, err := f.connect(clientIDOf("sensor-001"), certUser(), "", *presenting(leaf))
		require.Error(t, err, "a leaf missing its intermediate connected")
		f.moved(before, x509ResultChainInvalid)
	})

	t.Run("a certificate nothing pins", func(t *testing.T) {
		before := f.counts()
		stranger := newTestCert(t, inter, certOpts{cn: "SN-9999"})
		_, err := f.connect(clientIDOf("sensor-001"), certUser(), "", *presenting(stranger, inter))
		require.Error(t, err, "a valid certificate from the same CA but not pinned connected")
		f.moved(before, x509ResultUnknownPin)
	})

	t.Run("another device's client id", func(t *testing.T) {
		before := f.counts()
		_, err := f.connect(clientIDOf("sensor-002"), certUser(), "", *presenting(leaf, inter))
		require.Error(t, err, "a certificate connected under another device's client id")
		f.moved(before, x509ResultClientIDMismatch)
		// The control: its own id, with a discriminator, which the pin allows.
		_, err = f.connect(clientIDOf("sensor-001")+":b", certUser(), "", *presenting(leaf, inter))
		require.NoError(t, err)
	})

	t.Run("the wrong tenant in the username", func(t *testing.T) {
		before := f.counts()
		// Tenant comes from the username, never from the certificate: another tenant has no such pin.
		_, err := f.connect("inst-1:other-corp:sensor-001", "other-corp:"+X509Username, "", *presenting(leaf, inter))
		require.Error(t, err)
		f.moved(before, x509ResultUnknownPin)
	})

	t.Run("a disabled device", func(t *testing.T) {
		f.device("sensor-003")
		l3 := newTestCert(t, inter, certOpts{cn: "SN-0003"})
		f.pin("c-3", "sensor-003", "maker", l3, inter)
		_, err := f.connect(clientIDOf("sensor-003"), certUser(), "", *presenting(l3, inter))
		require.NoError(t, err, "the control: enabled, it connects")
		_, err = f.api.UpdateDevice(f.ctx, "sensor-003", &model.DeviceUpdateRequest{Enabled: dcgraphql.OptionalBoolOf(false)})
		require.NoError(t, err)
		before := f.counts()
		_, err = f.connect(clientIDOf("sensor-003"), certUser(), "", *presenting(l3, inter))
		require.Error(t, err, "a disabled device connected by certificate")
		f.moved(before, x509ResultRevoked)
	})
}

// 🔑 A REVOCATION IS NEVER ANSWERED FROM CACHE: the credential is disabled and the very next
// connect, with no wait, is refused; re-enabled, the next connect after that succeeds. A cached
// answer would serve the stale verdict in both directions.
func TestX509RevokedCredentialIsRefusedImmediately(t *testing.T) {
	f := newX509Fixture(t)
	root := newTestCert(t, nil, certOpts{cn: "Maker Root", ca: true})
	f.anchor("maker", root)
	f.device("sensor-001")
	leaf := newTestCert(t, root, certOpts{cn: "SN-0001"})
	f.pin("c-1", "sensor-001", "maker", leaf)

	_, err := f.connect(clientIDOf("sensor-001"), certUser(), "", *presenting(leaf))
	require.NoError(t, err, "the control: before the revocation it connects")

	_, err = f.api.UpdateDeviceCredential(f.ctx, "c-1", &model.DeviceCredentialUpdateRequest{Enabled: dcgraphql.OptionalBoolOf(false)})
	require.NoError(t, err)
	before := f.counts()
	_, err = f.connect(clientIDOf("sensor-001"), certUser(), "", *presenting(leaf))
	require.Error(t, err, "a revoked certificate connected on the very next attempt")
	f.moved(before, x509ResultRevoked)

	_, err = f.api.UpdateDeviceCredential(f.ctx, "c-1", &model.DeviceCredentialUpdateRequest{Enabled: dcgraphql.OptionalBoolOf(true)})
	require.NoError(t, err)
	_, err = f.connect(clientIDOf("sensor-001"), certUser(), "", *presenting(leaf))
	require.NoError(t, err, "re-enabled, the next connect must succeed: the verdict is read, not remembered")
}

// A deleted credential is a pin nothing holds: refused and counted as unknown (the hook where
// registration on first connect attaches), and the control is the credential before the delete.
func TestX509DeletedCredentialIsAnUnknownPin(t *testing.T) {
	f := newX509Fixture(t)
	root := newTestCert(t, nil, certOpts{cn: "Maker Root", ca: true})
	f.anchor("maker", root)
	f.device("sensor-001")
	leaf := newTestCert(t, root, certOpts{cn: "SN-0001"})
	f.pin("c-1", "sensor-001", "maker", leaf)
	_, err := f.connect(clientIDOf("sensor-001"), certUser(), "", *presenting(leaf))
	require.NoError(t, err)

	require.NoError(t, f.api.RDB.DB(f.ctx).Where("token = ?", "c-1").Delete(&model.DeviceCredential{}).Error)
	before := f.counts()
	_, err = f.connect(clientIDOf("sensor-001"), certUser(), "", *presenting(leaf))
	require.Error(t, err)
	f.moved(before, x509ResultUnknownPin)
}

// 🔑 AN ANCHOR DISABLED WHILE THE SESSION IS LIVE: the disable kicks the device (the pending-kick
// path), the device's own reconnects are refused while the CA is withdrawn, and the same device
// comes straight back when the anchor is enabled again. A second device on another anchor is
// untouched throughout.
func TestX509AnchorDisabledEndsTheLiveSession(t *testing.T) {
	f := newX509Fixture(t)
	rootA := newTestCert(t, nil, certOpts{cn: "Maker A", ca: true})
	rootB := newTestCert(t, nil, certOpts{cn: "Maker B", ca: true})
	f.anchor("maker-a", rootA)
	f.anchor("maker-b", rootB)
	f.device("dev-a")
	f.device("dev-b")
	leafA := newTestCert(t, rootA, certOpts{cn: "SN-A"})
	leafB := newTestCert(t, rootB, certOpts{cn: "SN-B"})
	f.pin("c-a", "dev-a", "maker-a", leafA)
	f.pin("c-b", "dev-b", "maker-b", leafB)

	// Both connect and STAY connected: auto-reconnect, as firmware does.
	pool := x509.NewCertPool()
	pool.AddCert(f.serverCA.cert)
	live := func(device string, leaf *testCert) mqtt.Client {
		opts := mqtt.NewClientOptions().AddBroker(fmt.Sprintf("ssl://127.0.0.1:%d", f.port)).
			SetClientID(clientIDOf(device)).SetUsername(certUser()).
			SetTLSConfig(&tls.Config{RootCAs: pool, Certificates: []tls.Certificate{*presenting(leaf)}, MinVersion: tls.VersionTLS12}).
			SetAutoReconnect(true).SetConnectRetry(false).SetMaxReconnectInterval(200 * time.Millisecond).
			SetConnectTimeout(5 * time.Second)
		c := mqtt.NewClient(opts)
		tok := c.Connect()
		require.True(t, tok.WaitTimeout(15*time.Second))
		require.NoError(t, tok.Error())
		t.Cleanup(func() { c.Disconnect(50) })
		return c
	}
	devA, devB := live("dev-a", leafA), live("dev-b", leafB)
	require.True(t, devA.IsConnectionOpen())

	// The control: nothing but the disable ends it. A second passes, the session is untouched.
	time.Sleep(time.Second)
	require.True(t, devA.IsConnectionOpen())

	_, err := f.api.UpdateDeviceTrustAnchor(f.ctx, "maker-a", &model.DeviceTrustAnchorUpdateRequest{Enabled: dcgraphql.OptionalBoolOf(false)})
	require.NoError(t, err, "the disable and its kick")
	require.Eventually(t, func() bool { return !devA.IsConnectionOpen() }, 10*time.Second, 25*time.Millisecond,
		"the device on the withdrawn CA kept its session")
	require.Never(t, devA.IsConnectionOpen, 2*time.Second, 50*time.Millisecond,
		"the device reconnected while its CA was withdrawn")
	require.Greater(t, f.count(x509ResultAnchorDisabled), float64(0), "its reconnects must be refused as anchor_disabled")
	require.True(t, devB.IsConnectionOpen(), "a device on another anchor must be untouched")

	_, err = f.api.UpdateDeviceTrustAnchor(f.ctx, "maker-a", &model.DeviceTrustAnchorUpdateRequest{Enabled: dcgraphql.OptionalBoolOf(true)})
	require.NoError(t, err)
	require.Eventually(t, devA.IsConnectionOpen, 10*time.Second, 25*time.Millisecond,
		"the control: with the CA trusted again the device must come back")
}

// Token and password clients are unaffected by the broker asking for certificates: one with no
// certificate connects as before, and one that happens to hold a certificate and sends it with a
// token username is admitted on the TOKEN, with the certificate ignored (and counted).
func TestX509TokenClientsAreUnaffected(t *testing.T) {
	f := newX509Fixture(t)
	root := newTestCert(t, nil, certOpts{cn: "Maker Root", ca: true})
	f.anchor("maker", root)
	f.device("sensor-001")
	f.device("sensor-002")
	stray := newTestCert(t, root, certOpts{cn: "SN-STRAY"})
	_, err := f.api.CreateDeviceCredential(f.ctx, &model.DeviceCredentialCreateRequest{
		Token: "tok-1", DeviceToken: "sensor-001", CredentialType: string(model.CredentialAccessToken),
		CredentialId: "tok-abc-0123456789", Enabled: true})
	require.NoError(t, err)

	before := f.counts()
	_, err = f.connect(clientIDOf("sensor-001"), x509Tenant+":tok-abc-0123456789", "")
	require.NoError(t, err, "a token client with no certificate must connect exactly as before")
	for r, v := range before {
		assert.Equal(t, v, f.count(r), "a token connect moved callout_x509_total{result=%q}", r)
	}

	_, err = f.connect(clientIDOf("sensor-001")+":b", x509Tenant+":tok-abc-0123456789", "", *presenting(stray))
	require.NoError(t, err, "a token client that also holds a certificate must still connect on its token")
	f.moved(before, x509ResultCertIgnored)

	// And the certificate gives a token username nothing: an unknown token with a registered
	// certificate is refused.
	pinned := newTestCert(t, root, certOpts{cn: "SN-PINNED"})
	f.pin("c-2", "sensor-002", "maker", pinned)
	_, err = f.connect(clientIDOf("sensor-002"), x509Tenant+":not-a-token-0123456", "", *presenting(pinned))
	require.Error(t, err, "a registered certificate authenticated a token username")
}

// "@x509" is the certificate path and nothing else: an ACCESS_TOKEN whose value is literally
// "@x509" does not make a back door around the certificate.
func TestX509UsernameCannotBeUsedAsAToken(t *testing.T) {
	f := newX509Fixture(t)
	f.device("sensor-001")
	_, err := f.api.CreateDeviceCredential(f.ctx, &model.DeviceCredentialCreateRequest{
		Token: "tok-at", DeviceToken: "sensor-001", CredentialType: string(model.CredentialAccessToken),
		CredentialId: X509Username, Enabled: true})
	if err != nil {
		t.Skipf("the token grammar refuses %q, so there is nothing to bypass: %v", X509Username, err)
	}
	before := f.counts()
	_, err = f.connect(clientIDOf("sensor-001"), certUser(), "")
	require.Error(t, err)
	f.moved(before, x509ResultNoCert)
}

// ---------------------------------------------------------------------------------------------
// The grant, decided without the wire: the clamp and the gates that need the request in hand.

// x509Authorize runs the responder's decision for a certificate connect whose broker-supplied
// chain is chain.
func (f *x509Fixture) x509Authorize(device string, chain ...*testCert) (string, string) {
	f.t.Helper()
	req := testRequestFromClient(f.t, certUser(), "", clientIDOf(device))
	req.TLS = &jwt.ClientTLS{Certs: pemsOf(chain...)}
	return f.resp.authorize(req)
}

func expiryOf(t *testing.T, userJWT string) int64 {
	t.Helper()
	uc, err := jwt.DecodeUserClaims(userJWT)
	require.NoError(t, err)
	return uc.Expires
}

// 🔑 THE CLAMP: the session ends at the earliest notAfter over the chain, whichever certificate
// holds it. Each case is the one where a DIFFERENT value than the clamp's would be the answer.
func TestX509SessionIsClampedToTheChainsEarliestNotAfter(t *testing.T) {
	f := newX509Fixture(t)
	ttl := f.resp.ttl
	now := time.Now()
	root := newTestCert(t, nil, certOpts{cn: "Maker Root", ca: true, notAfter: now.Add(72 * time.Hour)})
	f.anchor("maker", root)

	t.Run("a chain good for longer than the session lifetime gets the full lifetime", func(t *testing.T) {
		f.device("clamp-0")
		leaf := newTestCert(t, root, certOpts{cn: "SN-LONG", notAfter: now.Add(48 * time.Hour)})
		f.pin("c-long", "clamp-0", "maker", leaf)
		userJWT, denied := f.x509Authorize("clamp-0", leaf)
		require.Empty(t, denied)
		assert.InDelta(t, time.Now().Add(ttl).Unix(), expiryOf(t, userJWT), 3, "no clamp applies, so the session lasts the configured lifetime")
	})

	t.Run("the leaf expires first", func(t *testing.T) {
		f.device("clamp-1")
		leaf := newTestCert(t, root, certOpts{cn: "SN-LEAF", notAfter: now.Add(3 * time.Hour)})
		f.pin("c-leaf", "clamp-1", "maker", leaf)
		userJWT, denied := f.x509Authorize("clamp-1", leaf)
		require.Empty(t, denied)
		assert.Equal(t, leaf.cert.NotAfter.Unix(), expiryOf(t, userJWT))
	})

	t.Run("an intermediate expires before the leaf", func(t *testing.T) {
		f.device("clamp-2")
		inter := newTestCert(t, root, certOpts{cn: "Line CA", ca: true, notAfter: now.Add(2 * time.Hour)})
		leaf := newTestCert(t, inter, certOpts{cn: "SN-INTER", notAfter: now.Add(40 * time.Hour)})
		f.pin("c-inter", "clamp-2", "maker", leaf, inter)
		userJWT, denied := f.x509Authorize("clamp-2", leaf, inter)
		require.Empty(t, denied)
		assert.Equal(t, inter.cert.NotAfter.Unix(), expiryOf(t, userJWT),
			"the session must end when the INTERMEDIATE does, not when the leaf does")
	})

	t.Run("the chain is shorter than a credential that was stored with a later expiry", func(t *testing.T) {
		f.device("clamp-3")
		leaf := newTestCert(t, root, certOpts{cn: "SN-STALE", notAfter: now.Add(90 * time.Minute)})
		cred := f.pin("c-stale", "clamp-3", "maker", leaf)
		// A stored expiry later than the chain's: the chain's own notAfter must still decide.
		require.NoError(t, f.api.RDB.DB(f.ctx).Model(&model.DeviceCredential{}).Where("id = ?", cred.ID).
			Update("expires_at", now.Add(30*time.Hour)).Error)
		userJWT, denied := f.x509Authorize("clamp-3", leaf)
		require.Empty(t, denied)
		assert.Equal(t, leaf.cert.NotAfter.Unix(), expiryOf(t, userJWT))
	})

	t.Run("the credential expires before the chain", func(t *testing.T) {
		f.device("clamp-4")
		leaf := newTestCert(t, root, certOpts{cn: "SN-CRED", notAfter: now.Add(40 * time.Hour)})
		cred := f.pin("c-cred", "clamp-4", "maker", leaf)
		soon := time.Now().Add(100 * time.Minute)
		require.NoError(t, f.api.RDB.DB(f.ctx).Model(&model.DeviceCredential{}).Where("id = ?", cred.ID).
			Update("expires_at", soon).Error)
		userJWT, denied := f.x509Authorize("clamp-4", leaf)
		require.Empty(t, denied)
		assert.InDelta(t, soon.Unix(), expiryOf(t, userJWT), 1)
	})

	t.Run("a credential past its expiry is refused, not minted", func(t *testing.T) {
		f.device("clamp-5")
		leaf := newTestCert(t, root, certOpts{cn: "SN-GONE", notAfter: now.Add(40 * time.Hour)})
		cred := f.pin("c-gone", "clamp-5", "maker", leaf)
		require.NoError(t, f.api.RDB.DB(f.ctx).Model(&model.DeviceCredential{}).Where("id = ?", cred.ID).
			Update("expires_at", time.Now().Add(-time.Minute)).Error)
		before := f.counts()
		_, denied := f.x509Authorize("clamp-5", leaf)
		require.NotEmpty(t, denied)
		f.moved(before, x509ResultChainInvalid)
	})
}

// A credential with less than a second left is refused, not minted: the JWT's expiry is whole
// seconds, so the broker would close the session the instant it opened. The control is the same
// credential with minutes left.
func TestX509CredentialAboutToExpireIsRefused(t *testing.T) {
	f := newX509Fixture(t)
	root := newTestCert(t, nil, certOpts{cn: "Maker Root", ca: true})
	f.anchor("maker", root)
	f.device("sensor-001")
	leaf := newTestCert(t, root, certOpts{cn: "SN-0001"})
	cred := f.pin("c-1", "sensor-001", "maker", leaf)
	setExpiry := func(at time.Time) {
		require.NoError(t, f.api.RDB.DB(f.ctx).Model(&model.DeviceCredential{}).Where("id = ?", cred.ID).
			Update("expires_at", at).Error)
	}
	setExpiry(time.Now().Add(5 * time.Minute))
	_, denied := f.x509Authorize("sensor-001", leaf)
	require.Empty(t, denied, "the control: minutes left, it is granted")

	// 700 ms left when the lookup runs (still valid, so the lookup resolves it) but under the
	// one-second floor the clamp enforces.
	setExpiry(time.Now().Add(700 * time.Millisecond))
	before := f.counts()
	_, denied = f.x509Authorize("sensor-001", leaf)
	require.NotEmpty(t, denied, "a credential with under a second left was minted a session")
	f.moved(before, x509ResultChainInvalid)
}

// The gates every device connect passes, on the certificate path: the deleted-tenant gate and the
// erasure fence each refuse a connect that is granted with the gate open.
func TestX509TenantGatesRefuse(t *testing.T) {
	for _, tc := range []struct {
		name string
		open func(r *CalloutResponder)
		shut func(r *CalloutResponder)
	}{
		{"deleted tenant",
			func(r *CalloutResponder) { r.tenantDeleted = func(string) bool { return false } },
			func(r *CalloutResponder) { r.tenantDeleted = func(string) bool { return true } }},
		{"erasure fence",
			func(r *CalloutResponder) { r.tenantFenced = noFence },
			func(r *CalloutResponder) {
				r.tenantFenced = func(context.Context, string) error { return errors.New("tenant is being erased") }
			}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newX509Fixture(t)
			root := newTestCert(t, nil, certOpts{cn: "Maker Root", ca: true})
			f.anchor("maker", root)
			f.device("sensor-001")
			leaf := newTestCert(t, root, certOpts{cn: "SN-0001"})
			f.pin("c-1", "sensor-001", "maker", leaf)

			tc.open(f.resp)
			_, denied := f.x509Authorize("sensor-001", leaf)
			require.Empty(t, denied, "the control: the gate open, it is granted")
			tc.shut(f.resp)
			before := f.counts()
			_, denied = f.x509Authorize("sensor-001", leaf)
			require.NotEmpty(t, denied)
			f.moved(before, x509ResultTenantRefused)
		})
	}
}

// What the broker's own checks cannot make a callout accept: more than five certificates, an
// oversized presentation, and text that is not a certificate each refuse before a lookup.
func TestX509PresentedChainBoundsRefuse(t *testing.T) {
	f := newX509Fixture(t)
	root := newTestCert(t, nil, certOpts{cn: "Maker Root", ca: true})
	f.anchor("maker", root)
	f.device("sensor-001")
	leaf := newTestCert(t, root, certOpts{cn: "SN-0001"})
	f.pin("c-1", "sensor-001", "maker", leaf)
	_, denied := f.x509Authorize("sensor-001", leaf)
	require.Empty(t, denied, "the control")

	req := func(certs []string) (string, string) {
		r := testRequestFromClient(t, certUser(), "", clientIDOf("sensor-001"))
		r.TLS = &jwt.ClientTLS{Certs: certs}
		return f.resp.authorize(r)
	}
	six := append(pemsOf(leaf), pemsOf(root, root, root, root, root)...)
	for name, certs := range map[string][]string{
		"six certificates":   six,
		"not a certificate":  {"-----BEGIN CERTIFICATE-----\nAAAA\n-----END CERTIFICATE-----\n"},
		"empty presentation": {""},
	} {
		before := f.counts()
		_, d := req(certs)
		require.NotEmpty(t, d, name)
		f.moved(before, x509ResultCertMalformed)
	}
	before := f.counts()
	_, d := req(nil)
	require.NotEmpty(t, d)
	f.moved(before, x509ResultNoCert)
}

// A pinned leaf that has since passed its notAfter is refused by the chain verifier the callout
// calls (x509trust.VerifyClientChain) and surfaces as chain_invalid. The verifier's own cases
// (name constraints, key sizes, CA leaves, weak signatures) are in core/x509trust; this pins
// that the callout asks it, and refuses on its answer.
func TestX509ChainVerifierRefusalsReachTheWire(t *testing.T) {
	f := newX509Fixture(t)
	root := newTestCert(t, nil, certOpts{cn: "Maker Root", ca: true})
	f.anchor("maker", root)
	f.device("sensor-001")
	good := newTestCert(t, root, certOpts{cn: "SN-GOOD"})
	f.pin("c-good", "sensor-001", "maker", good)
	_, denied := f.x509Authorize("sensor-001", good)
	require.Empty(t, denied, "the control")

	// A leaf past its notAfter is verified-expired. It cannot be pinned (the pin refuses it), so
	// the row is written directly: this is the state a leaf reaches after the clock passes it.
	expired := newTestCert(t, root, certOpts{cn: "SN-OLD", notBefore: time.Now().Add(-48 * time.Hour), notAfter: time.Now().Add(-time.Hour)})
	rows := &model.DeviceCredential{}
	require.NoError(t, f.api.RDB.DB(f.ctx).Where("token = ?", "c-good").Take(rows).Error)
	clone := *rows
	clone.ID, clone.Token = 0, "c-old"
	clone.CredentialId = x509trust.Fingerprint(expired.cert)
	clone.ExpiresAt.Valid = false
	require.NoError(t, f.api.RDB.DB(f.ctx).Create(&clone).Error)
	before := f.counts()
	_, denied = f.x509Authorize("sensor-001", expired)
	require.NotEmpty(t, denied, "an expired leaf was granted a session")
	f.moved(before, x509ResultChainInvalid)
}

// 🔑 A CERTIFICATE GRANT NEEDS NO SECRET FROM THE DEVICE, so a request that did not come the way
// the broker sends it is not trusted for one: with an xkey configured the broker always seals,
// and an UNSEALED @x509 request is refused. Driven through handle with a request signed by a
// server key and the real reply path. Controls: the same request sealed is granted, and a
// responder with no xkey (where the broker does not seal) still grants it unsealed.
func TestX509RequestsMustArriveSealedWhenAnXkeyIsConfigured(t *testing.T) {
	f := newX509Fixture(t)
	root := newTestCert(t, nil, certOpts{cn: "Maker Root", ca: true})
	f.anchor("maker", root)
	f.device("sensor-001")
	leaf := newTestCert(t, root, certOpts{cn: "SN-0001"})
	f.pin("c-1", "sensor-001", "maker", leaf)

	serverKp, err := nkeys.CreateServer()
	require.NoError(t, err)
	serverX, err := nkeys.CreateCurveKeys()
	require.NoError(t, err)
	serverXPub, err := serverX.PublicKey()
	require.NoError(t, err)

	nc := f.resp.conn
	// ask runs one request through the responder's handler and returns whether it was granted.
	ask := func(r *CalloutResponder, sealed bool) bool {
		req := testRequestFromClient(t, certUser(), "", clientIDOf("sensor-001"))
		req.TLS = &jwt.ClientTLS{Certs: pemsOf(leaf)}
		claims := jwt.NewAuthorizationRequestClaims(req.UserNkey)
		claims.AuthorizationRequest = req
		tok, err := claims.Encode(serverKp)
		require.NoError(t, err)
		data := []byte(tok)
		msg := &nats.Msg{Subject: AuthCalloutSubject, Reply: nc.NewRespInbox(), Header: nats.Header{}}
		if sealed {
			data, err = serverX.Seal(data, f.creds.XKeyPublic)
			require.NoError(t, err)
			msg.Header.Set(natsauth.AuthRequestXKeyHeader, serverXPub)
		}
		msg.Data = data
		sub, err := nc.SubscribeSync(msg.Reply)
		require.NoError(t, err)
		defer sub.Unsubscribe()
		require.NoError(t, nc.Flush())
		r.handle(msg)
		out, err := sub.NextMsg(5 * time.Second)
		require.NoError(t, err)
		body := out.Data
		if sealed {
			body, err = serverX.Open(body, f.creds.XKeyPublic)
			require.NoError(t, err)
		}
		resp, err := jwt.DecodeAuthorizationResponseClaims(string(body))
		require.NoError(t, err)
		return resp.Jwt != "" && resp.Error == ""
	}

	_, err = f.resp.WithRequestEncryption(f.creds.XKeySeed, f.creds.XKeyPublic)
	require.NoError(t, err)
	require.True(t, ask(f.resp, true), "the control: a sealed certificate request is granted")
	before := f.counts()
	require.False(t, ask(f.resp, false), "an unsealed certificate request was granted although an xkey is configured")
	f.moved(before, x509ResultUnsealed)

	// A responder with no xkey configured: the broker does not seal, and an unsealed request is the norm.
	f.resp.xkey = nil
	require.True(t, ask(f.resp, false), "the control: with no xkey configured an unsealed request is the norm")
}
