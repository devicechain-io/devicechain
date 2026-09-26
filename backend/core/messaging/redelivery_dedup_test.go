// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging

import (
	"context"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/streams"
	"github.com/stretchr/testify/require"
)

// REACT re-publishes every alarm edge and connector request of a detection on each delivery
// of the derived event, under a dedup id that is stable across those deliveries. Both streams
// must therefore remember ids for longer than the whole redelivery span, or a retry's
// re-publish is stored — and for a connector, executed — a second time.
//
// The span is read off THIS package's constants, not streams': the window is derived in the
// leaf, and a messaging constant that stopped reading the leaf would leave the derivation
// describing a retry contract the broker no longer runs.
func TestREACTSinkStreamsCoverTheRedeliverySpan(t *testing.T) {
	span := MaxDeliver * AckWait
	for _, suffix := range []string{streams.ConnectorDispatch, streams.RaiseAlarm} {
		got := time.Duration(streams.DuplicateWindowSecondsFor(suffix)) * time.Second
		if got <= span {
			t.Errorf("%s duplicate window %v does not exceed the redelivery span %v "+
				"(MaxDeliver x AckWait): a REACT retry's re-publish is stored again", suffix, got, span)
		}
	}
}

// A REACT sink re-publishes the same alarm edge or connector request on every delivery of the
// derived event, under the same dedup id. On the real broker that must be stored ONCE, and a
// different id must still be stored (the negative control: the collapse is by id, not blanket).
//
// 🔑 THE VALUE THAT FAILS WITHOUT THE DECLARATION IS THE WINDOW, NOT THE COUNT. With no window
// declared the broker applies its own two-minute default, which absorbs an immediate repeat just
// as well, so Msgs is 2 either way. What the default cannot absorb is a repeat several minutes
// later — the last redelivery of a derived event — and a test cannot wait that long, so it reads
// the window the stream was actually created with.
func TestAREACTRepublishIsStoredOnce(t *testing.T) {
	srv := startEmbeddedServer(t)
	ms := testMicroservice(t, srv, uniqueArea("react-dedup"))
	nmgr := NewNatsManager(ms, core.NewNoOpLifecycleCallbacks(), func(*NatsManager) error { return nil })
	ctx := context.Background()
	require.NoError(t, nmgr.Initialize(ctx))
	require.NoError(t, nmgr.Start(ctx))
	t.Cleanup(func() { _ = nmgr.Stop(ctx) })
	want := time.Duration(streams.RedeliveryDuplicateWindowSeconds) * time.Second

	for _, suffix := range []string{streams.ConnectorDispatch, streams.RaiseAlarm} {
		t.Run(suffix, func(t *testing.T) {
			w, err := nmgr.NewWriter(suffix)
			require.NoError(t, err)
			tctx := core.WithTenant(ctx, "acme")
			require.NoError(t, w.WriteMessages(tctx, Message{Value: []byte("a"), DedupID: "t1"}))
			require.NoError(t, w.WriteMessages(tctx, Message{Value: []byte("a"), DedupID: "t1"}))
			require.NoError(t, w.WriteMessages(tctx, Message{Value: []byte("b"), DedupID: "t2"}))

			name := StreamName(ms.InstanceId, suffix)
			info, err := nmgr.js.StreamInfo(name)
			require.NoError(t, err)
			require.Equal(t, uint64(2), info.State.Msgs,
				"the repeat of t1 must be stored once, and t2 must still be stored")
			require.Equal(t, want, info.Config.Duplicates,
				"the stream must remember ids for the whole redelivery span, not the broker default")

			t.Run("an existing stream is widened", func(t *testing.T) {
				// The window a stream created by an earlier build holds: the broker default.
				cfg := info.Config
				cfg.Duplicates = 2 * time.Minute
				_, err := nmgr.js.UpdateStream(&cfg)
				require.NoError(t, err)

				_, err = nmgr.NewWriter(suffix) // ensureStream, as a restarted pod runs it
				require.NoError(t, err)
				widened, err := nmgr.js.StreamInfo(name)
				require.NoError(t, err)
				require.Equal(t, want, widened.Config.Duplicates)
			})
		})
	}
}
