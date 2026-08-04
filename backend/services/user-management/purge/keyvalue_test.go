// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package purge

import (
	"context"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/messaging"
	natsserver "github.com/nats-io/nats-server/v2/server"
	nats "github.com/nats-io/nats.go"
	"github.com/stretchr/testify/require"
)

// kvRig starts an in-process JetStream server and returns a connection to it.
//
// A REAL server rather than a fake, for the reason core/messaging's own purge rig gives:
// everything this store does happens inside JetStream — which buckets exist, what a scan
// finds, what the server says about a bucket that is not there. A fake answers whatever it
// was written to answer, which is asserting the code against itself.
func kvRig(t *testing.T) *nats.Conn {
	t.Helper()
	srv, err := natsserver.NewServer(&natsserver.Options{
		Host:       "127.0.0.1",
		Port:       -1,
		ServerName: "kv-store-test",
		JetStream:  true,
		StoreDir:   t.TempDir(),
	})
	require.NoError(t, err)
	go srv.Start()
	require.True(t, srv.ReadyForConnections(10*time.Second), "the test broker never became ready")
	t.Cleanup(srv.Shutdown)

	nc, err := nats.Connect(srv.ClientURL())
	require.NoError(t, err)
	t.Cleanup(nc.Close)
	return nc
}

// kvConn hands the store a broker connection, which is the whole of messaging.Connection.
type kvConn struct{ nc *nats.Conn }

func (c kvConn) Conn() *nats.Conn { return c.nc }

// TestTheKeyValueStoreRecordsWhatItDeclinedToLookAt drives the REAL Erase against a real
// JetStream server.
//
// 🔴 IT HAS TO DRIVE IT. The first version of this test built an `Outcome{Notes:
// messaging.KvPurgeExemptions()}` itself and asserted the exemptions were in it — which is
// true of a literal the test wrote, and says nothing about the store. Deleting the store's
// note entirely left that version green; this one fails. Assert on what the code PRODUCES,
// never on a value reconstructed beside it.
func TestTheKeyValueStoreRecordsWhatItDeclinedToLookAt(t *testing.T) {
	nc := kvRig(t)
	store := NewKeyValue(kvConn{nc: nc}, "inst1")

	out, err := store.Erase(context.Background(), "acme", time.Now())

	require.NoError(t, err)
	require.True(t, out.Clean(), "the exemptions hold nothing of the tenant's, so they must not block")

	exemptions := messaging.KvPurgeExemptions()
	require.NotEmpty(t, exemptions, "the platform must declare exemptions, or this test is vacuous")
	for _, e := range exemptions {
		require.Contains(t, out.Note(), e,
			"every exemption the platform declares must reach the store's outcome, and through it "+
				"the deletion record — a reader deciding whether an erasure claim holds needs the "+
				"judgement, not the omission")
	}
}

// TestAKeyValueStoreWithNoConnectionProducesNoNote pins that the note follows the connection
// check rather than preceding it.
//
// It is NOT sufficient on its own, and saying so is the point: it cannot tell a store that
// swept from one that returned its notes and skipped the sweep, because both take this same
// branch when there is no connection. TestAKeyValueStoreThatCannotSweepDoesNotReportClean is
// the test that discriminates those two.
func TestAKeyValueStoreWithNoConnectionProducesNoNote(t *testing.T) {
	out, err := NewKeyValue(kvConn{nc: nil}, "inst1").Erase(context.Background(), "acme", time.Now())

	require.Error(t, err, "an unavailable broker is retryable, not an answer")
	require.Empty(t, out.Note(),
		"a store that never ran cannot report what it declined to look at — a note here would mean "+
			"the note is a constant rather than a product of the sweep")
}

// TestAKeyValueStoreThatCannotSweepDoesNotReportClean is the discriminator, and it pins a
// property worth having quite apart from its role here.
//
// The broker it is given has JetStream DISABLED, so the sweep cannot run. A store that really
// calls PurgeTenantKv gets an error and reports the pass as retryable; a store that returned
// its notes without sweeping — the mutation the tests above cannot see — reports CLEAN, and a
// purge would complete over key-value data nobody ever looked at. That is a false erasure
// claim, which is the one failure this whole subsystem exists to prevent.
//
// It deliberately does not assert on the error TEXT. What matters is that not being able to
// sweep is an error rather than an answer; the wording belongs to core/messaging.
func TestAKeyValueStoreThatCannotSweepDoesNotReportClean(t *testing.T) {
	srv, err := natsserver.NewServer(&natsserver.Options{
		Host:       "127.0.0.1",
		Port:       -1,
		ServerName: "kv-store-no-jetstream",
		JetStream:  false,
	})
	require.NoError(t, err)
	go srv.Start()
	require.True(t, srv.ReadyForConnections(10*time.Second), "the test broker never became ready")
	t.Cleanup(srv.Shutdown)
	nc, err := nats.Connect(srv.ClientURL())
	require.NoError(t, err)
	t.Cleanup(nc.Close)

	out, err := NewKeyValue(kvConn{nc: nc}, "inst1").Erase(context.Background(), "acme", time.Now())

	require.Error(t, err,
		"a store that cannot reach the key-value buckets must report the pass as retryable — "+
			"reporting clean here would complete a purge over data nobody looked at")
	require.Empty(t, out.Note(),
		"a pass that never swept has nothing to say about what it declined to look at")
}
