// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package eventenvelope_test

import (
	"testing"

	"github.com/devicechain-io/dc-microservice/eventenvelope"
	"github.com/devicechain-io/dc-microservice/eventtime"
	"github.com/devicechain-io/dc-microservice/resolvedevent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The interned fields sit in the middle of the event (11 and 12 between the payload and the
// scope memberships, 17 and 18 between the disabled flag and the gateway token), so putting
// them back is an insertion, not an append. An event that carries the fields on both sides
// of them is the shape that tells the two apart.
func TestInternedFieldsReturnToTheirPlace(t *testing.T) {
	e := realisticResolved(3)
	e.ScopeMemberships = []resolvedevent.GroupRef{{GroupToken: "fleet-a", Version: 4}}
	e.ExternalId = "ext-42"
	e.FenceSetVersion = 7
	e.SourceDeviceDisabled = true
	e.GatewayToken = "gw-9"
	events := []eventenvelope.Event{
		{Index: 0, Bytes: legacyBytes(t, e, eventtime.WireCompact)},
		{Index: 1, Bytes: legacyBytes(t, realisticResolved(4), eventtime.WireCompact)},
	}
	msg, err := eventenvelope.Encode(resolvedHeader(), events)
	require.NoError(t, err)
	r, err := eventenvelope.Parse(msg)
	require.NoError(t, err)
	got, err := r.Member(0)
	require.NoError(t, err)
	assert.Equal(t, events[0].Bytes, got)
	want, err := resolvedevent.UnmarshalResolvedEvent(events[0].Bytes)
	require.NoError(t, err)
	typed, err := resolvedevent.UnmarshalEnvelopeMember(r, 0)
	require.NoError(t, err)
	assert.Equal(t, want, typed)
}
