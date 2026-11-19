// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package natsauth

import (
	"strings"
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
	// The deletion facts are read the same way, by dcctl broker facts; see
	// TestTheAdministratorCanReadTheDeletionFactsButNeverRemoveThem.
	readable := map[string]bool{}
	for _, c := range streams.OperatorReadable() {
		readable[c] = true
	}
	for _, st := range streams.All {
		name := messaging.StreamName(permInstance, st.Suffix)
		for _, verb := range []string{"STREAM.MSG.GET.", "DIRECT.GET."} {
			got := p.publishes("$JS.API." + verb + name)
			switch {
			case isCarrier[st.Suffix]:
				require.True(t, got, "the administrator cannot %s%s, so dcctl's envelope guard times out", verb, name)
			case readable[st.Suffix]:
				require.True(t, got, "the administrator cannot %s%s, so dcctl broker facts is refused", verb, name)
			default:
				require.False(t, got, "the administrator can read messages of %s, which carries no envelope and is not operator-readable", name)
			}
		}
	}
}

// `dcctl broker facts` reads the deletion facts the platform announced, as the broker
// administrator. The grant is the two READ verbs, on exactly the streams the table declares
// OperatorReadable (today the deletion facts and nothing else), derived from the table. It must
// never become a way to REMOVE a fact: no purge, no message delete, no consumer, no publish
// into the stream. And only a stream the platform itself writes may be declared readable: a
// device's own messages are not the operator's to read off the broker.
//
// GUARD: kills an administrator without the read on the deletion facts, one granted a
// destructive verb on them, and a table that declares a device-written stream readable.
func TestTheAdministratorCanReadTheDeletionFactsButNeverRemoveThem(t *testing.T) {
	p, err := AdminPermissions(permInstance)
	require.NoError(t, err)
	require.Equal(t, []string{streams.EntityDeleted}, streams.OperatorReadable(),
		"the operator-readable streams changed: the administrator's read grant follows them, so decide it on purpose")
	for _, suffix := range streams.OperatorReadable() {
		require.True(t, streams.PlatformAuthoredFor(suffix),
			"%s is operator-readable but not PlatformAuthored: a device's messages would be readable by the operator plane", suffix)
		name := messaging.StreamName(permInstance, suffix)
		require.True(t, p.publishes("$JS.API.STREAM.MSG.GET."+name), "no STREAM.MSG.GET on %s", name)
		require.True(t, p.publishes("$JS.API.DIRECT.GET."+name), "no DIRECT.GET on %s", name)
		for _, verb := range []string{"STREAM.MSG.DELETE.", "STREAM.PURGE.", "STREAM.DELETE.", "STREAM.RESTORE.",
			"CONSUMER.CREATE.", "CONSUMER.DURABLE.CREATE.", "CONSUMER.DELETE.", "CONSUMER.MSG.NEXT."} {
			require.False(t, p.publishes("$JS.API."+verb+name), "the administrator may %s%s: a deletion fact could be removed", verb, name)
			require.False(t, p.publishes("$JS.API."+verb+name+".c"), "the administrator may %s%s.c", verb, name)
		}
		for _, subject := range messaging.StreamSubjects(permInstance, suffix) {
			require.False(t, p.publishes(strings.ReplaceAll(subject, "*", "x")), "the administrator may publish into %s", subject)
		}
	}
}
