// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging

import (
	"testing"

	"github.com/nats-io/nats.go"
	"github.com/stretchr/testify/require"
)

// headerBytes is what natsMsg's headers cost on the wire: the message's NATS size less its
// subject and payload. It is the "NATS/1.0" status line, one "Key: value" line per header
// and the closing blank line.
func headerBytes(nm *nats.Msg) int {
	return nm.Size() - len(nm.Subject) - len(nm.Reply) - len(nm.Data)
}

// TestCorrelationHeaderIsCompact pins what the correlation header costs on every message
// the platform publishes. The header rides every inbound and every resolved event, which is
// stored on every replica and delivered to every consumer, so each byte of it is paid about
// six times per event in network and disk. The key was "Dc-Correlation-Id" (17 bytes); it is
// "Dc-Cid" (6). A 22-character id costs 10 (status line) + 8 ("Dc-Cid: ") + 22 + 2 + 2.
func TestCorrelationHeaderIsCompact(t *testing.T) {
	const cid = "0123456789abcdefABCDEF" // a nuid's length
	nm := natsMsg("s", Message{Value: []byte("x")}.WithCorrelationID(cid))
	require.Equal(t, cid, nm.Header.Get("Dc-Cid"), "the correlation id travels under the compact key")
	require.Empty(t, nm.Header.Get("Dc-Correlation-Id"), "and only under it")
	require.Equal(t, 44, headerBytes(nm), "header bytes for a propagated correlation id")

	// A generated one is a nuid, the same length.
	require.Equal(t, 44, headerBytes(natsMsg("s", Message{Value: []byte("x")})), "header bytes for a generated correlation id")
}

// TestConsumedCorrelationRoundTrips is the reader's half: a delivered message's headers are
// flattened by natsHeaders, and CorrelationID must find the id the writer put there, or every
// consumer would generate a fresh one and the trail would break at each hop.
func TestConsumedCorrelationRoundTrips(t *testing.T) {
	nm := natsMsg("s", Message{}.WithCorrelationID("upstream"))
	consumed := NewConsumedMessage(nm.Subject, nm.Data, 1, natsHeaders(nm), nil)
	require.Equal(t, "upstream", consumed.CorrelationID())
}
