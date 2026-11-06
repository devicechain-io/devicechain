// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package partition

import (
	"errors"
	"testing"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/server"
	nats "github.com/nats-io/nats.go"
	"github.com/stretchr/testify/require"
)

// newKV starts an embedded JetStream server and returns a real KV bucket on it. The adapter's
// error mapping is the one thing the in-memory bucket cannot test: it depends on what the
// broker actually answers.
func newKV(t *testing.T) nats.KeyValue {
	t.Helper()
	srv, err := natsserver.NewServer(&natsserver.Options{
		Host: "127.0.0.1", Port: -1, JetStream: true, StoreDir: t.TempDir(), NoLog: true, NoSigs: true,
	})
	require.NoError(t, err)
	go srv.Start()
	require.True(t, srv.ReadyForConnections(10*time.Second), "embedded nats server not ready")
	t.Cleanup(srv.Shutdown)

	nc, err := nats.Connect(srv.ClientURL())
	require.NoError(t, err)
	t.Cleanup(nc.Close)
	js, err := nc.JetStream()
	require.NoError(t, err)
	kv, err := js.CreateKeyValue(&nats.KeyValueConfig{Bucket: "placement_test"})
	require.NoError(t, err)
	return kv
}

func TestTheNatsAdapterMapsBrokerConflictsToErrConflict(t *testing.T) {
	b := FromKeyValue(newKV(t))

	_, _, found, err := b.Get(TableKey)
	require.NoError(t, err)
	require.False(t, found, "a missing key is not an error")

	require.NoError(t, b.Create(TableKey, []byte("one")))
	require.True(t, errors.Is(b.Create(TableKey, []byte("two")), ErrConflict), "Create over an existing key")

	v, rev, found, err := b.Get(TableKey)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, "one", string(v))

	require.True(t, errors.Is(b.Update(TableKey, []byte("x"), rev+7), ErrConflict), "Update at a stale revision")
	require.NoError(t, b.Update(TableKey, []byte("three"), rev))
	require.True(t, errors.Is(b.Update(TableKey, []byte("four"), rev), ErrConflict), "the revision just used is now stale")

	v, _, _, err = b.Get(TableKey)
	require.NoError(t, err)
	require.Equal(t, "three", string(v), "a refused write must not land")
}

// Publish against the real broker: the second publisher's stale write is refused, not applied.
func TestPublishOnARealBucketRoundTripsAndRefusesAStalePublisher(t *testing.T) {
	b := FromKeyValue(newKV(t))
	first := sampleTable()
	require.NoError(t, Publish(b, first))
	require.NoError(t, Verify(b, first))

	next := sampleTable()
	next.Generation++
	require.NoError(t, Publish(b, next))
	require.True(t, errors.Is(Publish(b, first), ErrStale))
	require.NoError(t, Verify(b, next))
	require.True(t, errors.Is(Publish(b, NewTable(DefaultP*2)), ErrPChanged))
}
