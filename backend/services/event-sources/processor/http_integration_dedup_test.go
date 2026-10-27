// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/devicechain-io/dc-event-sources/integration"
	"github.com/devicechain-io/dc-event-sources/model"
	core "github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/streams"
	dctest "github.com/devicechain-io/dc-microservice/test"
	natsserver "github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
	"github.com/stretchr/testify/require"
)

// 🔴 THE DEFECT. The broker dedups an integration publish on its Nats-Msg-Id, which was
// tenant:AltId. An AltId is the sender's own key, with no alias scheme in it, so two generic
// integrations on different schemes can send the same sender-local identifier, body and time and
// reach DIFFERENT devices: both requests answered 202 and the broker stored only the first
// device's event. The id now names the resolved device as well.

// aliasBackend serves one generic integration under its own alias scheme. The route
// authenticates the path token "cs"; the integration's real token is the slug the request
// arrived under.
type aliasBackend struct {
	*routeBackend
}

func (b aliasBackend) Auth(ctx context.Context, tenant, slug, bearer string) (*integration.Descriptor, error) {
	d, err := b.routeBackend.Auth(ctx, tenant, "cs", bearer)
	if d != nil {
		d.Token = slug
	}
	return d, err
}

// aliasRoute points a harness at a generic integration under scheme, whose sender-local
// identifiers resolve to the scheme's own device, and returns the path to post to.
func aliasRoute(h *routeHarness, scheme string) string {
	settings := `{"idFrom":"header:X-Device","timePointer":"/at"}`
	h.backend.desc = func() integration.Descriptor {
		return integration.Descriptor{Type: integration.GenericJSON, Identity: "ALIAS:" + scheme, Generic: &settings}
	}
	h.backend.resolve = func(_, _ string) (integration.Resolution, error) {
		return integration.Resolution{Outcome: integration.Resolved, DeviceToken: "device-" + scheme}, nil
	}
	b := aliasBackend{h.backend}
	h.route.Auth = integration.NewAuthenticator(b, integration.AuthConfig{})
	h.route.Devices = integration.NewDevices(b, integration.DeviceConfig{})
	return "/inst-1/acme/integrations/" + strings.ReplaceAll(scheme, ".", "-")
}

const sameReading = `{"at":"2022-07-18T09:34:16Z","temperature":22}`

func postSameReading(t *testing.T, h *routeHarness, path string) {
	t.Helper()
	r := h.post(path, goodBearer, sameReading, func(r *http.Request) { r.Header.Set("X-Device", "sensor-1") })
	require.Equal(t, http.StatusAccepted, r.Code, r.Body.String())
}

func TestDistinctDevicesDoNotShareABrokerDedupID(t *testing.T) {
	ids := map[string]string{}
	for _, scheme := range []string{"custom.alpha", "custom.beta"} {
		h := newRouteHarness(t)
		postSameReading(t, h, aliasRoute(h, scheme))
		p := h.published1()
		require.Equal(t, "device-"+scheme, p.event.Device)
		ids[scheme] = p.dedupID
	}
	require.NotEqual(t, ids["custom.alpha"], ids["custom.beta"],
		"different devices in separate alias namespaces collide in JetStream deduplication")

	// The same device's retransmission keeps the same id: that is the dedup the id exists for.
	h := newRouteHarness(t)
	path := aliasRoute(h, "custom.alpha")
	postSameReading(t, h, path)
	postSameReading(t, h, path)
	require.Len(t, h.published, 2)
	require.Equal(t, h.published[0].dedupID, h.published[1].dedupID)
	require.Equal(t, ids["custom.alpha"], h.published[0].dedupID)
}

// The id is a function of (tenant, device, AltId) with no ambiguity between the parts, bounded
// whatever the AltId holds, and still names the tenant in the clear.
func TestIntegrationDedupIDShape(t *testing.T) {
	long := strings.Repeat("a", integration.MaxAltIdLen)
	id := integrationDedupID("acme", "dev-1", long)
	require.True(t, strings.HasPrefix(id, "acme:"))
	require.Len(t, id, len("acme:")+43)

	require.Equal(t, id, integrationDedupID("acme", "dev-1", long))
	for name, other := range map[string]string{
		"tenant":                  integrationDedupID("acm2", "dev-1", long),
		"device":                  integrationDedupID("acme", "dev-2", long),
		"altid":                   integrationDedupID("acme", "dev-1", long[1:]),
		"device moved into altid": integrationDedupID("acme", "dev", "-1"+long),
	} {
		require.NotEqual(t, id, other, name)
	}
	require.NotEqual(t, integrationDedupID("acme", "dev-1a", "b"), integrationDedupID("acme", "dev-1", "ab"),
		"the boundary between device and AltId is ambiguous")
	// A golden: the id rides the wire, so a change to its derivation is a decision, not a refactor.
	require.Equal(t, "acme:PINlhbrm5BMMasmXqb2vmTJj_wEes2YreY2xDO26CUs", integrationDedupID("acme", "dev-1", "cs:1"))
}

// jsRig is an embedded JetStream broker with the platform's inbound-events writer on it.
type jsRig struct {
	nc     *nats.Conn
	writer messaging.MessageWriter
	stream string
}

func newJsRig(t *testing.T) *jsRig {
	t.Helper()
	srv, err := natsserver.NewServer(&natsserver.Options{
		Host: "127.0.0.1", Port: -1, JetStream: true, NoLog: true, NoSigs: true, ServerName: "dedup-rig",
		StoreDir: dctest.JetStreamStoreDir(t),
	})
	require.NoError(t, err)
	go srv.Start()
	require.True(t, srv.ReadyForConnections(15*time.Second), "embedded nats server not ready")
	t.Cleanup(srv.Shutdown)
	u, err := url.Parse(srv.ClientURL())
	require.NoError(t, err)
	port, err := strconv.Atoi(u.Port())
	require.NoError(t, err)

	ms := &core.Microservice{InstanceId: testInstance, FunctionalArea: "event-sources", Readiness: core.NewReadinessGate()}
	ms.InstanceConfiguration.Infrastructure.Nats.Hostname = u.Hostname()
	ms.InstanceConfiguration.Infrastructure.Nats.Port = uint32(port)
	ms.InstanceConfiguration.Infrastructure.Nats.StreamReplicas = 1
	ms.Readiness.MarkReadyWithoutAuthSurface()
	r := &jsRig{stream: messaging.StreamName(testInstance, streams.InboundEvents)}
	nmgr := messaging.NewNatsManager(ms, core.NewNoOpLifecycleCallbacks(), func(n *messaging.NatsManager) error {
		w, err := n.NewWriter(streams.InboundEvents)
		r.writer = w
		return err
	})
	require.NoError(t, nmgr.Initialize(context.Background()))
	require.NoError(t, nmgr.Start(context.Background()))
	t.Cleanup(func() { _ = nmgr.Stop(context.Background()) })
	r.nc, err = nats.Connect(srv.ClientURL())
	require.NoError(t, err)
	t.Cleanup(r.nc.Close)
	return r
}

// stored is how many messages the inbound-events stream holds.
func (r *jsRig) stored(t *testing.T) uint64 {
	t.Helper()
	js, err := r.nc.JetStream()
	require.NoError(t, err)
	info, err := js.StreamInfo(r.stream)
	require.NoError(t, err)
	return info.State.Msgs
}

// publishTo makes the route's Publish write through the real writer, as main wires it.
func (r *jsRig) publishTo(t *testing.T, h *routeHarness) {
	h.route.Publish = func(source, tenant string, event *model.UnresolvedEvent, payload interface{}, dedupID string) error {
		ctx, msg, ok := benchInboundMessage(source, tenant, event, payload, dedupID)
		require.True(t, ok)
		return r.writer.WriteMessages(ctx, msg)
	}
}

// Both devices' events persist, and a repeat of one is suppressed by the broker.
func TestTwoDevicesWithTheSameAltIdBothPersistAndARepeatIsSuppressed(t *testing.T) {
	rig := newJsRig(t)
	alpha, beta := newRouteHarness(t), newRouteHarness(t)
	alphaPath, betaPath := aliasRoute(alpha, "custom.alpha"), aliasRoute(beta, "custom.beta")
	rig.publishTo(t, alpha)
	rig.publishTo(t, beta)

	postSameReading(t, alpha, alphaPath)
	postSameReading(t, beta, betaPath)
	require.Equal(t, uint64(2), rig.stored(t), "the second device's event was dropped as a duplicate of the first")

	postSameReading(t, alpha, alphaPath)
	require.Equal(t, uint64(2), rig.stored(t), "a retransmission by the same device was stored twice")
	postSameReading(t, beta, betaPath)
	require.Equal(t, uint64(2), rig.stored(t), "a retransmission by the same device was stored twice")
}
