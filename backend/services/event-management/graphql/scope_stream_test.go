// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"encoding/json"
	"fmt"
	esmodel "github.com/devicechain-io/dc-event-sources/model"
	"github.com/devicechain-io/dc-microservice/auth"
	mscfg "github.com/devicechain-io/dc-microservice/config"
	"github.com/devicechain-io/dc-microservice/core"
	gqlcore "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/resolvedevent"
	"github.com/devicechain-io/dc-microservice/streams"
	dctest "github.com/devicechain-io/dc-microservice/test"
	"github.com/gorilla/websocket"
	natsserver "github.com/nats-io/nats-server/v2/server"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"
)

// The live measurement stream under a scoped caller (customer users). The stream reads
// no table, so no storage callback filters it: the resolver's own anchor check is the
// whole of the scope here, and these tests are what stands behind it. Each drives the
// REAL resolver over a REAL NATS connection — the resolved event goes out on the wire
// exactly as device-management publishes it — so the anchors the filter reads are the
// ones the decoder produced, not a struct the test built.

const streamTenant = "acme-tenant"

// streamFixture is an embedded NATS server and a started NatsManager on it.
type streamFixture struct {
	nats     *messaging.NatsManager
	instance string
}

func startStreamFixture(t *testing.T) *streamFixture {
	t.Helper()
	srv, err := natsserver.NewServer(&natsserver.Options{
		Host:      "127.0.0.1",
		Port:      -1,
		JetStream: true,
		StoreDir:  dctest.JetStreamStoreDir(t),
	})
	require.NoError(t, err)
	go srv.Start()
	require.True(t, srv.ReadyForConnections(10*time.Second), "embedded nats server not ready")
	t.Cleanup(srv.Shutdown)
	u, err := url.Parse(srv.ClientURL())
	require.NoError(t, err)
	port, err := strconv.Atoi(u.Port())
	require.NoError(t, err)

	instance := fmt.Sprintf("em-scope-%d", time.Now().UnixNano())
	ms := &core.Microservice{InstanceId: instance, FunctionalArea: "event-management", Readiness: core.NewReadinessGate()}
	ms.InstanceConfiguration.Infrastructure.Nats = mscfg.NatsConfiguration{Hostname: u.Hostname(), Port: uint32(port)}
	nm := messaging.NewNatsManager(ms, core.NewNoOpLifecycleCallbacks(), func(*messaging.NatsManager) error { return nil })
	require.NoError(t, nm.Initialize(context.Background()))
	require.NoError(t, nm.Start(context.Background()))
	t.Cleanup(func() { _ = nm.Stop(context.Background()) })
	return &streamFixture{nats: nm, instance: instance}
}

// publishReading publishes one resolved measurement event for device, anchored to
// anchor ("customer:acme"), carrying a single reading named name with value value.
func (f *streamFixture) publishReading(t *testing.T, device, anchor, name, value string) {
	t.Helper()
	// An empty anchor is an UNASSIGNED device: the event carries no anchors at all.
	var anchors []resolvedevent.ResolvedAnchor
	if anchor != "" {
		kind, token, ok := strings.Cut(anchor, ":")
		require.True(t, ok)
		anchors = []resolvedevent.ResolvedAnchor{{AnchorType: kind, AnchorToken: token, RelationshipId: 1}}
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	encoded, err := resolvedevent.MarshalResolvedEvent(&resolvedevent.ResolvedEvent{
		Source:            "mqtt",
		SourceDeviceToken: device,
		Anchors:           anchors,
		OccurredTime:      now,
		ProcessedTime:     now,
		EventType:         esmodel.Measurement,
		Payload: &resolvedevent.ResolvedMeasurementsPayload{Entries: []resolvedevent.ResolvedMeasurementsEntry{{
			OccurredTime: now,
			Entries:      []resolvedevent.ResolvedMeasurementEntry{{Name: name, Value: value}},
		}}},
	})
	require.NoError(t, err)
	subject := messaging.ScopedSubject(f.instance, streamTenant, streams.ResolvedEvents)
	if messaging.IsPerDeviceSuffix(streams.ResolvedEvents) {
		subject += "." + device
	}
	require.NoError(t, f.nats.Conn().Publish(subject, encoded))
	require.NoError(t, f.nats.Conn().Flush())
}

// streamCtx is a request context for the stream resolver: tenant, verified access-token
// claims (scoped when anchors is non-nil), and the NATS manager the resolver reads.
func (f *streamFixture) streamCtx(ctx context.Context, anchors []string) context.Context {
	ctx = core.WithTenant(ctx, streamTenant)
	ctx = auth.WithClaims(ctx, &auth.Claims{
		Tenant:       streamTenant,
		Username:     "member@example.com",
		TokenType:    auth.TokenTypeAccess,
		Authorities:  []string{string(auth.EventRead)},
		ScopeAnchors: anchors,
	})
	return context.WithValue(ctx, gqlcore.ContextNatsKey, f.nats)
}

// nextName waits for the next streamed reading and returns its name.
func nextName(t *testing.T, ch <-chan *MeasurementEventResolver) string {
	t.Helper()
	select {
	case m, ok := <-ch:
		require.True(t, ok, "the stream closed before delivering a reading")
		return m.Name()
	case <-time.After(5 * time.Second):
		t.Fatal("no reading arrived within 5s")
		return ""
	}
}

// Acme's scoped subscriber receives the Acme-anchored reading and never the Globex one.
// The Globex reading (and one anchored to a GROUP named acme) is published FIRST, so a filter that let it through would hand it
// over as the first reading — the assertion reads the order, not just the presence.
// The unscoped control on the same feed receives both, in publish order: what the
// scoped subscriber missed was filtered, not lost.
func TestAScopedSubscriberReceivesOnlyItsCustomersReadings(t *testing.T) {
	f := startStreamFixture(t)
	r := &SchemaResolver{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	scoped, err := r.MeasurementStream(f.streamCtx(ctx, []string{"customer:acme"}),
		struct{ DeviceToken, Name *string }{})
	require.NoError(t, err, "a scoped member with event:read must be admitted to the stream")
	unscoped, err := r.MeasurementStream(f.streamCtx(ctx, nil), struct{ DeviceToken, Name *string }{})
	require.NoError(t, err)

	f.publishReading(t, "shared-device", "customer:globex", "globex-reading", "2")
	// A GROUP that happens to share the customer's token is a different anchor: the kind
	// is part of what is compared, not decoration.
	f.publishReading(t, "shared-device", "group:acme", "group-reading", "3")
	// An UNASSIGNED device's reading carries no anchors and belongs to no customer: it
	// matches no scope, as its stored rows match none. A filter that read "no anchors" as
	// "nobody to hide it from" would hand every unassigned device to every customer.
	f.publishReading(t, "unassigned-device", "", "unanchored-reading", "4")
	f.publishReading(t, "shared-device", "customer:acme", "acme-reading", "1")

	assert.Equal(t, "acme-reading", nextName(t, scoped),
		"Acme's subscriber must receive Acme's reading and must not have received Globex's, group:acme's or the unanchored one first")
	assert.Equal(t, "globex-reading", nextName(t, unscoped), "control: the unscoped subscriber sees Globex's reading")
	assert.Equal(t, "group-reading", nextName(t, unscoped), "control: ...then the group's")
	assert.Equal(t, "unanchored-reading", nextName(t, unscoped), "control: ...then the unassigned device's")
	assert.Equal(t, "acme-reading", nextName(t, unscoped), "control: ...and then Acme's")
}

// A device outside the scope is not refused at subscribe time — event-management cannot
// see who owns a device now — and the stream simply stays silent for it.
func TestAScopedStreamForAnotherCustomersDeviceIsSilent(t *testing.T) {
	f := startStreamFixture(t)
	r := &SchemaResolver{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	device := "globex-device"
	scoped, err := r.MeasurementStream(f.streamCtx(ctx, []string{"customer:acme"}),
		struct{ DeviceToken, Name *string }{DeviceToken: &device})
	require.NoError(t, err, "no subscribe-time ownership check: the subscription is accepted")

	f.publishReading(t, device, "customer:globex", "globex-reading", "2")
	select {
	case m := <-scoped:
		t.Fatalf("a reading of a device outside the scope was delivered: %q", m.Name())
	case <-time.After(500 * time.Millisecond):
	}
}

// The same rule over the REAL WebSocket transport: a scoped token is parsed by the
// subscription handler's own validator, its claims ride connection_init into the
// resolver's context, and Acme's subscriber gets Acme's reading first. This is the path
// a customer's console takes, so it proves the scope survives the transport rather than
// only a context the test assembled.
func TestAScopedSubscriberOverTheWebSocket(t *testing.T) {
	f := startStreamFixture(t)
	key, err := auth.GenerateKeyPair()
	require.NoError(t, err)
	iss := auth.NewIssuer(key, "test", time.Minute, time.Hour)
	gate := core.NewReadinessGate()
	gate.MarkReady(auth.NewValidator(&key.PublicKey))

	schema := gqlcore.MustParseSchema(SchemaContent, &SchemaResolver{})
	h := gqlcore.NewSubscriptionHandler(schema, map[gqlcore.ContextKey]interface{}{gqlcore.ContextNatsKey: f.nats}, gate)
	srv := httptest.NewServer(h)
	defer srv.Close()

	scope, err := auth.NewScope([]string{"customer:acme"})
	require.NoError(t, err)
	scopedTok, err := iss.IssueScopedTenantAccess(streamTenant, "member@acme.example", nil,
		[]string{string(auth.EventRead)}, scope, "j-scoped")
	require.NoError(t, err)
	plainTok, err := iss.IssueTenantAccess(streamTenant, "admin@example.com", nil,
		[]string{string(auth.EventRead)}, false, "j-plain")
	require.NoError(t, err)

	scoped := dialStream(t, srv.URL, scopedTok.Token)
	unscoped := dialStream(t, srv.URL, plainTok.Token)

	f.publishReading(t, "shared-device", "customer:globex", "globex-reading", "2")
	f.publishReading(t, "shared-device", "customer:acme", "acme-reading", "1")

	assert.Equal(t, "acme-reading", wsNextName(t, scoped),
		"over the socket, Acme's subscriber must receive Acme's reading and not Globex's")
	assert.Equal(t, "globex-reading", wsNextName(t, unscoped), "control: the unscoped socket sees Globex's reading")
	assert.Equal(t, "acme-reading", wsNextName(t, unscoped), "control: ...and then Acme's")
}

type wsFrame struct {
	ID      string          `json:"id,omitempty"`
	Type    string          `json:"type"`
	Payload json.RawMessage `json:"payload,omitempty"`
}

// dialStream opens a graphql-transport-ws connection with token, subscribes to
// measurementStream and returns once the server's live feed for it is open.
func dialStream(t *testing.T, base, token string) *websocket.Conn {
	t.Helper()
	dialer := websocket.Dialer{Subprotocols: []string{"graphql-transport-ws"}}
	conn, _, err := dialer.Dial("ws"+strings.TrimPrefix(base, "http"), nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))

	init, _ := json.Marshal(map[string]string{"Authorization": "Bearer " + token})
	require.NoError(t, conn.WriteJSON(wsFrame{Type: "connection_init", Payload: init}))
	var ack wsFrame
	require.NoError(t, conn.ReadJSON(&ack))
	require.Equal(t, "connection_ack", ack.Type, "payload=%s", ack.Payload)

	sub, _ := json.Marshal(map[string]any{"query": "subscription { measurementStream { name deviceToken } }"})
	require.NoError(t, conn.WriteJSON(wsFrame{ID: "1", Type: "subscribe", Payload: sub}))
	// The read loop handles frames in order, and handles a subscribe by calling the
	// resolver — which opens and confirms its NATS feed — before it reads the next frame.
	// So the pong to a ping sent after the subscribe means the feed is open: nothing
	// published from here on can be missed for want of a subscription.
	require.NoError(t, conn.WriteJSON(wsFrame{Type: "ping"}))
	var pong wsFrame
	require.NoError(t, conn.ReadJSON(&pong))
	require.Equal(t, "pong", pong.Type, "payload=%s", pong.Payload)
	return conn
}

// wsNextName reads the next `next` frame and returns the streamed reading's name.
func wsNextName(t *testing.T, conn *websocket.Conn) string {
	t.Helper()
	var msg wsFrame
	require.NoError(t, conn.ReadJSON(&msg))
	require.Equal(t, "next", msg.Type, "payload=%s", msg.Payload)
	var body struct {
		Data struct {
			MeasurementStream struct {
				Name string `json:"name"`
			} `json:"measurementStream"`
		} `json:"data"`
		Errors []json.RawMessage `json:"errors"`
	}
	require.NoError(t, json.Unmarshal(msg.Payload, &body))
	require.Empty(t, body.Errors)
	return body.Data.MeasurementStream.Name
}
