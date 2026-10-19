// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"encoding/hex"
	emtest "github.com/devicechain-io/dc-event-management/test"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/eventtime"
	"github.com/devicechain-io/dc-microservice/resolvedevent"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"testing"
	"time"
)

// legacyResolvedMeasurementHex is a resolved measurement event as device-management wrote it
// before each instant became a count of nanoseconds, byte for byte (origin/main 9b680acf6;
// the same message core/resolvedevent/legacy_wire_test.go decodes). Its envelope
// carries the device's +02:00 offset. Such messages are on the retained resolved stream
// across an upgrade, and are what a redelivery straddling the upgrade replays.
const legacyResolvedMeasurementHex = "0a04687474701a1164657669636570756c73652d303030303122150a08637573746f6d6572120761636d652d636f18053a23323032362d31302d31365431313a33303a31352e3132333435363738392b30323a3030421e323032362d31302d31365430393a33303a31352e3132353030303030375a4802523f0a3d0a1b0a0474656d70120432312e351801220343656c2a06444f55424c45121e323032362d31302d31365430393a33303a31342e3938373635343332315a5a0b64657669636570756c7365620770756c7365403372156578742d64657669636570756c73652d3030303031"

// The historian persists a legacy message and the same event written in the current format
// IDENTICALLY: the same rows, at the same instants, under the same content-derived event id.
// The id is what makes this matter. It hashes the occurred time to the nanosecond, so an
// event resolved once before the upgrade and once after (an inbound redelivery straddling
// it) must derive one id from both encodings, or it is stored twice.
func TestALegacyResolvedMessagePersistsAsTheCurrentFormatDoes(t *testing.T) {
	legacy, err := hex.DecodeString(legacyResolvedMeasurementHex)
	require.NoError(t, err)
	decoded, err := resolvedevent.UnmarshalResolvedEvent(legacy)
	require.NoError(t, err)
	current, err := resolvedevent.MarshalResolvedEventAs(decoded, eventtime.WireCompact)
	require.NoError(t, err)
	require.Less(t, len(current), len(legacy))

	persist := func(encoded []byte) *capturingEntryTimeApi {
		event, err := resolvedevent.UnmarshalResolvedEvent(encoded)
		require.NoError(t, err)
		api := &capturingEntryTimeApi{MockApi: new(emtest.MockApi)}
		api.Mock.On("CreateEventAnchors", mock.Anything, mock.Anything).Return(nil)
		worker := &EventPersistenceWorker{WorkerId: 1, Api: api}
		_, err = worker.PersistEvent(core.WithTenant(context.Background(), "acme"), *event)
		require.NoError(t, err)
		require.Len(t, api.measurements, 1)
		return api
	}
	fromLegacy, fromCurrent := persist(legacy), persist(current)

	require.Equal(t, fromLegacy.measurements, fromCurrent.measurements)

	// And this release writing the LEGACY format (its default) persists the same rows under
	// the same id: a producer's format choice never moves an event's identity, whichever way
	// it is set during a rollout.
	rewritten, err := resolvedevent.MarshalResolvedEventAs(decoded, eventtime.WireLegacy)
	require.NoError(t, err)
	require.Equal(t, fromLegacy.measurements, persist(rewritten).measurements)
	got := fromLegacy.measurements[0]
	// The id the historian derived for this very message BEFORE the upgrade (origin/main
	// 9b680acf6, the same tenant): the change moved no event's identity.
	require.Equal(t, "c848d491de32cb0ad86ea243ffbaa5c316e0808be6f7c229015a407ca0a318fa", hex.EncodeToString(got.EventId))
	require.True(t, got.OccurredTime.Equal(time.Date(2026, 10, 16, 9, 30, 15, 123456789, time.UTC)), "%s", got.OccurredTime)
	require.True(t, got.EntryOccurredTime.Equal(time.Date(2026, 10, 16, 9, 30, 14, 987654321, time.UTC)), "%s", got.EntryOccurredTime)
}
