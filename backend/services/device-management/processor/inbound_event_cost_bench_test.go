// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"fmt"
	"github.com/devicechain-io/dc-microservice/resolvedevent"
	"testing"
	"time"

	"github.com/devicechain-io/dc-device-management/config"
	"github.com/devicechain-io/dc-device-management/model"
	esmodel "github.com/devicechain-io/dc-event-sources/model"
	esproto "github.com/devicechain-io/dc-event-sources/proto"
	"github.com/devicechain-io/dc-microservice/eventtime"
	"github.com/devicechain-io/dc-microservice/eventtype"
)

// BenchmarkInboundEventCost is the in-process CPU device-management spends on one inbound
// measurement event, split by stage, so the share of each stage can be read off directly:
//
//	decode   the unresolved event off the wire (esproto.UnmarshalUnresolvedEvent)
//	decode-payload  of which the measurements payload alone
//	         (esproto.UnmarshalPayloadForMeasurementsEvent)
//	resolve  resolution, every lookup a warm in-process cache hit, under required device
//	         authentication with a sealed access token (the default mode)
//	encode   the resolved event onto the wire (resolvedevent.MarshalResolvedEvent)
//	all      the three in sequence, as the resolver and the outbound loop run them
//
// declared= is the number of metrics the device's published profile declares, which the
// resolver's cached profile carries; readings= is the number of measurements the event
// carries. Nothing the broker costs is included: the read, the ack and the publish of the
// resolved event are network work this benchmark does not see, and on a cluster they are
// most of an event's cost.
//
// Run with: go test ./processor -run '^$' -bench InboundEventCost -benchmem -count 10 -cpu 1
func BenchmarkInboundEventCost(b *testing.B) {
	for _, shape := range []struct{ declared, readings int }{{1, 1}, {21, 1}, {21, 10}} {
		readings := shape.readings
		rig := newBenchResolveRig(b, shape.declared-1, nil)
		token := "tok-bench"
		if _, err := rig.api.CreateDeviceCredential(rig.ctx, &model.DeviceCredentialCreateRequest{
			Token: "c-1", DeviceToken: "dev", CredentialType: string(model.CredentialAccessToken),
			CredentialId: token, Enabled: true,
		}); err != nil {
			b.Fatal(err)
		}
		rez := NewEventResolver(1, rig.capi, config.AuthModeRequired, EventTimePolicy{}, nil, nil, nil, nil, nil, nil)
		rez.SecretTransit = testSecretTransit(b)

		values := map[string]string{"temp": "21.5"}
		for i := 0; len(values) < readings; i++ {
			values[fmt.Sprintf("m%d", i)] = fmt.Sprintf("%d.25", i)
		}
		now := time.Now().UTC()
		event := &esmodel.UnresolvedEvent{
			Source:        "http",
			Device:        "dev",
			OccurredTime:  now,
			ProcessedTime: now,
			EventType:     eventtype.Measurement,
			Payload: &esmodel.UnresolvedMeasurementsPayload{
				// Timed per entry, as the simulator's load generator sends it.
				Entries: []esmodel.UnresolvedMeasurementsEntry{{Measurements: values, OccurredTime: &now}},
			},
		}
		presentSealedToken(b, rig.ctx, event, string(model.CredentialAccessToken), token)
		wire, err := esproto.MarshalUnresolvedEvent(event)
		if err != nil {
			b.Fatal(err)
		}
		payloadWire, err := esproto.MarshalPayloadForMeasurementsEvent(
			event.Payload.(*esmodel.UnresolvedMeasurementsPayload), eventtime.ProducerWireFormat())
		if err != nil {
			b.Fatal(err)
		}
		decoded, err := esproto.UnmarshalUnresolvedEvent(wire)
		if err != nil {
			b.Fatal(err)
		}
		results, _, err := rez.ResolveEvent(rig.ctx, decoded) // warm every cache
		if err != nil || len(results) != 1 {
			b.Fatalf("warm resolve: %d results, err %v", len(results), err)
		}
		resolved := results[0].Resolved

		run := func(name string, op func() error) {
			b.Run(fmt.Sprintf("declared=%d/readings=%d/%s", shape.declared, readings, name), func(b *testing.B) {
				b.ReportAllocs()
				for i := 0; i < b.N; i++ {
					if err := op(); err != nil {
						b.Fatal(err)
					}
				}
			})
		}
		run("decode", func() error {
			_, err := esproto.UnmarshalUnresolvedEvent(wire)
			return err
		})
		run("decode-payload", func() error {
			_, err := esproto.UnmarshalPayloadForMeasurementsEvent(payloadWire)
			return err
		})
		run("resolve", func() error {
			_, _, err := rez.ResolveEvent(rig.ctx, decoded)
			return err
		})
		run("encode", func() error {
			_, err := resolvedevent.MarshalResolvedEvent(resolved)
			return err
		})
		run("all", func() error {
			ev, err := esproto.UnmarshalUnresolvedEvent(wire)
			if err != nil {
				return err
			}
			out, _, err := rez.ResolveEvent(rig.ctx, ev)
			if err != nil {
				return err
			}
			for _, r := range out {
				if _, err := resolvedevent.MarshalResolvedEvent(r.Resolved); err != nil {
					return err
				}
			}
			return nil
		})
	}
}
