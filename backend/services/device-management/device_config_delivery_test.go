// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"sync"
	"testing"
	"time"

	dmconfig "github.com/devicechain-io/dc-device-management/config"
	"github.com/devicechain-io/dc-device-management/model"
	"github.com/devicechain-io/dc-device-management/processor"
	mscfg "github.com/devicechain-io/dc-microservice/config"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/deadletter"
	"github.com/devicechain-io/dc-microservice/entity"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/ota"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/devicechain-io/dc-microservice/rdb/sqlitetest"
	"github.com/devicechain-io/dc-microservice/service"
	"github.com/devicechain-io/dc-microservice/streams"
	"github.com/devicechain-io/dc-microservice/test/msgtest"
	nats "github.com/nats-io/nats.go"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"
)

// These tests run the configuration delivery loop over a real embedded broker, through the
// service's own wiring: createNatsComponents builds the device-desired publisher and the
// device-reports reader, and the consumer is the one main.go builds. What stands in for the
// device is a small client that speaks the wire literals of the golden fixtures, and what stands
// in for the store is SQLite with tenant scoping registered, so the tenant predicate is the real one.

type deliveryFixture struct {
	instance string
	nc       *nats.Conn
	reg      *prometheus.Registry
}

func startDeliveryFixture(t *testing.T) *deliveryFixture {
	t.Helper()
	host, port := startEmbeddedNats(t)

	prevMs, prevCfg, prevNats, prevApi, prevCached, prevSvc, prevRdb := Microservice, Configuration, NatsManager, Api, CachedApi, Svc, RdbManager
	prevConsumer, prevMetrics, prevRaise, prevReader := DeviceReportsConsumer, DeviceReportsMetrics, RaiseAlarmConsumer, DeviceReportsReader
	t.Cleanup(func() {
		Microservice, Configuration, NatsManager, Api, CachedApi, Svc, RdbManager = prevMs, prevCfg, prevNats, prevApi, prevCached, prevSvc, prevRdb
		DeviceReportsConsumer, DeviceReportsMetrics, RaiseAlarmConsumer, DeviceReportsReader = prevConsumer, prevMetrics, prevRaise, prevReader
	})

	instance := fmt.Sprintf("cfgdeliv%d", time.Now().UnixNano())
	Microservice = &core.Microservice{InstanceId: instance, FunctionalArea: "device-management", Readiness: core.NewReadinessGate()}
	reg := prometheus.NewRegistry()
	Microservice.UseMetricsRegistry(reg)
	Microservice.InstanceConfiguration.Infrastructure.Nats = mscfg.NatsConfiguration{Hostname: host, Port: port}
	Microservice.MicroserviceConfigurationRaw = []byte(`{}`)
	require.NoError(t, parseConfiguration())
	Configuration = &dmconfig.DeviceManagementConfiguration{}
	// createNatsComponents builds the inbound processor, which refuses to exist without the
	// key that opens sealed credential secrets.
	prevTransit := SecretTransit
	t.Cleanup(func() { SecretTransit = prevTransit })
	SecretTransit = testTransit(t)
	Svc = service.New(Microservice, service.Spec{Nats: &service.NatsSpec{OnCreate: createNatsComponents}})
	buildMetrics()

	db := sqlitetest.OpenMemory(t, nil)
	require.NoError(t, rdb.RegisterTenantScoping(db))
	require.NoError(t, db.AutoMigrate(&model.Device{}, &model.DeviceType{}, &model.DeviceCredential{}, &model.DeviceReplacement{},
		&model.EntityAttribute{}, &model.EntityGroup{}, &model.IssuedGroupToken{}, &model.EntityGroupVersion{}, &model.EntityGroupMembership{},
		&model.EntityGroupFacetRef{}, &model.EntityRelationship{}, &model.PendingOwnershipFact{}, &model.Alarm{},
		&model.DeviceProfile{}, &model.DeviceProfileVersion{}, &model.DeviceProfileActivation{}, &model.MetricDefinition{}, &model.CommandDefinition{},
		&model.DetectionRule{}, &model.DetectionRuleScopeRef{},
		&model.DeviceConfigurationRevision{}, &model.DeviceConfigurationState{}))
	RdbManager = &rdb.RdbManager{Database: db}
	Api = model.NewApi(RdbManager)
	CachedApi = model.NewCachedApi(Api, &model.Caches{DeviceByToken: msgtest.NewMemoryKV().NewCache()})

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
	require.NotNil(t, DeviceReportsConsumer, "createNatsComponents built no device-reports consumer")
	require.NotNil(t, Api.DeviceDesiredPublisher, "createNatsComponents injected no device-desired publisher")
	// The reader fetches nothing until the instance gate opens, as in production.
	Microservice.Readiness.MarkReadyWithoutAuthSurface()
	require.NoError(t, DeviceReportsConsumer.Start(context.Background()))
	consumer := DeviceReportsConsumer
	t.Cleanup(func() { _ = consumer.Stop(context.Background()) })

	nc, err := nats.Connect(fmt.Sprintf("nats://%s:%d", host, port))
	require.NoError(t, err)
	t.Cleanup(nc.Close)
	return &deliveryFixture{instance: instance, nc: nc, reg: reg}
}

// seedConfiguredDevice creates profile/type/device for tenant, the profile declaring "mode".
func seedConfiguredDevice(t *testing.T, tenant, token string) context.Context {
	t.Helper()
	ctx := core.WithTenant(context.Background(), tenant)
	p := &model.DeviceProfile{}
	p.Token = "prof"
	require.NoError(t, Api.RDB.DB(ctx).Create(p).Error)
	_, err := Api.SetDeviceProfileConfigurationDeclaration(ctx, "prof", []model.ConfigurationKey{{Key: "mode", ValueType: "STRING"}})
	require.NoError(t, err)
	_, err = Api.PublishDeviceProfile(ctx, "prof", nil, nil, "tester")
	require.NoError(t, err)
	profile := "prof"
	_, err = Api.CreateDeviceType(ctx, &model.DeviceTypeCreateRequest{Token: "sensor", ProfileToken: &profile})
	require.NoError(t, err)
	_, err = Api.CreateDevice(ctx, &model.DeviceCreateRequest{Token: token, DeviceTypeToken: "sensor"})
	require.NoError(t, err)
	return ctx
}

func setMode(t *testing.T, ctx context.Context, token, value string) {
	t.Helper()
	_, err := Api.SetEntityAttribute(ctx, &model.EntityAttributeSetRequest{
		EntityType: string(entity.TypeDevice), Entity: token, Scope: string(model.AttributeScopeShared),
		AttrKey: "mode", ValueType: string(model.AttributeValueString), Value: &value,
	})
	require.NoError(t, err)
}

// wireDevice is the device side: it listens on its own device-desired subject and reports on its
// own device-reports subject. It is online only between listen and drop.
type wireDevice struct {
	f      *deliveryFixture
	tenant string
	token  string

	mu   sync.Mutex
	got  []ota.ConfigRevision
	sub  *nats.Subscription
	seen chan struct{}
}

func (f *deliveryFixture) device(tenant, token string) *wireDevice {
	return &wireDevice{f: f, tenant: tenant, token: token, seen: make(chan struct{}, 64)}
}

func (d *wireDevice) listen(t *testing.T) {
	t.Helper()
	sub, err := messaging.SubscribeSynced(d.f.nc,
		messaging.DeviceScopedSubject(d.f.instance, d.tenant, streams.DeviceDesired, d.token),
		func(m *nats.Msg) {
			rev, err := ota.DecodeConfigRevision(m.Data)
			if err != nil {
				t.Errorf("platform sent a message the device contract rejects: %v: %s", err, m.Data)
				return
			}
			d.mu.Lock()
			d.got = append(d.got, rev)
			d.mu.Unlock()
			d.seen <- struct{}{}
		})
	require.NoError(t, err)
	d.sub = sub
}

func (d *wireDevice) drop() { _ = d.sub.Unsubscribe() }

func (d *wireDevice) received() []ota.ConfigRevision {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]ota.ConfigRevision(nil), d.got...)
}

// next waits for one more desired message than the device has consumed so far.
func (d *wireDevice) next(t *testing.T) ota.ConfigRevision {
	t.Helper()
	select {
	case <-d.seen:
	case <-time.After(10 * time.Second):
		t.Fatalf("device %s/%s was sent nothing within 10s", d.tenant, d.token)
	}
	got := d.received()
	return got[len(got)-1]
}

func (d *wireDevice) publish(t *testing.T, body string) {
	t.Helper()
	require.NoError(t, d.f.nc.Publish(
		messaging.DeviceScopedSubject(d.f.instance, d.tenant, streams.DeviceReports, d.token), []byte(body)))
	require.NoError(t, d.f.nc.Flush())
}

func (d *wireDevice) sync(t *testing.T, have int64) {
	d.publish(t, fmt.Sprintf(`{"v":1,"kind":"sync","bootId":"boot-1","config":{"haveRevision":%d}}`, have))
}

func (d *wireDevice) status(t *testing.T, rev ota.ConfigRevision, status string, errs ...string) {
	body := fmt.Sprintf(`{"v":1,"kind":"config.status","revision":%d,"digest":%q,"status":%q`, rev.Revision, rev.Digest, status)
	if len(errs) > 0 {
		body += fmt.Sprintf(`,"errors":[%q]`, errs[0])
	}
	d.publish(t, body+"}")
}

func configState(t *testing.T, ctx context.Context, token string) *model.DeviceConfiguration {
	t.Helper()
	c, err := Api.DeviceConfigurationByToken(ctx, token)
	require.NoError(t, err)
	return c
}

func (f *deliveryFixture) counter(t *testing.T, name, reason string) float64 {
	t.Helper()
	mfs, err := f.reg.Gather()
	require.NoError(t, err)
	var total float64
	for _, mf := range mfs {
		if !strings.HasSuffix(mf.GetName(), name) {
			continue
		}
		for _, m := range mf.GetMetric() {
			match := reason == ""
			for _, l := range m.GetLabel() {
				if l.GetName() == "reason" && l.GetValue() == reason {
					match = true
				}
			}
			if match {
				total += m.GetCounter().GetValue()
			}
		}
	}
	return total
}

func (f *deliveryFixture) letters(t *testing.T) []deadletter.Envelope {
	t.Helper()
	js, err := f.nc.JetStream()
	require.NoError(t, err)
	info, err := js.StreamInfo(messaging.StreamName(f.instance, streams.DeadLetters))
	if err != nil {
		return nil
	}
	var out []deadletter.Envelope
	for seq := info.State.FirstSeq; info.State.Msgs > 0 && seq <= info.State.LastSeq; seq++ {
		raw, err := js.GetMsg(messaging.StreamName(f.instance, streams.DeadLetters), seq)
		if err != nil {
			continue
		}
		env, err := deadletter.Unmarshal(raw.Data)
		require.NoError(t, err)
		out = append(out, env)
	}
	return out
}

// Mint a revision -> it is published to the device -> the device reports -> the state row is
// written. Then the device goes offline, its configuration changes, and a sync after it reconnects
// brings it the current revision.
func TestConfigurationDeliveryLoop(t *testing.T) {
	f := startDeliveryFixture(t)
	ctx := seedConfiguredDevice(t, "acme", "dev")
	dev := f.device("acme", "dev")
	dev.listen(t)

	setMode(t, ctx, "dev", "eco")
	r1 := dev.next(t)
	require.EqualValues(t, 1, r1.Revision)
	require.True(t, r1.DigestMatches(), "the device could not trust what it was sent")
	require.Equal(t, `{"mode":"eco"}`, r1.Document)
	require.True(t, configState(t, ctx, "dev").Pending)

	dev.status(t, r1, ota.ConfigReceived)
	dev.status(t, r1, ota.ConfigApplied)
	require.Eventually(t, func() bool { return !configState(t, ctx, "dev").Pending }, 10*time.Second, 20*time.Millisecond,
		"the platform never recorded the device's APPLIED report")
	c := configState(t, ctx, "dev")
	require.Equal(t, "APPLIED", c.Reported.ReportedStatus.String)
	require.EqualValues(t, 1, c.Reported.ReportedRevision.Int64)

	// Offline: the revision minted now has no listener.
	dev.drop()
	setMode(t, ctx, "dev", "turbo")
	require.True(t, configState(t, ctx, "dev").Pending)

	// Back online: it syncs and is sent the current revision.
	dev.listen(t)
	dev.sync(t, 1)
	r2 := dev.next(t)
	require.EqualValues(t, 2, r2.Revision)
	require.Equal(t, `{"mode":"turbo"}`, r2.Document)
	dev.status(t, r2, ota.ConfigApplied)

	// A second sync straight away is acknowledged and counted, and delivers nothing.
	before := len(dev.received())
	dev.sync(t, 2)
	require.Eventually(t, func() bool { return f.counter(t, "config_reports_rejected_total", "throttled") == 1 }, 10*time.Second, 20*time.Millisecond)
	require.Len(t, dev.received(), before, "a throttled sync delivered a revision")
	require.Eventually(t, func() bool { return !configState(t, ctx, "dev").Pending }, 10*time.Second, 20*time.Millisecond)
	require.NotNil(t, configState(t, ctx, "dev").Reported.LastSyncAt)
	require.Empty(t, f.letters(t), "a healthy loop wrote a dead letter")
}

// Malformed and oversized reports are dead-lettered and counted, never applied; the state row is
// untouched and the consumer keeps working afterwards.
func TestMalformedAndOversizedReportsAreDeadLettered(t *testing.T) {
	f := startDeliveryFixture(t)
	ctx := seedConfiguredDevice(t, "acme", "dev")
	dev := f.device("acme", "dev")
	dev.listen(t)
	setMode(t, ctx, "dev", "eco")
	r1 := dev.next(t)

	dev.publish(t, `{"v":1,"kind":"config.status","revision":1,"digest":`)                                                          // truncated JSON
	dev.publish(t, `{"v":1,"kind":"config.status","revision":1,"digest":"`+r1.Digest+`","status":"APPLIED","deviceToken":"other"}`) // identity in the body, otherwise valid
	dev.publish(t, `{"v":1,"kind":"config.status","revision":1,"revision":9,"status":"APPLIED"}`)                                   // duplicate key
	dev.publish(t, `{"v":1,"kind":"config.status","revision":1,"digest":"`+r1.Digest+`","status":"APPLIED","pad":"`+
		strings.Repeat("x", ota.MaxReportBytes)+`"}`) // over the 4 KiB cap

	require.Eventually(t, func() bool { return len(f.letters(t)) == 4 }, 10*time.Second, 20*time.Millisecond,
		"every refused message should leave a dead letter")
	for _, l := range f.letters(t) {
		require.Equal(t, deadletter.ReasonUnprocessable, l.Reason)
		require.Equal(t, "dev", l.Reference)
		require.LessOrEqual(t, len(l.Payload), 4096, "a dead letter must bound what a device chose to send")
	}
	require.EqualValues(t, 4, f.counter(t, "config_reports_rejected_total", "malformed"))
	require.Nil(t, configState(t, ctx, "dev").Reported, "a refused report wrote reported state")

	// And the loop still works.
	dev.status(t, r1, ota.ConfigApplied)
	require.Eventually(t, func() bool { return !configState(t, ctx, "dev").Pending }, 10*time.Second, 20*time.Millisecond)
}

// A report for a revision the device was never sent, or with a digest that is not that
// revision's, is refused, counted and lettered; the recorded state does not move.
func TestForgedReportsAreRefused(t *testing.T) {
	f := startDeliveryFixture(t)
	ctx := seedConfiguredDevice(t, "acme", "dev")
	dev := f.device("acme", "dev")
	dev.listen(t)
	setMode(t, ctx, "dev", "eco")
	r1 := dev.next(t)

	ghost := r1
	ghost.Revision = 7
	dev.status(t, ghost, ota.ConfigApplied)
	forged := r1
	forged.Digest = "sha256:" + strings.Repeat("0", 64)
	dev.status(t, forged, ota.ConfigApplied)

	require.Eventually(t, func() bool { return len(f.letters(t)) == 2 }, 10*time.Second, 20*time.Millisecond)
	require.EqualValues(t, 1, f.counter(t, "config_reports_rejected_total", "unknown_revision"))
	require.EqualValues(t, 1, f.counter(t, "config_reports_rejected_total", "digest_mismatch"))
	require.True(t, configState(t, ctx, "dev").Pending)
	require.Nil(t, configState(t, ctx, "dev").Reported)
}

// Two tenants can each have a device with the same token. A report lands only on the tenant whose
// subject carried it, and a device of one tenant cannot be reported for from another's subject.
func TestReportsAreTenantIsolated(t *testing.T) {
	f := startDeliveryFixture(t)
	acme := seedConfiguredDevice(t, "acme", "dev")
	globex := seedConfiguredDevice(t, "globex", "dev")
	a, g := f.device("acme", "dev"), f.device("globex", "dev")
	a.listen(t)
	g.listen(t)
	setMode(t, acme, "dev", "eco")
	setMode(t, globex, "dev", "turbo")
	ra, rg := a.next(t), g.next(t)
	require.Equal(t, `{"mode":"eco"}`, ra.Document)
	require.Equal(t, `{"mode":"turbo"}`, rg.Document)

	// globex's device reports acme's revision digest: globex has revision 1 with another digest.
	g.status(t, ra, ota.ConfigApplied)
	require.Eventually(t, func() bool { return f.counter(t, "config_reports_rejected_total", "digest_mismatch") == 1 }, 10*time.Second, 20*time.Millisecond)
	require.Nil(t, configState(t, globex, "dev").Reported)
	require.Nil(t, configState(t, acme, "dev").Reported)

	// acme's device reports its own: acme converges, globex does not.
	a.status(t, ra, ota.ConfigApplied)
	require.Eventually(t, func() bool { return !configState(t, acme, "dev").Pending }, 10*time.Second, 20*time.Millisecond)
	require.True(t, configState(t, globex, "dev").Pending)
	require.Nil(t, configState(t, globex, "dev").Reported)

	// A device the tenant does not have is counted, not applied.
	ghost := f.device("acme", "nobody")
	ghost.sync(t, 0)
	require.Eventually(t, func() bool { return f.counter(t, "config_reports_rejected_total", "unknown_device") == 1 }, 10*time.Second, 20*time.Millisecond)
}

// Nothing else runs main.go's lifecycle functions, so this reads them: the device-reports
// consumer must be started, stopped and terminated by the service, or the loop is built, wired
// and never turned on.
func TestMainStartsStopsAndTerminatesTheDeviceReportsConsumer(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "main.go", nil, 0)
	require.NoError(t, err)
	want := map[string]string{
		"afterMicroserviceStarted":     "Start",
		"beforeMicroserviceStopped":    "Stop",
		"beforeMicroserviceTerminated": "Terminate",
	}
	found := map[string]bool{}
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || want[fn.Name.Name] == "" {
			continue
		}
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			if id, ok := sel.X.(*ast.Ident); ok && id.Name == "DeviceReportsConsumer" && sel.Sel.Name == want[fn.Name.Name] {
				found[fn.Name.Name] = true
			}
			return true
		})
	}
	for fn, call := range want {
		require.True(t, found[fn], "%s never calls DeviceReportsConsumer.%s", fn, call)
	}
}

// A sync or report resolves its device by token. Through the cached Api a device deleted on
// another replica would stay resolvable here until its copy expired, so the consumer is handed the
// plain one.
func TestTheDeviceReportsConsumerResolvesDevicesUncached(t *testing.T) {
	savedMs, savedApi, savedCached, savedMetrics := Microservice, Api, CachedApi, DeviceReportsMetrics
	t.Cleanup(func() {
		Microservice, Api, CachedApi, DeviceReportsMetrics = savedMs, savedApi, savedCached, savedMetrics
	})

	Microservice = &core.Microservice{InstanceId: "test", FunctionalArea: "device-management"}
	Microservice.UseMetricsRegistry(prometheus.NewRegistry())
	Api = model.NewApi(&rdb.RdbManager{})
	CachedApi = model.NewCachedApi(Api, &model.Caches{DeviceByToken: msgtest.NewMemoryKV().NewCache()})
	DeviceReportsMetrics = processor.NewDeviceReportsMetrics(Microservice)

	rc := newDeviceReportsConsumer(nil, nil)
	got, ok := rc.Api.(*model.Api)
	require.True(t, ok, "the device-reports consumer was given a %T, want the plain *model.Api", rc.Api)
	require.Same(t, Api, got)
}

// The update family belongs to update-management, which reads the same channel on its own
// durable. This consumer acknowledges those kinds without a dead letter and without counting a
// refusal, and keeps serving configuration afterwards.
func TestUpdateFamilyReportsAreLeftToUpdateManagement(t *testing.T) {
	f := startDeliveryFixture(t)
	ctx := seedConfiguredDevice(t, "acme", "dev")
	dev := f.device("acme", "dev")
	dev.listen(t)
	setMode(t, ctx, "dev", "eco")
	r1 := dev.next(t)

	dev.publish(t, `{"v":1,"kind":"update.inventory","component":"firmware","bootId":"b","running":{"version":"1"}}`)
	dev.publish(t, `{"v":1,"kind":"download.request","assignmentId":"asg-1"}`)
	dev.publish(t, `{"v":1,"kind":"update.progress","attemptId":"att-1","assignmentId":"asg-1","component":"firmware",`+
		`"artifactDigest":"sha256:`+strings.Repeat("a", 64)+`","seq":1,"stage":"RECEIVED","bootId":"b"}`)

	// A configuration report published after them is applied, so by then the three were consumed.
	dev.status(t, r1, ota.ConfigApplied)
	require.Eventually(t, func() bool { return !configState(t, ctx, "dev").Pending }, 10*time.Second, 20*time.Millisecond)
	require.Empty(t, f.letters(t), "an update-family report was dead-lettered by the configuration consumer")
	for _, reason := range []string{"malformed", "unattributable", "throttled", "unknown_device", "unknown_revision", "digest_mismatch", "stale"} {
		require.Zero(t, f.counter(t, "config_reports_rejected_total", reason), reason)
	}
}
