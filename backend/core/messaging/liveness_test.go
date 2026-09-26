// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	dctest "github.com/devicechain-io/dc-microservice/test"
	natsserver "github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
)

// healthz answers /healthz through the microservice's own mux, with the probes core
// registers on every DeviceChain HTTP server.
func healthz(ms *core.Microservice) int {
	rec := httptest.NewRecorder()
	ms.Mux().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	return rec.Code
}

// authManager is a manager built by NewNatsManager, logging in to an auth-enabled
// broker with user/pass the way a service presents its static credential, with the
// probe routes installed on its microservice.
func authManager(t *testing.T, srv *natsserver.Server, area, user, pass string) *NatsManager {
	t.Helper()
	ms := testMicroservice(t, srv, uniqueArea(area))
	ms.InstanceConfiguration.Infrastructure.Nats.Auth.User = user
	ms.InstanceConfiguration.Infrastructure.Nats.Auth.Password = pass
	ms.RegisterProbes(ms.Readiness)
	return NewNatsManager(ms, core.NewNoOpLifecycleCallbacks(), func(*NatsManager) error { return nil })
}

// A connection the client closes permanently, NOT because this process asked, must fail
// liveness — so the kubelet restarts the pod, and the restart re-reads the credential.
//
// The route is the realistic one: the broker's password changes under a running
// service. The client reconnects, is refused, and on the second identical authorization
// failure nats.go aborts its reconnect loop and closes for good. Before this, the pod
// stayed Live and Ready with one ERROR line, doing nothing, forever.
func TestAnUnrequestedCloseFailsLiveness(t *testing.T) {
	logs := captureLogs(t)

	srv := startAuthBrokerOnPort(t, -1, "svc", "old-pass")
	port := srv.Addr().(*net.TCPAddr).Port
	nmgr := authManager(t, srv, "liveness-trip", "svc", "old-pass")
	ms := nmgr.Microservice
	ctx := context.Background()
	if err := nmgr.Initialize(ctx); err != nil {
		t.Fatalf("initialize: %v", err)
	}
	if err := nmgr.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = nmgr.Stop(ctx) })
	nc := nmgr.Conn()
	if !nc.IsConnected() {
		t.Fatal("not connected with the valid credential; nothing below would mean anything")
	}
	if got := healthz(ms); got != http.StatusOK {
		t.Fatalf("precondition: /healthz = %d on a connected service, want 200", got)
	}

	// Rotate the credential under the running service: same port, new password.
	srv.Shutdown()
	srv.WaitForShutdown()
	revived := startAuthBrokerOnPort(t, port, "svc", "new-pass")
	t.Cleanup(revived.Shutdown)

	deadline := time.Now().Add(30 * time.Second)
	for !nc.IsClosed() {
		if time.Now().After(deadline) {
			t.Fatalf("the connection never closed permanently (status %s, last error %v)",
				nc.Status(), nc.LastError())
		}
		time.Sleep(50 * time.Millisecond)
	}
	// The handler runs on the client's callback goroutine after the status flips.
	waitFor(t, "the liveness failure", func() bool { return ms.Live() != nil })

	if got := healthz(ms); got != http.StatusServiceUnavailable {
		t.Errorf("/healthz = %d after the connection closed permanently, want 503", got)
	}
	if err := ms.Live(); !strings.Contains(err.Error(), "not by this process") {
		t.Errorf("Live() = %q, want the reason to say the close was not this process's", err)
	}
	if rec := findLog(logs, "CLOSED permanently"); rec == nil {
		t.Error("the unrequested close was not logged")
	} else if lvl, _ := rec["level"].(string); lvl != "error" {
		t.Errorf("the unrequested close was logged at %q, want error", lvl)
	}
	// One liveness ERROR, not one per event: the latch logs only its first transition.
	if n := strings.Count(logs.String(), "the liveness probe (/healthz) now fails"); n != 1 {
		t.Errorf("the liveness failure was logged %d times, want exactly 1", n)
	}
}

// ...and an orderly stop must NOT fail liveness. Its drain and close fire the same
// ClosedHandler, and a pod that failed liveness on the way out of every rolling update
// would be killed mid-teardown instead of finishing it.
//
// Asserted after Stop has returned and after the handler has provably run — the
// "closed during shutdown" record is what says so — so a 200 here is the handler's
// verdict, not the handler not having run yet.
func TestAnOrderlyStopDoesNotFailLiveness(t *testing.T) {
	logs := captureLogs(t)

	srv := startAuthBrokerOnPort(t, -1, "svc", "pass")
	t.Cleanup(srv.Shutdown)
	nmgr := authManager(t, srv, "liveness-orderly", "svc", "pass")
	ms := nmgr.Microservice
	ctx := context.Background()
	if err := nmgr.Initialize(ctx); err != nil {
		t.Fatalf("initialize: %v", err)
	}
	if err := nmgr.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}

	stopCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := nmgr.Stop(stopCtx); err != nil {
		t.Fatalf("stop: %v", err)
	}
	if err := nmgr.Terminate(stopCtx); err != nil {
		t.Fatalf("terminate: %v", err)
	}
	waitFor(t, "the shutdown-close log", func() bool {
		return findLog(logs, "closed during shutdown") != nil
	})

	if got := healthz(ms); got != http.StatusOK {
		t.Errorf("/healthz = %d after an orderly stop, want 200", got)
	}
	if err := ms.Live(); err != nil {
		t.Errorf("an orderly stop marked the process not live: %v", err)
	}
	if rec := findLog(logs, "CLOSED permanently"); rec != nil {
		t.Errorf("an orderly stop logged the unrequested-close alarm: %v", rec["message"])
	}
}

// ...nor may a startup that abandons its own connection. Initialize closes the
// connection it dialled when its context is cancelled before the broker answers, and
// that close is this process's, not the broker's: it must not mark a process that is on
// its way out — or, re-initialized, on its way in — as needing a restart.
//
// The ClosedHandler fires asynchronously after Initialize returns, so the test waits
// for its record first; a 200 read before the handler ran would prove nothing.
func TestAnAbandonedStartupDoesNotFailLiveness(t *testing.T) {
	logs := captureLogs(t)

	ms := &core.Microservice{InstanceId: "test", FunctionalArea: uniqueArea("liveness-startup")}
	ms.InstanceConfiguration.Infrastructure.Nats.Hostname = "127.0.0.1"
	ms.InstanceConfiguration.Infrastructure.Nats.Port = 1 // nothing listening
	ms.RegisterProbes(nil)
	nmgr := NewNatsManager(ms, core.NewNoOpLifecycleCallbacks(), func(*NatsManager) error { return nil })

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := nmgr.Initialize(ctx)
	if err == nil {
		t.Fatal("Initialize succeeded with a cancelled context and no broker")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Initialize failed with %v, want the cancellation; the close under test is "+
			"the one that path makes", err)
	}

	waitFor(t, "the startup close to be handled", func() bool {
		return findLog(logs, "closed during shutdown") != nil
	})
	if got := healthz(ms); got != http.StatusOK {
		t.Errorf("/healthz = %d after a startup abandoned its own connection, want 200", got)
	}
	if err := ms.Live(); err != nil {
		t.Errorf("an abandoned startup marked the process not live: %v", err)
	}
}

// A Stop that begins while an unrequested close's ClosedHandler is still QUEUED must
// not relabel that close as its own.
//
// The handler runs on the client's single callback goroutine and reads closeRequested
// when it runs, not when the close happened. The stop that follows an unrequested close
// arrives soon after it, so if the stop set the flag on the dead connection, a handler
// still waiting its turn would read "requested", log a shutdown, and never fail
// liveness — the exact close liveness exists for, lost.
//
// The queue is held deterministically: close() queues the disconnect callback ahead of
// the closed one, so a disconnect handler that blocks keeps the ClosedHandler waiting
// until the Stop has returned.
func TestAStopDoesNotRelabelAQueuedUnrequestedClose(t *testing.T) {
	logs := captureLogs(t)
	srv := startEmbeddedServer(t)
	nmgr := startedManager(t, srv, "liveness-queued", nil)
	nc := nmgr.Conn()

	hold := make(chan struct{})
	var released atomic.Bool
	release := func() {
		if released.CompareAndSwap(false, true) {
			close(hold)
		}
	}
	t.Cleanup(release)
	nc.SetDisconnectErrHandler(func(*nats.Conn, error) { <-hold })

	// Closed without the manager asking, as a revoked credential would.
	nc.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := nmgr.Stop(ctx); err != nil {
		t.Fatalf("stop: %v", err)
	}
	if rec := findLog(logs, "CLOSED permanently"); rec != nil {
		t.Fatal("the ClosedHandler ran before the Stop; the queue was not held and the case " +
			"under test did not happen")
	}
	release()

	waitFor(t, "the queued ClosedHandler to run", func() bool {
		return findLog(logs, "CLOSED permanently") != nil || findLog(logs, "closed during shutdown") != nil
	})
	if findLog(logs, "closed during shutdown") != nil {
		t.Error("the unrequested close was logged as a shutdown: the Stop relabelled it")
	}
	if err := nmgr.Microservice.Live(); err == nil {
		t.Error("an unrequested close left the process live because a Stop began before its " +
			"handler ran")
	}
}

// startAuthBrokerOnPort starts an embedded broker that requires user/pass, on an
// ephemeral port when port is -1 or on a specific one — the rotation test revives the
// server at the address the client already knows. The fixed-port case retries, for the
// reason startBrokerOnPort gives.
func startAuthBrokerOnPort(t *testing.T, port int, user, pass string) *natsserver.Server {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		srv, err := natsserver.NewServer(&natsserver.Options{
			Host:      "127.0.0.1",
			Port:      port,
			JetStream: true,
			StoreDir:  dctest.JetStreamStoreDir(t),
			Username:  user,
			Password:  pass,
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
