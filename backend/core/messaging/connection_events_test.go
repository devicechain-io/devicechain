// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging

import (
	"bytes"
	"encoding/json"
	"errors"
	"net"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/config"
	"github.com/devicechain-io/dc-microservice/core"
	natsserver "github.com/nats-io/nats-server/v2/server"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
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
	nmgr := managerFor(t, srv)
	if err := nmgr.ExecuteInitialize(t.Context()); err != nil {
		t.Fatalf("connecting to the embedded broker: %v", err)
	}
	t.Cleanup(func() { nmgr.nc.Close() })
	if !nmgr.nc.IsConnected() {
		t.Fatal("not connected after ExecuteInitialize; the outage below would prove nothing")
	}

	srv.Shutdown()
	srv.WaitForShutdown()

	waitFor(t, "a disconnect log", func() bool {
		return strings.Contains(logs.String(), "Disconnected from NATS")
	})

	// The counterweight. A handler that logged on every event — or a test matching
	// a substring loose enough to hit anything — would pass the assertion above
	// while telling an operator nothing. A live-but-idle connection must be silent.
	if strings.Contains(logs.String(), "CLOSED permanently") {
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
	t.Cleanup(func() { nmgr.nc.Close() })

	srv.Shutdown()
	srv.WaitForShutdown()
	waitFor(t, "a disconnect log", func() bool {
		return strings.Contains(logs.String(), "Disconnected from NATS")
	})

	// Same port, so the client's reconnect loop finds it again.
	revived := startBrokerOnPort(t, port)
	defer revived.Shutdown()

	waitFor(t, "a reconnect log", func() bool {
		return findLog(logs, "Reconnected to NATS") != nil
	})

	// Read the field OFF THE RECONNECT RECORD, not out of the whole buffer.
	//
	// The first version of this searched the buffer for the port and passed even
	// with the server field deleted from the handler — because ExecuteInitialize
	// logs "Verified connectivity to NATS at 'nats://127.0.0.1:<port>'" on the way
	// in, and that line contains the port too. The assertion was satisfied by a log
	// written before the thing it was checking had happened. Found by mutation; it
	// would not have been found any other way.
	rec := findLog(logs, "Reconnected to NATS")
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
func findLog(logs *syncBuffer, want string) map[string]any {
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
		return findLog(logs, "CLOSED permanently") != nil
	})
	// The level is read off THAT record rather than searched for in the buffer: any
	// unrelated error logged during the test would otherwise satisfy it. A terminal
	// condition logged at warn sits in the same bucket as the recoverable
	// disconnects and is lost among them, which is the whole point of checking.
	if lvl, _ := findLog(logs, "CLOSED permanently")["level"].(string); lvl != "error" {
		t.Errorf("the permanent-close message was logged at %q, want error: it is the one "+
			"connection event that does not heal itself, so it must not share a level "+
			"with the two that do", lvl)
	}
}

// captureLogs redirects the global zerolog logger into a buffer for the duration
// of the test and restores it afterwards.
func captureLogs(t *testing.T) *syncBuffer {
	t.Helper()
	buf := &syncBuffer{}
	prev := log.Logger
	log.Logger = zerolog.New(buf)
	t.Cleanup(func() { log.Logger = prev })
	return buf
}

// syncBuffer is a bytes.Buffer safe for the NATS client's callback goroutines to
// write while the test goroutine reads.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
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
			FunctionalArea:        "area",
			InstanceConfiguration: *cfg,
		},
	}
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
