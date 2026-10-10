// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/server"
	nats "github.com/nats-io/nats.go"
	"github.com/stretchr/testify/require"

	mscfg "github.com/devicechain-io/dc-microservice/config"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/streams"
	dctest "github.com/devicechain-io/dc-microservice/test"
)

// 🔑 ONE EMBEDDED JETSTREAM SERVER FOR THE WHOLE PROCESSOR TEST BINARY.
//
// The broker tests in this package used to start a server each. Under the race detector the
// server is instrumented along with the test, so ten of them — each waiting out redelivery
// timeouts one after another — were one of the two fixture costs of race-checking the module.
// The larger one was the ceiling fence-set and maximum-size roster fixtures (see
// manifestFactFor and the t.Parallel calls on the fence and reconcile tests). The broker tests
// now share one server and are kept apart the way a production broker keeps two instances
// apart: every stream, durable, subject and max-delivery advisory is named from the instance
// id (messaging.StreamName, DurableName, ScopedSubject, StreamSubjects), and each test gets
// its own. That is what lets them run in parallel.
//
// Which tests in this package call t.Parallel, and why only those: the broker tests (they wait
// on redelivery timers far more than they compute) and the fixture-heavy fence and reconcile
// tests (they author a full ceiling fence set, or a maximum-size roster, through
// device-management's real Api — CPU a parallel run spreads across cores). Everything else stays
// serial, and Go releases the parallel tests only after every serial one has returned, so the
// log-capture tests (logSink.Capture) and the wall-clock-bound TestAnAttemptEndsWithItsDelivery
// never overlap them.
var (
	sharedOnce  sync.Once
	sharedSrv   *natsserver.Server
	sharedDir   string
	sharedErr   error
	instanceSeq atomic.Int32
)

// sharedStoreCeiling is the server's JetStream file-store ceiling. It is FIXED rather than
// left to nats-server's disk-derived default, so that a test which reserves more than its
// share fails the same way on every machine instead of only on a runner with a small disk.
const sharedStoreCeiling = 1 << 30

// The stream ceilings every manager on the shared server is configured with. JetStream
// reserves a stream's MaxBytes up front, and at the production defaults (1 GiB for a hot
// stream) a hot stream created beside any other reservation is refused storage outright —
// the manager logs "insufficient storage resources" and retries until the test times out.
//
// No KV ceiling is set: none of these tests creates a bucket. One that does will reserve the
// production default and be refused at once, which is the loud way to find out it needs one.
const (
	testStreamMaxBytes     = 16 << 20
	testStreamMaxBytesCold = 4 << 20
)

func sharedBroker(t *testing.T) *natsserver.Server {
	t.Helper()
	sharedOnce.Do(func() {
		sharedDir, sharedErr = os.MkdirTemp("", "ep-processor-js-")
		if sharedErr != nil {
			return
		}
		srv, err := natsserver.NewServer(&natsserver.Options{
			Host: "127.0.0.1", Port: -1, JetStream: true, StoreDir: sharedDir,
			JetStreamMaxStore: sharedStoreCeiling, JetStreamMaxMemory: 64 << 20,
			NoLog: true, NoSigs: true,
		})
		if err != nil {
			sharedErr = err
			return
		}
		go srv.Start()
		if !srv.ReadyForConnections(10 * time.Second) {
			srv.Shutdown()
			sharedErr = errors.New("shared embedded nats server not ready")
			return
		}
		sharedSrv = srv
	})
	require.NoError(t, sharedErr, "shared embedded JetStream server")
	return sharedSrv
}

// stopSharedBroker shuts the shared server down and removes its store. TestMain calls it
// after every test has run; it is a no-op when no test asked for the server.
func stopSharedBroker() error {
	if sharedSrv != nil {
		sharedSrv.Shutdown()
	}
	if sharedDir == "" {
		return nil
	}
	return dctest.RemoveJetStreamStoreDir(sharedDir)
}

// detectBroker is ONE TEST'S view of the shared server: its own instance id and its own
// client connection.
type detectBroker struct {
	instance string
	host     string
	port     uint32
	nc       *nats.Conn
	// fetch is the pull shape every manager of this test's readers is built with. Unset it is
	// the production default; DC_TEST_FETCH_AHEAD=1 turns fetch-ahead on at batch 128 for every
	// broker test in this package, which is how the live-gap and integration suites are run
	// with the mechanism engaged:
	//
	//	DC_TEST_FETCH_AHEAD=1 go test -count=1 -p 2 ./processor/
	fetch mscfg.NatsFetchConfiguration
}

func startDetectBroker(t *testing.T) *detectBroker {
	t.Helper()
	srv := sharedBroker(t)
	u, err := url.Parse(srv.ClientURL())
	require.NoError(t, err)
	port, err := strconv.Atoi(u.Port())
	require.NoError(t, err)
	nc, err := nats.Connect(srv.ClientURL())
	require.NoError(t, err)
	// Registered first so it runs last: the stream deletion below needs the connection, and
	// cleanups run last in, first out.
	t.Cleanup(nc.Close)
	b := &detectBroker{
		// Letters and digits only, fixed width: no instance's "<id>_" can be a prefix of
		// another's, so the per-instance stream sweep below touches only its own.
		instance: fmt.Sprintf("ep%04d", instanceSeq.Add(1)),
		host:     u.Hostname(),
		port:     uint32(port),
		nc:       nc,
	}
	if os.Getenv("DC_TEST_FETCH_AHEAD") == "1" {
		b.fetch = mscfg.NatsFetchConfiguration{Batch: 128, Ahead: true}
	}
	// Registered before any manager or dispatcher the test builds, so it runs after all of
	// them have stopped.
	t.Cleanup(func() { b.deleteInstanceStreams(t) })
	return b
}

// natsConfig is the NATS configuration every manager of this test uses.
func (b *detectBroker) natsConfig() mscfg.NatsConfiguration {
	return mscfg.NatsConfiguration{
		Hostname: b.host, Port: b.port,
		StreamMaxBytes: testStreamMaxBytes, StreamMaxBytesCold: testStreamMaxBytesCold,
		Fetch: b.fetch,
	}
}

// instanceStreamPrefixes are the prefixes of every stream this instance can own on the
// broker: its message streams, and the streams JetStream creates behind its KV buckets.
func instanceStreamPrefixes(instance string) []string {
	return []string{instance + "_", messaging.KvStreamName(instance + "_")}
}

// instanceStreams lists the streams on the broker that belong to instance.
func instanceStreams(t *testing.T, nc *nats.Conn, instance string) []string {
	t.Helper()
	js, err := nc.JetStream()
	require.NoError(t, err)
	var out []string
	for name := range js.StreamNames() {
		for _, p := range instanceStreamPrefixes(instance) {
			if strings.HasPrefix(name, p) {
				out = append(out, name)
				break
			}
		}
	}
	return out
}

// deleteInstanceStreams deletes every stream this test's instance created, then checks none
// is left. A leaked stream keeps its reservation against sharedStoreCeiling and would starve
// a later test, so a failure here fails THIS test, which is the one that leaked it.
func (b *detectBroker) deleteInstanceStreams(t *testing.T) {
	js, err := b.nc.JetStream()
	if err != nil {
		t.Errorf("instance %s: JetStream context for stream cleanup: %v", b.instance, err)
		return
	}
	for _, name := range instanceStreams(t, b.nc, b.instance) {
		if err := js.DeleteStream(name); err != nil {
			t.Errorf("instance %s: delete stream %s: %v", b.instance, name, err)
		}
	}
	if left := instanceStreams(t, b.nc, b.instance); len(left) > 0 {
		t.Errorf("instance %s: streams outlived their test: %v", b.instance, left)
	}
}

// Two broker tests are served by the same server, as different instances.
func TestTheBrokerTestsShareOneServer(t *testing.T) {
	b1 := startDetectBroker(t)
	b2 := startDetectBroker(t)
	require.Equal(t, b1.host, b2.host)
	require.Equal(t, b1.port, b2.port, "a second embedded server was started")
	require.NotEqual(t, b1.instance, b2.instance, "two tests on one server must be different instances")
}

// What one test publishes is invisible to another test's instance on the same server.
func TestABrokerTestSeesOnlyItsOwnInstance(t *testing.T) {
	a := startDetectBroker(t)
	b := startDetectBroker(t)
	for _, x := range []*detectBroker{a, b} {
		nmgr, _, _ := x.brokerManager(t, func(*messaging.NatsManager) error { return nil })
		_, err := nmgr.NewWriter(streams.ResolvedEvents)
		require.NoError(t, err)
	}
	a.publish(t, testBase)
	// The positive half first: a read that silently saw nothing would otherwise pass as isolation.
	require.Equal(t, uint64(1), a.streamHeld(t, streams.ResolvedEvents))
	require.Equal(t, uint64(0), b.streamHeld(t, streams.ResolvedEvents))
}

// A broker test's streams are gone from the shared server once it ends.
func TestABrokerTestDeletesItsStreamsWhenItEnds(t *testing.T) {
	var instance string
	t.Run("owner", func(t *testing.T) {
		b := startDetectBroker(t)
		instance = b.instance
		nmgr, _, _ := b.brokerManager(t, func(*messaging.NatsManager) error { return nil })
		_, err := nmgr.NewWriter(streams.ResolvedEvents)
		require.NoError(t, err)
		require.NotEmpty(t, instanceStreams(t, b.nc, instance), "the owner created no stream; the check below is vacuous")
	})
	nc, err := nats.Connect(sharedBroker(t).ClientURL())
	require.NoError(t, err)
	defer nc.Close()
	require.Equal(t, []string(nil), instanceStreams(t, nc, instance), "the owner's streams outlived it")
}
