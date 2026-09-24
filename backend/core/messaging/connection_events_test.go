// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/config"
	"github.com/devicechain-io/dc-microservice/core"
	dctest "github.com/devicechain-io/dc-microservice/test"
	natsserver "github.com/nats-io/nats-server/v2/server"
)

// A broker outage must reach the logs.
//
// This goes through ExecuteInitialize — the production connect path — rather than
// building the option list the way that function does. The distinction is not
// pedantry: the failure this guards against is the handlers existing and nothing
// registering them, and a test that assembles its own options would agree with
// itself while the real connect had none. That exact shape (a harness restating a
// production expression instead of calling it) is how an earlier regression in
// this package survived a full green suite.
//
// Killing the server rather than closing the client is deliberate too. Client
// Close fires the CLOSED handler; only losing the server produces the DISCONNECT
// the reconnect logic exists for, which is the one a node loss in an HA cluster
// actually causes.
func TestBrokerOutageIsLogged(t *testing.T) {
	logs := captureLogs(t)

	srv := startBroker(t)
	// Captured BEFORE the shutdown below: srv.Addr() is nil once the server stops.
	port := srv.Addr().(*net.TCPAddr).Port
	nmgr := managerFor(t, srv)
	if err := nmgr.ExecuteInitialize(t.Context()); err != nil {
		t.Fatalf("connecting to the embedded broker: %v", err)
	}
	t.Cleanup(func() { terminateAndWait(t, logs, nmgr) })
	if !nmgr.nc.IsConnected() {
		t.Fatal("not connected after ExecuteInitialize; the outage below would prove nothing")
	}

	srv.Shutdown()
	srv.WaitForShutdown()

	waitFor(t, "a disconnect log", func() bool {
		return ownLog(logs, nmgr, "Disconnected from NATS") != nil
	})

	// The disconnect must name the broker it lost.
	//
	// This cannot come from nc.ConnectedUrl() inside the callback: that returns ""
	// for any status other than CONNECTED, and the status has already flipped by the
	// time the handler runs — so the obvious implementation logs an empty field
	// forever. It reads a remembered value instead, and this asserts the remembering
	// works. Read off the record, not out of the buffer: ExecuteInitialize logs the
	// same URL on the way in, so a whole-buffer match would pass with the field gone.
	if got, _ := ownLog(logs, nmgr, "Disconnected from NATS")["server"].(string); !strings.Contains(got, strconv.Itoa(port)) {
		t.Errorf("the disconnect record's server field is %q and does not name the broker "+
			"that was lost; on a cluster that is the field that says WHICH node went away", got)
	}

	// The counterweight. A handler that logged on every event — or a test matching
	// a substring loose enough to hit anything — would pass the assertion above
	// while telling an operator nothing. A live-but-idle connection must be silent.
	if ownLog(logs, nmgr, "CLOSED permanently") != nil {
		t.Error("a recoverable disconnect logged the TERMINAL closed message: those two " +
			"say opposite things about whether the service comes back on its own, and " +
			"conflating them makes the loud one meaningless")
	}
}

// ...and the recovery must reach the logs too, naming the server it landed on.
//
// A disconnect log with no matching reconnect is how an outage looks in the
// records whether it lasted two seconds or two hours. The server URL is the datum
// that distinguishes real failover in a cluster (a DIFFERENT server) from a
// flapping one (the same server, repeatedly), which want opposite responses.
func TestReconnectIsLogged(t *testing.T) {
	logs := captureLogs(t)

	srv := startBroker(t)
	port := srv.Addr().(*net.TCPAddr).Port
	nmgr := managerFor(t, srv)
	if err := nmgr.ExecuteInitialize(t.Context()); err != nil {
		t.Fatalf("connecting to the embedded broker: %v", err)
	}
	t.Cleanup(func() { terminateAndWait(t, logs, nmgr) })

	srv.Shutdown()
	srv.WaitForShutdown()
	waitFor(t, "a disconnect log", func() bool {
		return ownLog(logs, nmgr, "Disconnected from NATS") != nil
	})

	// Same port, so the client's reconnect loop finds it again.
	revived := startBrokerOnPort(t, port)
	defer revived.Shutdown()

	waitFor(t, "a reconnect log", func() bool {
		return ownLog(logs, nmgr, "Reconnected to NATS") != nil
	})

	// Read the field OFF THE RECONNECT RECORD, not out of the whole buffer.
	//
	// The first version of this searched the buffer for the port and passed even
	// with the server field deleted from the handler — because ExecuteInitialize
	// logs "Verified connectivity to NATS at 'nats://127.0.0.1:<port>'" on the way
	// in, and that line contains the port too. The assertion was satisfied by a log
	// written before the thing it was checking had happened. Found by mutation; it
	// would not have been found any other way.
	rec := ownLog(logs, nmgr, "Reconnected to NATS")
	if got, _ := rec["server"].(string); !strings.Contains(got, strconv.Itoa(port)) {
		t.Errorf("the reconnect record's server field is %q, which does not name the "+
			"server the client landed on: without it there is no way to tell failover "+
			"to another node from the same one flapping, and those want opposite "+
			"responses", got)
	}
}

// findLog returns the first captured zerolog record whose message contains want,
// decoded, or nil. Decoding rather than substring-matching is what lets an
// assertion name the FIELD it depends on.
func findLog(logs *dctest.LogSink, want string) map[string]any {
	for _, line := range strings.Split(logs.String(), "\n") {
		if line == "" {
			continue
		}
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			continue
		}
		if msg, _ := rec["message"].(string); strings.Contains(msg, want) {
			return rec
		}
	}
	return nil
}

// A permanent close is the one connection event that is not self-healing, so it
// must be distinguishable from the two that are.
//
// This is the UNEXPECTED close: the connection dies without the manager asking.
// See TestShutdownCloseIsNotLoggedAtError for the other half — the distinction is
// load-bearing, because without it every graceful pod termination emits this
// message and the level stops carrying information.
func TestPermanentCloseIsLoggedAtError(t *testing.T) {
	logs := captureLogs(t)

	srv := startBroker(t)
	defer srv.Shutdown()
	nmgr := managerFor(t, srv)
	if err := nmgr.ExecuteInitialize(t.Context()); err != nil {
		t.Fatalf("connecting to the embedded broker: %v", err)
	}

	nmgr.nc.Close()

	waitFor(t, "a closed log", func() bool {
		return ownLog(logs, nmgr, "CLOSED permanently") != nil
	})
	// The handler goes on to call MarkNotLive, which logs a SECOND error line. Wait for
	// it too: returning after the first line let the second land in the NEXT test's
	// capture, where TestShutdownCloseIsNotLoggedAtError read it as its own graceful
	// shutdown logging at error level.
	waitFor(t, "the liveness log", func() bool {
		return ownLog(logs, nmgr, "only a restart can clear it") != nil
	})
	// The level is read off THAT record rather than searched for in the buffer: any
	// unrelated error logged during the test would otherwise satisfy it. A terminal
	// condition logged at warn sits in the same bucket as the recoverable
	// disconnects and is lost among them, which is the whole point of checking.
	if lvl, _ := ownLog(logs, nmgr, "CLOSED permanently")["level"].(string); lvl != "error" {
		t.Errorf("the permanent-close message was logged at %q, want error: it is the one "+
			"connection event that does not heal itself, so it must not share a level "+
			"with the two that do", lvl)
	}
}

// ...and a close the manager ASKED for is not that event.
//
// ExecuteStop drains and ExecuteTerminate closes, so the ClosedHandler fires on
// every graceful shutdown. Logging the terminal message there would put an ERROR
// saying the service is mute into the logs of every service on every rolling update,
// node drain and scale-down — roughly a dozen services times their replicas per
// `helm upgrade`, all of them describing a healthy deploy. Any error-rate alerting
// then fires on success, and the one message that means "this pod is mute" is buried
// in the noise of the ones that do not. (The same classification decides liveness;
// liveness_test.go holds that half.)
//
// An earlier version of this suite asserted the error level using nmgr.nc.Close()
// — the bare call ExecuteTerminate once made — so it did not merely miss this, it
// pinned the defect in place.
func TestShutdownCloseIsNotLoggedAtError(t *testing.T) {
	logs := captureLogs(t)

	srv := startBroker(t)
	defer srv.Shutdown()
	nmgr := managerFor(t, srv)
	if err := nmgr.ExecuteInitialize(t.Context()); err != nil {
		t.Fatalf("connecting to the embedded broker: %v", err)
	}

	if err := nmgr.ExecuteTerminate(t.Context()); err != nil {
		t.Fatalf("terminate: %v", err)
	}

	waitFor(t, "a shutdown-close log", func() bool {
		return ownLog(logs, nmgr, "closed during shutdown") != nil
	})
	if rec := ownLog(logs, nmgr, "CLOSED permanently"); rec != nil {
		t.Errorf("a deliberate shutdown logged the terminal not-as-part-of-a-shutdown "+
			"message: %v", rec["message"])
	}
	for _, rec := range ownLogs(logs, nmgr) {
		if lvl, _ := rec["level"].(string); lvl == "error" {
			t.Errorf("a graceful shutdown logged at error level: %v", rec["message"])
		}
	}
}

// captureLogs collects the global logger's output for the duration of the test.
//
// It switches collection on against the package-wide sink installed by TestMain
// rather than installing a logger of its own, which is what an earlier version did:
// it assigned to log.Logger here and restored it in t.Cleanup, underneath the NATS
// client's callback goroutine still reading it. The reasoning is on dctest.LogSink.
func captureLogs(t *testing.T) *dctest.LogSink {
	t.Helper()
	return logSink.Capture(t)
}

func startBroker(t *testing.T) *natsserver.Server { return startBrokerOnPort(t, -1) }

// startBrokerOnPort starts an embedded broker, on an ephemeral port when port is
// -1 or on a specific one for the reconnect test, which has to revive the server
// at the address the client already knows.
//
// The fixed-port case RETRIES. A port just released by Shutdown is not
// instantaneously re-bindable — the listener teardown is asynchronous and the
// socket may sit briefly unavailable — so a single attempt makes the reconnect
// test fail intermittently for a reason that has nothing to do with what it
// asserts. That is worth spending a few hundred milliseconds to avoid: an
// intermittent failure in a test about connection recovery is precisely the kind
// that gets waved through as "flaky, probably the network".
func startBrokerOnPort(t *testing.T, port int) *natsserver.Server {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		srv, err := natsserver.NewServer(&natsserver.Options{
			Host:      "127.0.0.1",
			Port:      port,
			JetStream: true,
			StoreDir:  t.TempDir(),
		})
		if err == nil {
			go srv.Start()
			if srv.ReadyForConnections(5 * time.Second) {
				return srv
			}
			srv.Shutdown()
			err = errors.New("not ready for connections")
		}
		if port == -1 || time.Now().After(deadline) {
			t.Fatalf("embedded nats server on port %d: %v", port, err)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// managerFor builds a NatsManager whose instance config points at the embedded
// broker, so ExecuteInitialize dials it exactly as a real service would.
func managerFor(t *testing.T, srv *natsserver.Server) *NatsManager {
	t.Helper()
	addr := srv.Addr().(*net.TCPAddr)
	cfg := &config.InstanceConfiguration{}
	cfg.ApplyDefaults()
	cfg.Infrastructure.Nats.Hostname = addr.IP.String()
	cfg.Infrastructure.Nats.Port = uint32(addr.Port)
	// The defaults carry a TLS block only when configured; the embedded server is
	// plaintext, so leave it alone.
	return &NatsManager{
		Microservice: &core.Microservice{
			InstanceId:            "test",
			FunctionalArea:        areaFor(t),
			InstanceConfiguration: *cfg,
		},
	}
}

// areaFor gives each test's manager an area of its own.
//
// The log sink is shared by the whole test binary, and a NATS client runs its
// connection callbacks on a goroutine of its own, so a manager can still be logging
// after the test that made it has returned. Its ClosedHandler writes two records, and a
// test that waits for the first can hand the second to the NEXT test's capture. That
// read-by-timing is what made TestShutdownCloseIsNotLoggedAtError fail on a line that
// another test's manager wrote. Scoping every read to the reader's own area makes the
// attribution a fact rather than a race: ownLog and ownLogs never see a neighbour.
func areaFor(t *testing.T) string {
	return "t-" + strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			return r
		case r >= 'A' && r <= 'Z':
			return r + ('a' - 'A')
		}
		return '-'
	}, t.Name())
}

// ownLogs returns the captured records written for nmgr's area, decoded.
func ownLogs(logs *dctest.LogSink, nmgr *NatsManager) []map[string]any {
	var out []map[string]any
	for _, line := range strings.Split(logs.String(), "\n") {
		var rec map[string]any
		if line == "" || json.Unmarshal([]byte(line), &rec) != nil {
			continue
		}
		if area, _ := rec["area"].(string); area == nmgr.Microservice.FunctionalArea {
			out = append(out, rec)
		}
	}
	return out
}

// ownLog is findLog restricted to nmgr's own records.
func ownLog(logs *dctest.LogSink, nmgr *NatsManager, want string) map[string]any {
	for _, rec := range ownLogs(logs, nmgr) {
		if msg, _ := rec["message"].(string); strings.Contains(msg, want) {
			return rec
		}
	}
	return nil
}

// terminateAndWait ends a test's connection the way a shutdown does, and waits for the
// handler's record before the test's capture stops. A bare Close here is an UNREQUESTED
// close, which writes two ERROR records after the test has returned.
func terminateAndWait(t *testing.T, logs *dctest.LogSink, nmgr *NatsManager) {
	t.Helper()
	if err := nmgr.ExecuteTerminate(context.Background()); err != nil {
		t.Errorf("terminate: %v", err)
		return
	}
	waitFor(t, "the shutdown-close log", func() bool {
		return ownLog(logs, nmgr, "closed during shutdown") != nil
	})
}

// waitFor polls until cond holds, failing with what was being waited on. The NATS
// client dispatches these callbacks on its own goroutines, so there is nothing to
// synchronize on other than the effect.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// The "you are not highly available" shout must not fire at a broker that is
// merely not reachable YET.
//
// Under RetryOnFailedConnect, nats.Connect returns a non-nil connection and a nil
// error while it is still dialling, so at the end of ExecuteInitialize the client
// is routinely not connected. Reporting the clamp there told a correctly
// configured 3-node instance that its broker was unclustered and it was not HA —
// on every cold start, every node drain, and every helm upgrade that rolls the
// NATS StatefulSet alongside the Deployments. It is the same false negative the
// clamp itself was fixed for, one layer up, and it points the operator at the one
// lever they already set correctly.
func TestReplicaClampIsNotReportedBeforeTheBrokerIsReachable(t *testing.T) {
	logs := captureLogs(t)

	// A configured-for-HA instance whose broker is not up.
	cfg := &config.InstanceConfiguration{}
	cfg.ApplyDefaults()
	cfg.Infrastructure.Nats.Hostname = "127.0.0.1"
	cfg.Infrastructure.Nats.Port = 1 // nothing listening
	cfg.Infrastructure.Nats.StreamReplicas = 3
	nmgr := &NatsManager{Microservice: &core.Microservice{
		InstanceId: "test", FunctionalArea: "area", InstanceConfiguration: *cfg,
	}}

	if err := nmgr.ExecuteInitialize(t.Context()); err != nil {
		t.Fatalf("ExecuteInitialize should not fail while retrying: %v", err)
	}
	t.Cleanup(nmgr.closeConn)

	if nmgr.nc.IsConnected() {
		t.Fatal("the client connected to a port with nothing on it; this test proves nothing")
	}
	if rec := findLog(logs, "not highly available"); rec != nil {
		t.Errorf("an instance whose broker is merely unreachable was told it is not highly "+
			"available: %v", rec["message"])
	}
}
