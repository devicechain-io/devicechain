// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/blob"
	mscfg "github.com/devicechain-io/dc-microservice/config"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/deadletter"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/ota"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/devicechain-io/dc-microservice/rdb/sqlitetest"
	"github.com/devicechain-io/dc-microservice/service"
	"github.com/devicechain-io/dc-microservice/streams"
	dctest "github.com/devicechain-io/dc-microservice/test"
	"github.com/devicechain-io/dc-simulator/updater"
	"github.com/devicechain-io/dc-update-management/config"
	"github.com/devicechain-io/dc-update-management/model"
	"github.com/devicechain-io/dc-update-management/processor"
	"github.com/devicechain-io/dc-update-management/schema"
	natsserver "github.com/nats-io/nats-server/v2/server"
	nats "github.com/nats-io/nats.go"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"
)

// These tests run the whole update flow over a real embedded broker, through the service own
// wiring: createNatsComponents builds the desired-state writer and the device-reports consumer,
// buildApi carries the configuration into the API, and registerDownloadHandler mounts the download
// route on the service own mux. What stands in for the device is the simulator fake updater
// (dc-simulator/updater), speaking the wire from its own structs; what stands in for the store is
// SQLite with the real migration chain and the tenant predicate registered.

func startEmbeddedNats(t *testing.T) (string, uint32) {
	t.Helper()
	srv, err := natsserver.NewServer(&natsserver.Options{
		Host: "127.0.0.1", Port: -1, JetStream: true, StoreDir: dctest.JetStreamStoreDir(t),
	})
	require.NoError(t, err)
	go srv.Start()
	require.True(t, srv.ReadyForConnections(10*time.Second), "embedded nats server not ready")
	t.Cleanup(srv.Shutdown)
	u, err := url.Parse(srv.ClientURL())
	require.NoError(t, err)
	port, err := strconv.Atoi(u.Port())
	require.NoError(t, err)
	return u.Hostname(), uint32(port)
}

// fixtureDevices maps tenant/device to a profile token.
type fixtureDevices map[string]string

func (f fixtureDevices) DeviceOf(_ context.Context, tenant, device string) (model.DeviceFacts, bool, error) {
	p, ok := f[tenant+"/"+device]
	return model.DeviceFacts{Profile: p}, ok, nil
}

type fixtureProfiles struct{}

func (fixtureProfiles) Missing(context.Context, string, []string) ([]string, error) { return nil, nil }

type e2e struct {
	t        *testing.T
	instance string
	nc       *nats.Conn
	srv      *httptest.Server
	clockMu  sync.Mutex
	clock    time.Time
	sent     atomic.Int64 // messages every device link has published on device-reports

	// keys are the tenants' registered signing keys. Every artifact the tests declare is signed
	// with its tenant's key, and a device trusts the keys in trusted, so the default flow is the
	// strict one: the instance refuses unverified artifacts and the device requires a signature.
	keys    map[string]*signingKey
	trusted []string
}

// signingKey is a signer's private key with the PEM of its public half.
type signingKey struct {
	pem    string
	sign   func(digest []byte) []byte
	signer *model.ArtifactSigner // set once registered
}

func newSigningKey(t *testing.T, ed bool) *signingKey {
	t.Helper()
	var pub any
	k := &signingKey{}
	if ed {
		p, priv, err := ed25519.GenerateKey(rand.Reader)
		require.NoError(t, err)
		pub, k.sign = p, func(d []byte) []byte { return ed25519.Sign(priv, d) }
	} else {
		priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		require.NoError(t, err)
		pub = &priv.PublicKey
		k.sign = func(d []byte) []byte {
			sig, err := ecdsa.SignASN1(rand.Reader, priv, d)
			require.NoError(t, err)
			return sig
		}
	}
	der, err := x509.MarshalPKIXPublicKey(pub)
	require.NoError(t, err)
	k.pem = string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}))
	return k
}

// register adds the key to the tenant's signer registry.
func (f *e2e) register(tenant string, k *signingKey) *signingKey {
	f.t.Helper()
	s, err := Api.RegisterSigner(ctxOf(tenant), "release key", k.pem)
	require.NoError(f.t, err)
	k.signer = s
	return k
}

func (f *e2e) now() time.Time {
	f.clockMu.Lock()
	defer f.clockMu.Unlock()
	return f.clock
}

// advance moves the fake clock. The platform stamps a report with its RECEIVE time, and the device
// publishes asynchronously, so a report the device has already sent can still be queued on the broker
// when a test observes the state its predecessor produced. Moving the clock under it would stamp the
// report in the future and refresh the deadline the test is about to cross; advance therefore waits
// for every report sent so far to be applied before it moves.
func (f *e2e) advance(d time.Duration) {
	f.t.Helper()
	f.settle()
	f.clockMu.Lock()
	defer f.clockMu.Unlock()
	f.clock = f.clock.Add(d)
}

// settle waits until the broker holds every message a device link has published on device-reports and
// every consumer of that stream has acknowledged all of them (none pending, none in flight).
func (f *e2e) settle() {
	f.t.Helper()
	js, err := f.nc.JetStream()
	require.NoError(f.t, err)
	name := messaging.StreamName(f.instance, streams.DeviceReports)
	require.Eventually(f.t, func() bool {
		info, err := js.StreamInfo(name)
		if err != nil || info.State.LastSeq < uint64(f.sent.Load()) {
			return false
		}
		for ci := range js.Consumers(name) {
			if ci.NumPending != 0 || ci.NumAckPending != 0 {
				return false
			}
		}
		return true
	}, 10*time.Second, time.Millisecond, "device reports were still in flight")
}

func startE2E(t *testing.T) *e2e {
	t.Helper()
	host, port := startEmbeddedNats(t)

	prevMs, prevCfg, prevNats, prevApi, prevSvc, prevRdb := Microservice, Configuration, NatsManager, Api, Svc, RdbManager
	prevConsumer, prevMetrics, prevReader, prevDead, prevDL, prevSweeper := ReportsConsumer, ReportsMetrics, ReportsReader, DeadLetters, DownloadStats, Sweeper
	t.Cleanup(func() {
		Microservice, Configuration, NatsManager, Api, Svc, RdbManager = prevMs, prevCfg, prevNats, prevApi, prevSvc, prevRdb
		ReportsConsumer, ReportsMetrics, ReportsReader, DeadLetters, DownloadStats, Sweeper = prevConsumer, prevMetrics, prevReader, prevDead, prevDL, prevSweeper
	})

	f := &e2e{t: t, instance: fmt.Sprintf("updsvc%d", time.Now().UnixNano()), clock: time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)}
	Microservice = &core.Microservice{InstanceId: f.instance, FunctionalArea: "update-management", Readiness: core.NewReadinessGate()}
	Microservice.UseMetricsRegistry(prometheus.NewRegistry())
	infra := &Microservice.InstanceConfiguration.Infrastructure
	infra.Nats = mscfg.NatsConfiguration{Hostname: host, Port: port}
	infra.Blob = mscfg.BlobConfiguration{Backend: "filesystem", Directory: t.TempDir()}
	infra.ServiceAuth.Secret = "service-secret"
	infra.UserManagement = mscfg.UserManagementConfiguration{Hostname: "um", Port: 8080, InternalPort: 8080}
	infra.DeviceManagement = mscfg.DeviceManagementConfiguration{Hostname: "dm", Port: 8080}
	infra.Secrets = mscfg.SecretsConfiguration{RootKey: base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{9}, 32))}
	Microservice.MicroserviceConfigurationRaw = []byte(`{"updates":{"allowUnverifiedArtifacts":false}}`)
	require.NoError(t, parseConfiguration())

	// The download endpoint is served by the service own mux, as in production.
	f.srv = httptest.NewServer(Microservice.Mux())
	t.Cleanup(f.srv.Close)
	Configuration.Updates.DownloadBaseURL = f.srv.URL
	Configuration.Updates.CapabilityTTLSeconds = 600
	Configuration.Updates.Deadlines = config.DeadlinesConfiguration{
		AcknowledgeSeconds: 3600, DownloadSeconds: 600, VerifySeconds: 300, InstallSeconds: 1800, ConfirmSeconds: 1200}

	db := sqlitetest.OpenMemory(t, nil)
	for _, m := range schema.Migrations {
		require.NoError(t, m.Migrate(db))
	}
	require.NoError(t, rdb.RegisterTenantScoping(db))
	require.NoError(t, rdb.RegisterTokenGrammar(db))
	RdbManager = &rdb.RdbManager{Database: db}

	api, err := buildApi(context.Background())
	require.NoError(t, err)
	Api = api
	// buildApi carried the configuration across; the lookups go to fakes, not to device-management.
	require.Equal(t, f.srv.URL, Api.DownloadBaseURL)
	require.False(t, Api.AllowUnverified, "the e2e flow is the strict one: signed artifacts only")
	require.Equal(t, 600*time.Second, Api.Policy.Download)
	require.NotNil(t, Api.Devices, "the device lookup was not wired from the instance configuration")
	Api.Profiles = fixtureProfiles{}
	Api.Devices = fixtureDevices{"acme/dev-1": "gateway", "acme/dev-2": "gateway", "globex/dev-1": "gateway"}
	Api.Clock = f.now
	require.NoError(t, wireSecrets(context.Background(), Api))
	require.NotNil(t, Api.Secrets)

	// Each tenant has a registered signer whose key every device trusts.
	f.keys = map[string]*signingKey{}
	for _, tenant := range []string{"acme", "globex"} {
		f.keys[tenant] = f.register(tenant, newSigningKey(t, false))
	}
	f.trusted = []string{f.keys["acme"].pem, f.keys["globex"].pem}

	Svc = service.New(Microservice, service.Spec{Nats: &service.NatsSpec{OnCreate: createNatsComponents}})
	DeadLetters = Svc.DeadLetters
	buildMetrics()
	registerDownloadHandler()

	NatsManager = messaging.NewNatsManager(Microservice, core.NewNoOpLifecycleCallbacks(), createNatsComponents)
	NatsManager.RecordMaxDeliveries(deadletter.MaxDeliveryRecorder(Svc.DeadLetters))
	mgr := NatsManager
	t.Cleanup(func() {
		if c := mgr.Conn(); c != nil && !c.IsClosed() {
			c.Close()
		}
	})
	require.NoError(t, mgr.Initialize(context.Background()))
	require.NoError(t, mgr.Start(context.Background()))
	require.NotNil(t, ReportsConsumer, "createNatsComponents built no device-reports consumer")
	require.NotNil(t, Api.Desired, "createNatsComponents wired no device-desired publisher")
	Microservice.Readiness.MarkReadyWithoutAuthSurface()
	require.NoError(t, ReportsConsumer.Start(context.Background()))
	consumer := ReportsConsumer
	t.Cleanup(func() { _ = consumer.Stop(context.Background()) })

	f.nc, err = nats.Connect(fmt.Sprintf("nats://%s:%d", host, port))
	require.NoError(t, err)
	t.Cleanup(f.nc.Close)
	return f
}

func ctxOf(tenant string) context.Context {
	return auth.WithClaims(core.WithTenant(context.Background(), tenant), &auth.Claims{Tenant: tenant, Username: "operator"})
}

func image(n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(i*13 + i/997)
	}
	return b
}

func digestOf(b []byte) string {
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// artifact declares and uploads an artifact through the model, as the upload endpoint does, and
// signs it with the tenant's registered key.
func (f *e2e) artifact(tenant, token, version string, content []byte) {
	f.t.Helper()
	f.unsignedArtifact(tenant, token, version, content)
	f.sign(tenant, token, content, f.keys[tenant])
}

// sign adds the key's signature over the artifact's raw digest.
func (f *e2e) sign(tenant, token string, content []byte, k *signingKey) {
	f.t.Helper()
	sum := sha256.Sum256(content)
	_, err := Api.AddSignature(ctxOf(tenant), token, k.signer.Token, base64.StdEncoding.EncodeToString(k.sign(sum[:])))
	require.NoError(f.t, err)
}

// unsignedArtifact is artifact without the signature.
func (f *e2e) unsignedArtifact(tenant, token, version string, content []byte) {
	f.t.Helper()
	ctx := ctxOf(tenant)
	a, err := Api.CreateArtifact(ctx, &model.ArtifactCreateRequest{
		Token: token, Kind: model.KindFirmware, Name: "fw", Version: version, Component: "firmware",
		SizeBytes: int64(len(content)), Digest: digestOf(content), CompatibleProfiles: []string{"gateway"}})
	require.NoError(f.t, err)
	ref, err := Api.Blobs.Put(ctx, blob.Key{Tenant: tenant, Purpose: model.BlobPurpose, ID: token + "-object"},
		bytes.NewReader(content), blob.PutOptions{MaxSize: int64(len(content))})
	require.NoError(f.t, err)
	require.NoError(f.t, Api.CompleteUpload(ctx, a, ref))
}

// natsLink is the device side of the broker: it publishes on the device own device-reports subject
// and hands what arrives on its device-desired subject to the client.
type natsLink struct {
	f              *e2e
	tenant, device string
	mu             sync.Mutex
	sub            *nats.Subscription
	desired        []string // kinds, in order of arrival
	assignments    [][]byte // every update.assignment payload, verbatim, in order of arrival
}

func (f *e2e) link(tenant, device string) *natsLink {
	return &natsLink{f: f, tenant: tenant, device: device}
}

func (l *natsLink) Send(_ context.Context, payload []byte) error {
	if err := l.f.nc.Publish(messaging.DeviceScopedSubject(l.f.instance, l.tenant, streams.DeviceReports, l.device), payload); err != nil {
		return err
	}
	l.f.sent.Add(1)
	return l.f.nc.Flush()
}

// listen routes the device-desired subject into client.HandleDesired.
func (l *natsLink) listen(client *updater.Client) {
	l.f.t.Helper()
	sub, err := messaging.SubscribeSynced(l.f.nc,
		messaging.DeviceScopedSubject(l.f.instance, l.tenant, streams.DeviceDesired, l.device),
		func(m *nats.Msg) {
			msg, err := ota.DecodeDesiredUpdate(m.Data)
			if err != nil {
				l.f.t.Errorf("platform sent a message the device contract rejects: %v: %s", err, m.Data)
				return
			}
			l.mu.Lock()
			l.desired = append(l.desired, msg.Kind)
			if msg.Kind == ota.KindUpdateAssignment {
				l.assignments = append(l.assignments, append([]byte(nil), m.Data...))
			}
			l.mu.Unlock()
			if err := client.HandleDesired(context.Background(), m.Data); err != nil && !errors.Is(err, updater.ErrNotUpdateKind) && !l.f.nc.IsClosed() {
				l.f.t.Errorf("device refused %s: %v", msg.Kind, err)
			}
		})
	require.NoError(l.f.t, err)
	l.sub = sub
}

func (l *natsLink) drop() { _ = l.sub.Unsubscribe() }

func (l *natsLink) kinds() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.desired...)
}

// cutTransport is the device HTTP client: it records every Range it sends and can cut one response
// body after a number of bytes, as a dropped connection does.
type cutTransport struct {
	mu     sync.Mutex
	ranges []string
	cutAt  int // when > 0, the next response body fails after this many bytes
}

func (c *cutTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	c.mu.Lock()
	c.ranges = append(c.ranges, r.Header.Get("Range"))
	cut := c.cutAt
	c.cutAt = 0
	c.mu.Unlock()
	resp, err := http.DefaultTransport.RoundTrip(r)
	if err != nil || cut == 0 {
		return resp, err
	}
	resp.Body = &cutBody{rc: resp.Body, left: cut}
	return resp, nil
}

func (c *cutTransport) rangeLog() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.ranges...)
}

type cutBody struct {
	rc   io.ReadCloser
	left int
}

func (b *cutBody) Read(p []byte) (int, error) {
	if b.left <= 0 {
		return 0, errors.New("connection reset (injected)")
	}
	if len(p) > b.left {
		p = p[:b.left]
	}
	n, err := b.rc.Read(p)
	b.left -= n
	return n, err
}

func (b *cutBody) Close() error { return b.rc.Close() }

func (f *e2e) device(tenant, device string, chunk int64, httpc *http.Client) (*updater.Client, *natsLink, string) {
	f.t.Helper()
	statePath := filepath.Join(f.t.TempDir(), "state.json")
	return f.openDevice(tenant, device, chunk, httpc, statePath)
}

func (f *e2e) openDevice(tenant, device string, chunk int64, httpc *http.Client, statePath string) (*updater.Client, *natsLink, string) {
	f.t.Helper()
	l := f.link(tenant, device)
	c, err := updater.OpenClient(updater.ClientConfig{
		StatePath: statePath, Component: "firmware", InitialVersion: "1.0.0", ChunkSize: chunk,
		Transport: l, HTTP: httpc, Now: f.now, TrustedKeys: f.trusted, RequireSignature: updater.RequireSignatureOption(true),
	})
	require.NoError(f.t, err)
	l.listen(c)
	f.t.Cleanup(l.drop)
	return c, l, statePath
}

// run drives the device until it has nothing more to do, waiting for grants as they arrive.
func (f *e2e) run(c *updater.Client, until func() bool) {
	f.t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if until() {
			return
		}
		done, err := c.Drive(context.Background(), 1)
		switch {
		case errors.Is(err, updater.ErrAwaitingGrant):
			time.Sleep(15 * time.Millisecond)
		case err != nil:
			f.t.Fatalf("device stopped: %v", err)
		case done:
			time.Sleep(15 * time.Millisecond)
		}
	}
	f.t.Fatalf("device did not reach the expected position within 30s (stage %s)", c.Updater().Snapshot().Stage)
}

func (f *e2e) attemptState(tenant, assignment string) ota.State {
	f.t.Helper()
	v, err := Api.AssignmentByToken(ctxOf(tenant), assignment)
	require.NoError(f.t, err)
	require.NotNil(f.t, v)
	return ota.State(v.Attempt.State)
}

func (f *e2e) waitState(tenant, assignment string, want ota.State) {
	f.t.Helper()
	require.Eventually(f.t, func() bool { return f.attemptState(tenant, assignment) == want }, 20*time.Second, 20*time.Millisecond,
		"attempt %s never reached %s (is %s)", assignment, want, f.attemptState(tenant, assignment))
}

func (f *e2e) letters() []deadletter.Envelope {
	f.t.Helper()
	js, err := f.nc.JetStream()
	require.NoError(f.t, err)
	name := messaging.StreamName(f.instance, streams.DeadLetters)
	info, err := js.StreamInfo(name)
	if err != nil {
		return nil
	}
	var out []deadletter.Envelope
	for seq := info.State.FirstSeq; info.State.Msgs > 0 && seq <= info.State.LastSeq; seq++ {
		raw, err := js.GetMsg(name, seq)
		if err != nil {
			continue
		}
		env, err := deadletter.Unmarshal(raw.Data)
		require.NoError(f.t, err)
		out = append(out, env)
	}
	return out
}

func TestTheServiceWiringServesTheDownloadRouteAndTheDeviceChannels(t *testing.T) {
	startE2E(t)
	_, pattern := Microservice.Mux().Handler(httptest.NewRequest(http.MethodGet, "/updates/download/fw-1?cap=x", nil))
	require.Equal(t, "GET /updates/download/{artifact}", pattern)
	// An anonymous request to it is refused before anything is read, not routed to a 404.
	rec := httptest.NewRecorder()
	Microservice.Mux().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/updates/download/fw-1?cap=x", nil))
	require.Equal(t, http.StatusForbidden, rec.Code)
}

// The whole flow: assign, the device is told, asks for a grant, downloads with ranges, verifies,
// installs, reboots and confirms; the platform ends on UPDATED anchored on the install boot.
func TestADeviceUpdatesEndToEndOverTheBroker(t *testing.T) {
	f := startE2E(t)
	content := image(600_000)
	f.artifact("acme", "fw-2", "2.0.0", content)
	httpc := &http.Client{Transport: &cutTransport{}}
	client, link, _ := f.device("acme", "dev-1", 64<<10, httpc)

	view, err := Api.AssignArtifact(ctxOf("acme"), "dev-1", "fw-2")
	require.NoError(t, err)
	f.run(client, func() bool { return f.attemptState("acme", view.Assignment.Token) == ota.StateUpdated })

	got, err := Api.AssignmentByToken(ctxOf("acme"), view.Assignment.Token)
	require.NoError(t, err)
	require.Equal(t, "2.0.0", client.Updater().Snapshot().RunningVersion)
	require.Equal(t, got.Attempt.LastBootId, client.Updater().Snapshot().BootID)
	require.NotEqual(t, got.Attempt.InstallBootId, got.Attempt.LastBootId, "UPDATED is anchored on a boot after the install boot")
	require.Equal(t, []string{ota.KindUpdateAssignment, ota.KindDownloadGrant}, link.kinds())
	require.Empty(t, f.letters(), "a healthy update wrote a dead letter")

	ranges := httpc.Transport.(*cutTransport).rangeLog()
	require.Len(t, ranges, (len(content)+(64<<10)-1)/(64<<10))
	require.Equal(t, "bytes=0-65535", ranges[0])
}

// A download cut at ~40% and a process restart: the device resumes with Range from the offset it
// persisted, the report sequence continues, the digest verifies, and the update completes.
func TestACutDownloadResumesAfterARestartAndCompletes(t *testing.T) {
	f := startE2E(t)
	content := image(1 << 20)
	f.artifact("acme", "fw-2", "2.0.0", content)
	tr := &cutTransport{}
	client, link, statePath := f.device("acme", "dev-1", 64<<10, &http.Client{Transport: tr})

	view, err := Api.AssignArtifact(ctxOf("acme"), "dev-1", "fw-2")
	require.NoError(t, err)
	f.run(client, func() bool { return client.Updater().Snapshot().BytesHave >= 400_000 })

	// The connection drops in the middle of the next window; the device is then killed.
	tr.mu.Lock()
	tr.cutAt = 10_000
	tr.mu.Unlock()
	_, err = client.Drive(context.Background(), 5)
	require.Error(t, err)
	have := client.Updater().Snapshot().BytesHave
	require.Less(t, have, int64(len(content)))
	require.Equal(t, int64(0), have%(64<<10), "only whole windows are committed")
	link.drop()

	// Restart: a new client over the same state file. It holds no grant, so it asks for one.
	client2, _, _ := f.openDevice("acme", "dev-1", 64<<10, &http.Client{Transport: tr}, statePath)
	require.Equal(t, have, client2.Updater().Snapshot().BytesHave)
	before := len(tr.rangeLog())
	f.run(client2, func() bool { return f.attemptState("acme", view.Assignment.Token) == ota.StateUpdated })

	after := tr.rangeLog()[before:]
	require.Equal(t, fmt.Sprintf("bytes=%d-%d", have, have+(64<<10)-1), after[0],
		"the first request after the restart starts where the device stopped")
	require.Equal(t, "2.0.0", client2.Updater().Snapshot().RunningVersion)
	require.Empty(t, f.letters())
}

// A reboot the device never confirms is UNKNOWN once the confirm deadline passes, not a success and
// not a failure; the device later reports what it runs and the platform reconciles.
func TestARebootWithoutConfirmationIsUnknownUntilTheDeviceSaysWhatItRuns(t *testing.T) {
	f := startE2E(t)
	f.artifact("acme", "fw-2", "2.0.0", image(200_000))
	client, _, _ := f.device("acme", "dev-1", 64<<10, nil)
	client.Updater().SetFaults(updater.Faults{NoConfirmAfterReboot: true})

	view, err := Api.AssignArtifact(ctxOf("acme"), "dev-1", "fw-2")
	require.NoError(t, err)
	f.run(client, func() bool { return f.attemptState("acme", view.Assignment.Token) == ota.StateRebooting })
	// The platform has the REBOOTING report; the device now boots into the new image and says nothing.
	_, err = client.Drive(context.Background(), 3)
	require.NoError(t, err)
	require.Equal(t, "2.0.0", client.Updater().Snapshot().RunningVersion, "the device is on the new image")

	// Not yet past the deadline: nothing moves.
	sweeper := newSweeperForTest()
	f.advance(10 * time.Minute)
	sweeper.Pass(context.Background())
	require.Equal(t, ota.StateRebooting, f.attemptState("acme", view.Assignment.Token))

	f.advance(15 * time.Minute)
	sweeper.Pass(context.Background())
	require.Equal(t, ota.StateUnknown, f.attemptState("acme", view.Assignment.Token))

	// The fault clears; the device reports an inventory and the platform reconciles.
	client.Updater().SetFaults(updater.Faults{})
	require.NoError(t, client.Updater().ReportInventory(context.Background()))
	f.waitState("acme", view.Assignment.Token, ota.StateUpdated)
	got, _ := Api.AssignmentByToken(ctxOf("acme"), view.Assignment.Token)
	require.True(t, got.Attempt.Reconciled)
}

// A device that goes quiet while downloading times out; one told to stop abandons; and the platform
// tells the device in both cases.
func TestTimeoutAndCancelReachTheDevice(t *testing.T) {
	f := startE2E(t)
	f.artifact("acme", "fw-2", "2.0.0", image(300_000))
	client, link, _ := f.device("acme", "dev-1", 64<<10, nil)
	view, err := Api.AssignArtifact(ctxOf("acme"), "dev-1", "fw-2")
	require.NoError(t, err)
	f.run(client, func() bool { return client.Updater().Snapshot().BytesHave >= 128<<10 })

	// Cancel mid-download: the device is told, abandons, and the platform ends on CANCELLED.
	_, err = Api.CancelUpdate(ctxOf("acme"), view.Assignment.Token)
	require.NoError(t, err)
	f.waitState("acme", view.Assignment.Token, ota.StateCancelled)
	require.Contains(t, link.kinds(), ota.KindUpdateCancel)
	require.Equal(t, "ABANDONED", client.Updater().Snapshot().Stage)

	// A new assignment supersedes the cancelled one, and a stalled download times out.
	view2, err := Api.AssignArtifact(ctxOf("acme"), "dev-1", "fw-2")
	require.NoError(t, err)
	require.NotEqual(t, view.Assignment.Token, view2.Assignment.Token)
	f.waitDesired(link, 2)
	f.run(client, func() bool { return f.attemptState("acme", view2.Assignment.Token) == ota.StateDownloading })
	f.advance(11 * time.Minute) // the download deadline is 10 minutes and the device never reported
	newSweeperForTest().Pass(context.Background())
	require.Equal(t, ota.StateTimedOut, f.attemptState("acme", view2.Assignment.Token))
	// The device is told to stop and does; the platform keeps TIMED_OUT (terminal is terminal).
	require.Eventually(t, func() bool { return client.Updater().Snapshot().Stage == "ABANDONED" }, 10*time.Second, 10*time.Millisecond)
	time.Sleep(200 * time.Millisecond)
	require.Equal(t, ota.StateTimedOut, f.attemptState("acme", view2.Assignment.Token))
}

func (f *e2e) waitDesired(l *natsLink, n int) {
	f.t.Helper()
	require.Eventually(f.t, func() bool {
		count := 0
		for _, k := range l.kinds() {
			if k == ota.KindUpdateAssignment {
				count++
			}
		}
		return count >= n
	}, 10*time.Second, 10*time.Millisecond)
}

// A device that is offline when the assignment is made learns of it on its next sync.
func TestASyncDeliversAnAssignmentMadeWhileTheDeviceWasOffline(t *testing.T) {
	f := startE2E(t)
	f.artifact("acme", "fw-2", "2.0.0", image(100_000))
	view, err := Api.AssignArtifact(ctxOf("acme"), "dev-1", "fw-2") // nobody is listening
	require.NoError(t, err)

	client, link, _ := f.device("acme", "dev-1", 64<<10, nil)
	agent, err := updater.OpenConfigAgent(updater.ConfigAgentConfig{Transport: link})
	require.NoError(t, err)
	require.NoError(t, agent.Sync(context.Background()))
	f.waitDesired(link, 1)
	f.run(client, func() bool { return f.attemptState("acme", view.Assignment.Token) == ota.StateUpdated })

	// A second sync inside the cooldown is acknowledged and answered with nothing.
	before := len(link.kinds())
	require.NoError(t, agent.Sync(context.Background()))
	time.Sleep(300 * time.Millisecond)
	require.Equal(t, before, len(link.kinds()), "a finished update is not re-sent on sync")
}

// A device cannot be given, or take, an assignment that is not its own.
func TestAnotherDeviceOrTenantCannotDownloadSomeoneElsesAssignment(t *testing.T) {
	f := startE2E(t)
	f.artifact("acme", "fw-2", "2.0.0", image(100_000))
	view, err := Api.AssignArtifact(ctxOf("acme"), "dev-1", "fw-2")
	require.NoError(t, err)

	// dev-2 of the same tenant, and dev-1 of another tenant, ask for dev-1's assignment.
	for _, who := range []struct{ tenant, device string }{{"acme", "dev-2"}, {"globex", "dev-1"}} {
		client, link, _ := f.device(who.tenant, who.device, 64<<10, nil)
		_ = client
		raw := fmt.Sprintf(`{"v":1,"kind":"download.request","assignmentId":%q}`, view.Assignment.Token)
		require.NoError(t, link.Send(context.Background(), []byte(raw)))
		// The platform answers by telling the device to drop what it does not hold, never with a grant.
		require.Eventually(t, func() bool { return len(link.kinds()) == 1 }, 10*time.Second, 10*time.Millisecond, who.tenant+"/"+who.device)
		require.Equal(t, []string{ota.KindUpdateCancel}, link.kinds())
	}
	require.Equal(t, ota.StateQueued, f.attemptState("acme", view.Assignment.Token), "nothing moved the real attempt")
}

func newSweeperForTest() *processor.DeadlineSweeper {
	return processor.NewDeadlineSweeper(Api, time.Minute, &ReportsMetrics)
}

func (f *e2e) assignmentOf(tenant, token string) *model.UpdateAssignmentView {
	f.t.Helper()
	v, err := Api.AssignmentByToken(ctxOf(tenant), token)
	require.NoError(f.t, err)
	require.NotNil(f.t, v)
	return v
}

// The platform pins a signature and sends SIGNED with the flag OFF; a device that requires a
// signature and trusts the signer's key installs it. Both key types, so each registry algorithm
// crosses the wire under its wire name.
func TestASignedUpdateCompletesWithTheFlagOff(t *testing.T) {
	for name, ed := range map[string]bool{"ecdsa-p256": false, "ed25519": true} {
		t.Run(name, func(t *testing.T) {
			f := startE2E(t)
			key := f.register("acme", newSigningKey(t, ed))
			f.trusted = []string{key.pem}
			content := image(150_000)
			f.unsignedArtifact("acme", "fw-2", "2.0.0", content)
			f.sign("acme", "fw-2", content, key)
			client, link, _ := f.device("acme", "dev-1", 64<<10, nil)

			view, err := Api.AssignArtifact(ctxOf("acme"), "dev-1", "fw-2")
			require.NoError(t, err)
			require.NotNil(t, view.Pin)
			require.Equal(t, key.signer.Token, view.Pin.Signer.Token)
			f.run(client, func() bool { return f.attemptState("acme", view.Assignment.Token) == ota.StateUpdated })

			require.Len(t, link.assignments, 1)
			msg, err := ota.DecodeDesiredUpdate(link.assignments[0])
			require.NoError(t, err)
			require.Equal(t, ota.TrustSigned, msg.Assignment.Trust)
			require.Equal(t, key.signer.KeyId, msg.Assignment.KeyID)
			wantAlg := ota.AlgorithmECDSAP256SHA256
			if ed {
				wantAlg = ota.AlgorithmEd25519
			}
			require.Equal(t, wantAlg, msg.Assignment.Algorithm)
			require.Empty(t, f.letters())
		})
	}
}

// A device that does not hold the signer's key refuses the update with UNKNOWN_SIGNER, failing at
// the DOWNLOADED stage, and the platform records it against the device.
func TestADeviceWithoutTheSignersKeyFailsWithUnknownSigner(t *testing.T) {
	f := startE2E(t)
	f.trusted = []string{newSigningKey(t, false).pem} // not the registered signer's key
	f.artifact("acme", "fw-2", "2.0.0", image(150_000))
	client, _, _ := f.device("acme", "dev-1", 64<<10, nil)

	view, err := Api.AssignArtifact(ctxOf("acme"), "dev-1", "fw-2")
	require.NoError(t, err)
	f.run(client, func() bool { return f.attemptState("acme", view.Assignment.Token) == ota.StateFailed })

	got := f.assignmentOf("acme", view.Assignment.Token)
	require.Equal(t, ota.CodeUnknownSigner, got.Attempt.FailureCode)
	require.Equal(t, string(ota.StageDownloaded), got.Attempt.FailureStage)
	require.NotEqual(t, "2.0.0", client.Updater().Snapshot().RunningVersion)
}

// A signature damaged in transit fails verification on the device: SIGNATURE_INVALID.
func TestACorruptedSignatureFailsWithSignatureInvalid(t *testing.T) {
	f := startE2E(t)
	f.artifact("acme", "fw-2", "2.0.0", image(150_000))
	client, _, _ := f.device("acme", "dev-1", 64<<10, nil)
	client.Updater().SetFaults(updater.Faults{CorruptSignature: true})

	view, err := Api.AssignArtifact(ctxOf("acme"), "dev-1", "fw-2")
	require.NoError(t, err)
	f.run(client, func() bool { return f.attemptState("acme", view.Assignment.Token) == ota.StateFailed })

	got := f.assignmentOf("acme", view.Assignment.Token)
	require.Equal(t, ota.CodeSignatureInvalid, got.Attempt.FailureCode)
	require.Equal(t, string(ota.StageDownloaded), got.Attempt.FailureStage)
}

// Revoking the signer mid-flight: the update already under way keeps the signature it was
// assigned under and completes, a re-sync sends the byte-identical message, and a NEW assignment
// of the same artifact is refused.
func TestARevokeMidFlightLeavesTheInFlightUpdateAloneAndRefusesNewAssignments(t *testing.T) {
	f := startE2E(t)
	f.artifact("acme", "fw-2", "2.0.0", image(600_000))
	client, link, _ := f.device("acme", "dev-1", 64<<10, nil)

	view, err := Api.AssignArtifact(ctxOf("acme"), "dev-1", "fw-2")
	require.NoError(t, err)
	f.run(client, func() bool { return client.Updater().Snapshot().BytesHave >= 128<<10 })

	_, err = Api.RevokeSigner(ctxOf("acme"), f.keys["acme"].signer.Token)
	require.NoError(t, err)

	// A new assignment of the now-unsigned artifact is refused.
	_, err = Api.AssignArtifact(ctxOf("acme"), "dev-2", "fw-2")
	var refusal *model.RefusalError
	require.ErrorAs(t, err, &refusal)
	require.Equal(t, model.CodeUnverifiedRefused, refusal.Code)

	// The device syncs: the re-sent assignment is byte-identical to the first.
	agent, err := updater.OpenConfigAgent(updater.ConfigAgentConfig{Transport: link})
	require.NoError(t, err)
	require.NoError(t, agent.Sync(context.Background()))
	require.Eventually(t, func() bool { link.mu.Lock(); defer link.mu.Unlock(); return len(link.assignments) >= 2 },
		10*time.Second, 10*time.Millisecond)
	link.mu.Lock()
	require.Equal(t, string(link.assignments[0]), string(link.assignments[1]))
	link.mu.Unlock()

	f.run(client, func() bool { return f.attemptState("acme", view.Assignment.Token) == ota.StateUpdated })
	require.Equal(t, "2.0.0", client.Updater().Snapshot().RunningVersion)
}
