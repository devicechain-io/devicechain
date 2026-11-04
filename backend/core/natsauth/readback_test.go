// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package natsauth

import (
	"testing"

	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/streams"
	"github.com/stretchr/testify/require"
)

// A writer that settles envelope rounds reads the stored message a duplicate PubAck names back by
// sequence, from the stream it writes, so the stream table's ReadBackWriters must reach the grant:
// without it the first duplicate answer is a permission error and the round's sources are left
// unacknowledged for good. The expected set is DERIVED from the table, and a writer the table does
// not list (and that does not read the stream) must NOT hold the read, so the grant cannot widen
// unnoticed either.
//
// GUARD: kills a natsauth that stops granting from ReadBackWriters, and one that grants the read to
// every writer.
func TestARoundSettlingWriterCanReadItsStreamBackAndOtherWritersCannot(t *testing.T) {
	all := allAreaPermissions(t)
	listed := 0
	for _, st := range streams.All {
		if st.Shape == streams.ShapeAdvisory {
			continue
		}
		name := messaging.StreamName(permInstance, st.Suffix)
		for area, p := range all {
			if !streams.Writes(st.Suffix, area) {
				continue
			}
			readsAsReader := false
			for _, r := range streams.ReadersOf(st.Suffix) {
				if r.Area == area {
					readsAsReader = true
				}
			}
			if streams.ReadsBack(st.Suffix, area) {
				listed++
				require.True(t, p.publishes("$JS.API.STREAM.MSG.GET."+name), "%s settles rounds on %s but cannot STREAM.MSG.GET it", area, st.Suffix)
				require.True(t, p.publishes("$JS.API.DIRECT.GET."+name), "%s settles rounds on %s but cannot DIRECT.GET it", area, st.Suffix)
			} else if !readsAsReader {
				require.False(t, p.publishes("$JS.API.STREAM.MSG.GET."+name), "%s is a plain writer of %s yet can read it back", area, st.Suffix)
			}
		}
	}
	require.NotZero(t, listed, "no stream declares a round-settling writer: the table lost its ReadBackWriters")
}

// dcctl's downgrade guard and its off-marks read the oldest and the newest messages of the streams
// that carry envelopes as the broker administrator. A broker drops a request its login may not
// make without answering, so a missing grant is a timeout far from the cause. The expected set is
// DERIVED from the table (the streams that declare a round-settling writer), and the grant must
// not spread to a stream that carries no envelope.
//
// GUARD: kills an administrator without STREAM.MSG.GET / DIRECT.GET on an envelope stream, and one
// granted the read on every stream.
func TestTheAdministratorCanReadTheEnvelopeStreamsAndNoOthers(t *testing.T) {
	p, err := AdminPermissions(permInstance)
	require.NoError(t, err)
	carriers := streams.EnvelopeCarriers()
	require.ElementsMatch(t, []string{streams.InboundEvents, streams.ResolvedEvents}, carriers,
		"the envelope-carrying streams changed: the guard's read grant follows them, so decide it on purpose")
	isCarrier := map[string]bool{}
	for _, c := range carriers {
		isCarrier[c] = true
	}
	for _, st := range streams.All {
		name := messaging.StreamName(permInstance, st.Suffix)
		for _, verb := range []string{"STREAM.MSG.GET.", "DIRECT.GET."} {
			got := p.publishes("$JS.API." + verb + name)
			if isCarrier[st.Suffix] {
				require.True(t, got, "the administrator cannot %s%s, so dcctl's envelope guard times out", verb, name)
			} else {
				require.False(t, got, "the administrator can read messages of %s, which carries no envelope", name)
			}
		}
	}
}
