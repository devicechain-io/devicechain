// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package proto

import (
	"testing"
	"time"

	esmodel "github.com/devicechain-io/dc-event-sources/model"
	esproto "github.com/devicechain-io/dc-event-sources/proto"
	"github.com/devicechain-io/dc-microservice/eventtime"
	"github.com/devicechain-io/dc-microservice/resolvedevent"
	"github.com/stretchr/testify/require"
)

// This test stays with device-management rather than moving with the resolved-event wire
// contract: it crosses the ingest hop (event-sources' inbound codec) and the resolved hop
// (resolvedevent), and core cannot import event-sources.

// 🔴 The instant that broke the compact format as first written: a device's
// 9999-12-31T23:59:59-05:00 is the year 10000 in UTC, which RFC 3339 cannot carry. It
// crosses BOTH hops: the ingest source writes it, device-management reads it back and writes
// it again on the resolved event. Written in UTC at either hop it is unparseable and the
// event is lost (dead-lettered by device-management, dropped by every resolved consumer).
func TestAnInstantPastTheUTCYearRangeCrossesBothHops(t *testing.T) {
	late := time.Date(9999, 12, 31, 23, 59, 59, 999999999, time.FixedZone("", -5*3600))
	for _, inFormat := range []eventtime.WireFormat{eventtime.WireLegacy, eventtime.WireCompact} {
		inbound, err := esproto.MarshalUnresolvedEventAs(&esmodel.UnresolvedEvent{
			Source: "http", Device: "d", OccurredTime: late, ProcessedTime: late, EventType: esmodel.Alert,
			Payload: &esmodel.UnresolvedAlertsPayload{Entries: []esmodel.UnresolvedAlertEntry{{Type: "t", OccurredTime: &late}}},
		}, inFormat)
		require.NoError(t, err)
		unrez, err := esproto.UnmarshalUnresolvedEvent(inbound)
		require.NoError(t, err, "%s: the resolver could not read the inbound event", inFormat)
		entry := unrez.Payload.(*esmodel.UnresolvedAlertsPayload).Entries[0].OccurredTime

		for _, outFormat := range []eventtime.WireFormat{eventtime.WireLegacy, eventtime.WireCompact} {
			resolved, err := resolvedevent.MarshalResolvedEventAs(&resolvedevent.ResolvedEvent{
				Source: "http", SourceDeviceToken: "d", OccurredTime: unrez.OccurredTime, ProcessedTime: unrez.ProcessedTime,
				EventType: esmodel.Alert,
				Payload:   &resolvedevent.ResolvedAlertsPayload{Entries: []resolvedevent.ResolvedAlertEntry{{Type: "t", OccurredTime: *entry}}},
			}, outFormat)
			require.NoError(t, err)
			got, err := resolvedevent.UnmarshalResolvedEvent(resolved)
			require.NoError(t, err, "%s -> %s: a resolved consumer could not read it", inFormat, outFormat)
			require.True(t, got.OccurredTime.Equal(late), "%s -> %s", inFormat, outFormat)
			require.True(t, got.Payload.(*resolvedevent.ResolvedAlertsPayload).Entries[0].OccurredTime.Equal(late))
		}
	}
}
