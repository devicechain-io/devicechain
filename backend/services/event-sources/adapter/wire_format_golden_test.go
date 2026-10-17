// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package adapter

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/devicechain-io/dc-microservice/eventtime"
)

// The goldens in lwm2m_emit_golden_test.go and emit_split_test.go run with the process set
// to LEGACY, and still hold the digests captured before the compact format existed: that is
// the proof that a producer set to wireFormat: legacy writes, byte for byte, what the
// releases before the compact reader wrote, through the whole Emit/Ingest path a transport
// adapter (LwM2M, Sparkplug) takes.
//
// These are the same scenarios with the process set to COMPACT, the default, and the way an
// operator's infrastructure.messaging.wireFormat: compact sets it. The digests are the ones captured
// when the compact format was introduced; every dedup id is the legacy one, unchanged,
// because the dedup id is derived from the event, never from its encoding, which is what
// lets a redelivery that straddles a format change still be dropped as a duplicate.

// inProducerWireFormat runs the test with this process writing format, and restores the
// default after. The adapter's tests are not parallel, so nothing else observes the change.
func inProducerWireFormat(t *testing.T, format eventtime.WireFormat) {
	t.Helper()
	require.NoError(t, eventtime.SetProducerWireFormat(format))
	t.Cleanup(func() { require.NoError(t, eventtime.SetProducerWireFormat(eventtime.DefaultWireFormat)) })
}

func TestGoldenLwm2mEmitWritesInTheCompactFormat(t *testing.T) {
	inProducerWireFormat(t, eventtime.WireCompact)
	w := &fakeWriter{}
	e := NewEmitter(w, fixedNow, "lw", true)
	in := numberedSamples(300)
	in = append(in, Sample{Name: "ancient", Value: 1, Time: 1})
	tooOld, err := e.Emit(context.Background(), "acme", "lwm2m-src", "lw-dev-1", in)
	require.NoError(t, err)
	assert.Equal(t, 1, tooOld)
	require.Len(t, w.msgs, 2)
	assert.Equal(t, "lwih3ffszln5b9", w.msgs[0].DedupID)
	assert.Equal(t, "lw3v4hl6gazr6qt", w.msgs[1].DedupID)
	assert.Equal(t, "e39c29945c8fbb64c3ce7501f5da63b4c55d554455cdba94e68866cd60fc3c3f", writtenDigest(t, w))
}

func TestGoldenLwm2mEmitPresenceWritesInTheCompactFormat(t *testing.T) {
	inProducerWireFormat(t, eventtime.WireCompact)
	w := &fakeWriter{}
	e := NewEmitter(w, fixedNow, "lw", true)
	for _, connected := range []bool{true, false} {
		require.NoError(t, e.EmitPresence(context.Background(), "acme", "lwm2m-src", "lw-dev-1", PresenceEvent{
			ExternalId: "ep-1", Connected: connected, Reason: "registration",
			SessionId: 1_700_000_000_123456789, OccurredAt: time.Unix(1_700_000_000, 0),
		}))
	}
	require.Len(t, w.msgs, 2)
	assert.Equal(t, "lw2qmgm3hd1ei3w", w.msgs[0].DedupID)
	assert.Equal(t, "lw2qmq169wjn4ut", w.msgs[1].DedupID)
	assert.Equal(t, "85b768b46ab8468cf2622db4e6ef110438ff6058be1843ec2a5ebd01e76b1863", writtenDigest(t, w))
}

func TestABatchIsPublishedInTheCompactFormatUnderTheSameDedupId(t *testing.T) {
	for _, c := range []struct {
		n              int
		dedup          string
		legacy, compct string
	}{
		{3, "spy2068tpcdekh",
			"911615560d90773da4fc827343a68d654d47373f83a19a3e0c0aa1a864df7ea3",
			"25b5f4f830932306148e1714deda1098df630c6d8c035c38841e7c200b74cc66"},
		{256, "sp3ksr7wzo2aawh",
			"a96d189266f2686ce11a0fa76f2bda82634dc93fa5add600353e2e4432c10817",
			"77453a1cc7c517604814d0745b461907a8eb984bcd200aa06df24be0208bde69"},
	} {
		for format, want := range map[eventtime.WireFormat]string{eventtime.WireLegacy: c.legacy, eventtime.WireCompact: c.compct} {
			inProducerWireFormat(t, format)
			w := &fakeWriter{}
			e := NewEmitter(w, fixedNow, "sp", true)
			require.NoError(t, emitErr(e, context.Background(), "acme", "src", "dev-1", numberedSamples(c.n)))
			require.Len(t, w.msgs, 1)
			assert.Equal(t, c.dedup, w.msgs[0].DedupID, "%s: %d samples", format, c.n)
			sum := sha256.Sum256(w.msgs[0].Value)
			assert.Equal(t, want, hex.EncodeToString(sum[:]), "%s: bytes of a %d-sample batch", format, c.n)
		}
	}
}
