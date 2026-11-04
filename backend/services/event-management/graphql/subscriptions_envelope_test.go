// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/eventenvelope"
	"github.com/devicechain-io/dc-microservice/eventtype"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/resolvedevent"
	"github.com/stretchr/testify/require"
)

// A live tap reads an event envelope as the events inside it and a legacy message as itself.
func TestLiveTapReadsEnvelopesAsTheirEvents(t *testing.T) {
	at := time.Date(2026, 11, 3, 9, 0, 0, 0, time.UTC)
	var values [][]byte
	var members []eventenvelope.Event
	for i, dev := range []string{"d1", "d2", "d3"} {
		b, err := resolvedevent.MarshalResolvedEvent(&resolvedevent.ResolvedEvent{
			Source: "mqtt", SourceDeviceToken: dev, OccurredTime: at.Add(time.Duration(i) * time.Second),
			ProcessedTime: at, EventType: eventtype.Measurement,
			Payload: &resolvedevent.ResolvedMeasurementsPayload{},
		})
		require.NoError(t, err)
		values = append(values, b)
		members = append(members, eventenvelope.Event{Index: uint32(i), Bytes: b})
	}
	body, err := eventenvelope.Encode(eventenvelope.Header{Kind: eventenvelope.KindResolved, Tenant: "acme"}, members)
	require.NoError(t, err)

	env := messaging.NewConsumedMessage("i.acme.resolved-events", body, 0,
		map[string]string{eventenvelope.HeaderName: eventenvelope.HeaderValue}, nil)
	require.Equal(t, values, eventValues(env, "acme"))
	require.Nil(t, eventValues(env, "globex"), "an envelope for another tenant is skipped")

	legacy := messaging.NewConsumedMessage("i.acme.resolved-events", values[0], 0, nil, nil)
	require.Equal(t, [][]byte{values[0]}, eventValues(legacy, "acme"))
}
