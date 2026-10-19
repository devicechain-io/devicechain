// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"database/sql"
	"errors"
	"github.com/devicechain-io/dc-microservice/resolvedevent"
	"testing"
	"time"

	dmconfig "github.com/devicechain-io/dc-device-management/config"
	dmmodel "github.com/devicechain-io/dc-device-management/model"
	dmprocessor "github.com/devicechain-io/dc-device-management/processor"
	"github.com/devicechain-io/dc-device-state/model"
	"github.com/devicechain-io/dc-event-sources/adapter"
	esmodel "github.com/devicechain-io/dc-event-sources/model"
	esproto "github.com/devicechain-io/dc-event-sources/proto"
	"github.com/devicechain-io/dc-microservice/core"
	dcgraphql "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-microservice/rdb"
	putest "github.com/devicechain-io/dc-microservice/rdb/partialupdatetest"
)

// presenceProducer is one transport that asserts presence, by the source it writes under,
// the external id its devices carry and the reasons its transitions name.
type presenceProducer struct {
	name, source, externalId, connectReason, disconnectReason string
}

// The three producers whose devices can be connected when an operator disables them: the
// broker-asserted MQTT tap, the LwM2M server (whose registration of a disabled device is
// ended at its next update), and the Sparkplug host (an edge node's death takes its devices
// with it).
var presenceProducers = []presenceProducer{
	{name: "broker-asserted MQTT", source: mqttTestSource, connectReason: "connect", disconnectReason: "disconnect"},
	{name: "LwM2M", source: "lwm2m", externalId: "ep-001", connectReason: "register", disconnectReason: "disabled"},
	{name: "Sparkplug", source: "sparkplug", externalId: "plant/edge-1/sensor-001", connectReason: "dbirth", disconnectReason: "ndeath-will"},
}

// presenceChain carries one presence transition from a producer's struct to the projection's
// row through every real step in between: the emitter, the event-sources wire, the REAL
// resolver against a real device registry (so a disabled device is refused or let through
// exactly as it is in production), the device-management wire, and the projection's mapper
// and write.
type presenceChain struct {
	t          *testing.T
	ctx        context.Context
	producer   presenceProducer
	registry   *dmmodel.Api
	resolver   *dmprocessor.EventResolver
	projection *model.Api
}

const disabledTestDevice = "sensor-001"

func newPresenceChain(t *testing.T, producer presenceProducer) *presenceChain {
	t.Helper()
	ctx := core.WithTenant(context.Background(), "acme")
	db := putest.NewSQLiteDB(t, &dmmodel.Device{}, &dmmodel.DeviceType{}, &dmmodel.DeviceProfile{},
		&dmmodel.DeviceProfileVersion{}, &dmmodel.MetricDefinition{}, &dmmodel.CommandDefinition{},
		&dmmodel.DetectionRule{}, &dmmodel.DetectionRuleScopeRef{}, &dmmodel.EntityRelationship{},
		&dmmodel.EntityRelationshipType{}, &dmmodel.GeoFenceSetVersion{}, &dmmodel.EntityGroupFacetRef{},
		&dmmodel.EntityGroupMembership{}, &dmmodel.DeviceCredential{})
	dtype := &dmmodel.DeviceType{}
	dtype.Token = "dt"
	if err := db.WithContext(ctx).Create(dtype).Error; err != nil {
		t.Fatalf("seed device type: %v", err)
	}
	device := &dmmodel.Device{DeviceTypeId: dtype.ID}
	device.Token = disabledTestDevice
	if producer.externalId != "" {
		device.ExternalId = sql.NullString{String: producer.externalId, Valid: true}
	}
	if err := db.WithContext(ctx).Create(device).Error; err != nil {
		t.Fatalf("seed device: %v", err)
	}
	registry := dmmodel.NewApi(&rdb.RdbManager{Database: db})
	return &presenceChain{
		t: t, ctx: ctx, producer: producer, registry: registry,
		resolver: dmprocessor.NewEventResolver(1, registry, dmconfig.AuthModeDisabled, dmprocessor.EventTimePolicy{},
			nil, nil, nil, nil, nil, nil),
		projection: newProjection(t),
	}
}

// resolve carries one transition through the emitter, the event-sources wire and the real
// resolver, returning what the resolver answered.
func (c *presenceChain) resolve(connected bool, session uint64, at time.Time) ([]dmprocessor.EventResolutionResults, error) {
	c.t.Helper()
	writer := &captureWriter{}
	reason := c.producer.disconnectReason
	if connected {
		reason = c.producer.connectReason
	}
	emitter := adapter.NewEmitter(writer, func() time.Time { return at }, "mq", false)
	if err := emitter.EmitPresence(c.ctx, "acme", c.producer.source, disabledTestDevice, adapter.PresenceEvent{
		ExternalId: c.producer.externalId, Connected: connected, Reason: reason, SessionId: session, OccurredAt: at,
	}); err != nil {
		c.t.Fatalf("emit: %v", err)
	}
	if len(writer.msgs) != 1 {
		c.t.Fatalf("emitted %d messages, want 1", len(writer.msgs))
	}
	unresolved, err := esproto.UnmarshalUnresolvedEvent(writer.msgs[0].Value)
	if err != nil {
		c.t.Fatalf("unresolved decode: %v", err)
	}
	results, _, err := c.resolver.ResolveEvent(c.ctx, unresolved)
	return results, err
}

// apply carries one transition the whole way into the projection and returns the row.
func (c *presenceChain) apply(connected bool, session uint64, at time.Time) (*model.DeviceState, *resolvedevent.ResolvedEvent) {
	c.t.Helper()
	results, err := c.resolve(connected, session, at)
	if err != nil {
		c.t.Fatalf("%s: the resolver refused a presence transition (connected=%v): %v", c.producer.name, connected, err)
	}
	if len(results) != 1 {
		c.t.Fatalf("%s: resolved %d events, want 1", c.producer.name, len(results))
	}
	encoded, err := resolvedevent.MarshalResolvedEvent(results[0].Resolved)
	if err != nil {
		c.t.Fatalf("resolved encode: %v", err)
	}
	resolved, err := resolvedevent.UnmarshalResolvedEvent(encoded)
	if err != nil {
		c.t.Fatalf("resolved decode: %v", err)
	}
	pt, err := presenceTransitionFor(resolved)
	if err != nil || pt == nil {
		c.t.Fatalf("no presence transition from the resolved event: %v", err)
	}
	state, err := c.projection.MergeDeviceState(c.ctx, disabledTestDevice, at, pt,
		model.DeviceIdentity{ExternalId: resolved.ExternalId, Source: resolved.Source})
	if err != nil {
		c.t.Fatalf("merge: %v", err)
	}
	return state, resolved
}

func (c *presenceChain) setEnabled(enabled bool) {
	c.t.Helper()
	if _, err := c.registry.UpdateDevice(c.ctx, disabledTestDevice,
		&dmmodel.DeviceUpdateRequest{Enabled: dcgraphql.OptionalBoolOf(enabled)}); err != nil {
		c.t.Fatalf("set enabled=%v: %v", enabled, err)
	}
}

// 🔴 DISABLING A CONNECTED DEVICE MUST NOT FREEZE IT AT CONNECTED. Disabling a device does not
// close a connection it already has; the connection stays open until it ends. When it ends,
// the transport reports the DISCONNECTED — and the resolver used to refuse it with every other
// event of a disabled device, so the projection never heard. An asserted row takes Active only
// from a transition and the inactivity sweep skips it, so the device read CONNECTED for as long
// as it stayed disabled. Driven for each producer that asserts presence, through the real
// resolver.
func TestADisabledDevicesConnectionEndingShowsInItsPresence(t *testing.T) {
	for _, producer := range presenceProducers {
		t.Run(producer.name, func(t *testing.T) {
			c := newPresenceChain(t, producer)
			session := uint64(1786552664076882575)
			connectedAt := time.Now().UTC().Add(-time.Minute)

			state, resolved := c.apply(true, session, connectedAt)
			if !state.Active {
				t.Fatal("the control is not live: the device did not read connected while enabled")
			}
			if resolved.SourceDeviceDisabled {
				t.Fatal("an enabled device's transition was marked as a disabled device's")
			}

			c.setEnabled(false)
			state, resolved = c.apply(false, session, connectedAt.Add(30*time.Second))
			if state.Active {
				t.Fatal("the disabled device still reads CONNECTED after its connection ended")
			}
			if !resolved.SourceDeviceDisabled {
				t.Error("the disabled device's transition was not marked, so detection would raise an alarm for it")
			}

			// Its own data is still refused while it is disabled.
			now := time.Now().UTC()
			_, _, err := c.resolver.ResolveEvent(c.ctx, &esmodel.UnresolvedEvent{
				Source: producer.source, Device: disabledTestDevice, EventType: esmodel.Measurement,
				OccurredTime: now, ProcessedTime: now,
				Payload: &esmodel.UnresolvedMeasurementsPayload{
					Entries: []esmodel.UnresolvedMeasurementsEntry{{Measurements: map[string]string{"temp": "21.5"}}},
				},
			})
			if !errors.Is(err, dmmodel.ErrDeviceDisabled) {
				t.Fatalf("a disabled device's measurement was not refused as disabled: %v", err)
			}
		})
	}
}
