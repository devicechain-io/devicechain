// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package resolvedevent

import (
	"testing"

	"github.com/devicechain-io/dc-microservice/eventenvelope"
	"github.com/devicechain-io/dc-microservice/eventtime"
	"github.com/stretchr/testify/require"
)

func TestIncarnationRoundTripsThroughLegacyAndBatchEnvelopes(t *testing.T) {
	for _, format := range []eventtime.WireFormat{eventtime.WireCompact, eventtime.WireLegacy} {
		e := legacyResolvedEvents()["measurement"]
		e.DeviceIncarnation = 23001
		e.Anchors[0].EntityId = 17001
		wire, err := MarshalResolvedEventAs(e, format)
		require.NoError(t, err)
		reference, err := referenceMarshalResolvedEvent(e, format)
		require.NoError(t, err)
		require.Equal(t, reference, wire)
		decoded, err := UnmarshalResolvedEvent(wire)
		require.NoError(t, err)
		require.Equal(t, e.DeviceIncarnation, decoded.DeviceIncarnation)
		require.Equal(t, e.Anchors, decoded.Anchors)
		batch, err := eventenvelope.Encode(eventenvelope.Header{Kind: eventenvelope.KindResolved, Tenant: "a"},
			[]eventenvelope.Event{{Index: 0, Bytes: wire}, {Index: 1, Bytes: wire}})
		require.NoError(t, err)
		r, err := eventenvelope.Parse(batch)
		require.NoError(t, err)
		for i := 0; i < r.Len(); i++ {
			member, err := UnmarshalEnvelopeMember(r, i)
			require.NoError(t, err)
			require.Equal(t, decoded, member)
			rebuilt, err := r.Member(i)
			require.NoError(t, err)
			require.Equal(t, wire, rebuilt)
		}
	}
}
