// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/devicechain-io/dc-microservice/config"
	"github.com/devicechain-io/dc-microservice/core"
)

// drainSchema pairs a query with a subscription that stays open until its context
// is cancelled — the shape every real subscription has, and the one a teardown has
// to be able to end.
const drainSchema = `
	schema { query: Query subscription: Subscription }
	type Query { hello: String! }
	type Subscription { ticker: Int! }
`

// drainResolver counts the subscription streams that are currently live.
//
// 🔴 THE COUNTER IS THE INSTRUMENT, AND IT COUNTS THE RESOLVER'S OWN GOROUTINE, NOT
// THE HANDLER'S BOOKKEEPING. In production each of these goroutines holds a
// tenant-scoped NATS subscription, so "is it still running?" is the question that
// actually matters — and asking the thing under test whether it thinks it cleaned up
// would be a control built out of the code it is controlling.
type drainResolver struct{ live atomic.Int64 }

func (*drainResolver) Hello() string { return "hi" }

func (r *drainResolver) Ticker(ctx context.Context) <-chan int32 {
	ch := make(chan int32)
	r.live.Add(1)
	go func() {
		defer r.live.Add(-1)
		defer close(ch)
		// One value, so a client can prove the stream is live, and then nothing: the
		// goroutine sits on the context exactly as a real event stream sits on its
		// subscription, and ends only when something cancels it.
		select {
		case ch <- 1:
		case <-ctx.Done():
			return
		}
		<-ctx.Done()
	}()
	return ch
}

// startDrainServer brings up the REAL GraphQL server — the same ExecuteInitialize and
// ExecuteStart the lifecycle calls — on an ephemeral port, and returns it with the
// resolver whose live-stream count is the measurement.
func startDrainServer(t *testing.T, maxMessageBytes int64) (*GraphQLManager, *drainResolver, string) {
	t.Helper()

	ms := &core.Microservice{InstanceId: "test", FunctionalArea: "subscription-drain"}
	ms.UseMetricsRegistry(prometheus.NewRegistry())
	ms.InstanceConfiguration.Infrastructure.GraphQL.MaxSubscriptionMessageBytes = maxMessageBytes
	gate := core.NewReadinessGate()
	gate.MarkReadyWithoutAuthSurface()

	res := &drainResolver{}
	gql := &GraphQLManager{
		Microservice: ms,
		Schema:       MustParseSchema(drainSchema, res),
		Gate:         gate,
		Port:         ephemeralPort,
	}
	if err := gql.ExecuteInitialize(context.Background()); err != nil {
		t.Fatalf("ExecuteInitialize: %v", err)
	}
	if err := gql.ExecuteStart(context.Background()); err != nil {
		t.Fatalf("ExecuteStart: %v", err)
	}
	t.Cleanup(func() { _ = gql.ExecuteStop(context.Background()) })
	return gql, res, gql.Server.Addr()
}

// dialSubscribed opens one graphql-transport-ws connection against addr, initialises
// it, starts a ticker subscription and reads its first `next` — so on return the
// connection is unambiguously live and streaming.
func dialSubscribed(t *testing.T, addr string) *websocket.Conn {
	t.Helper()

	dialer := websocket.Dialer{Subprotocols: []string{wsSubprotocol}}
	conn, _, err := dialer.Dial("ws://"+addr+"/graphql", nil)
	if err != nil {
		t.Fatalf("dial ws://%s/graphql: %v", addr, err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))

	writeMsg(t, conn, wsMessage{Type: msgConnectionInit})
	if got := readMsg(t, conn); got.Type != msgConnectionAck {
		t.Fatalf("expected %s, got %s", msgConnectionAck, got.Type)
	}
	writeMsg(t, conn, subscribeMsg("1", "subscription { ticker }", nil))
	if got := readMsg(t, conn); got.Type != msgNext {
		t.Fatalf("expected %s, got %s", msgNext, got.Type)
	}
	return conn
}

// eventuallyLive waits for the resolver's live-stream count to reach want.
func eventuallyLive(t *testing.T, res *drainResolver, want int64, why string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if res.live.Load() == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("%d subscription streams are still running, want %d — %s", res.live.Load(), want, why)
}

// The gate for the shutdown half.
//
// 🔴 net/http's Shutdown NEITHER CLOSES NOR WAITS FOR HIJACKED CONNECTIONS, and every
// WebSocket here is hijacked. Before SubscriptionHandler.Shutdown existed, this
// server reported an orderly stop with every one of these connections still running
// its read loop, its ping ticker and its pump — each pump holding what is a live
// tenant-scoped NATS subscription in production, on a pod that had just been declared
// drained. They ended at process exit, which reaches the client as a torn TCP
// connection rather than as a close frame.
//
// The two assertions after ExecuteStop are deliberately NOT polled: Shutdown's
// contract is that when it returns, the connections are done. Only the resolver
// count is polled, because a stream's own goroutine and the pump feeding from it
// unwind independently.
func TestExecuteStopEndsLiveSubscriptions(t *testing.T) {
	const n = 8
	gql, res, addr := startDrainServer(t, 0)

	conns := make([]*websocket.Conn, 0, n)
	for i := 0; i < n; i++ {
		conns = append(conns, dialSubscribed(t, addr))
	}
	if got := gql.subscriptions.liveConnections(); got != n {
		t.Fatalf("liveConnections() = %d before the stop, want %d", got, n)
	}
	if got := res.live.Load(); got != n {
		t.Fatalf("%d subscription streams are live before the stop, want %d", got, n)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := gql.ExecuteStop(ctx); err != nil {
		t.Fatalf("ExecuteStop: %v", err)
	}

	if got := gql.subscriptions.liveConnections(); got != 0 {
		t.Errorf("liveConnections() = %d after ExecuteStop returned, want 0 — the server reported an "+
			"orderly stop with subscription connections it had walked away from", got)
	}
	eventuallyLive(t, res, 0, "ExecuteStop returned, so nothing is left to cancel them")

	// And the clients were TOLD, rather than discovering it as a torn connection when
	// the process exited. Reads run here, on the test goroutine, over sockets that are
	// already closed — never in a goroutine, where a failed assertion would unwind
	// only that goroutine and the test would pass in silence.
	for i, conn := range conns {
		err := readUntilError(conn)
		var closeErr *websocket.CloseError
		if !errors.As(err, &closeErr) {
			t.Errorf("connection %d ended with %v, want a WebSocket close frame", i, err)
			continue
		}
		if closeErr.Code != websocket.CloseGoingAway {
			t.Errorf("connection %d was closed with code %d, want %d (going away) — a drained pod is "+
				"exactly the case a client should reconnect from", i, closeErr.Code, websocket.CloseGoingAway)
		}
	}
}

// readUntilError drains whatever is buffered and returns the error that ends the
// connection.
func readUntilError(conn *websocket.Conn) error {
	_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	for i := 0; i < 1000; i++ {
		if _, _, err := conn.ReadMessage(); err != nil {
			return err
		}
	}
	return errors.New("the connection kept delivering frames and never ended")
}

// The counterweight to the test above: the ordinary path, where the CLIENT hangs up.
// Without it, a Shutdown that tore everything down would look identical to a handler
// that had never tracked a connection in the first place.
func TestClientDisconnectReleasesItsSubscriptions(t *testing.T) {
	const n = 8
	gql, res, addr := startDrainServer(t, 0)

	conns := make([]*websocket.Conn, 0, n)
	for i := 0; i < n; i++ {
		conns = append(conns, dialSubscribed(t, addr))
	}
	if got := gql.subscriptions.liveConnections(); got != n {
		t.Fatalf("liveConnections() = %d, want %d", got, n)
	}

	for _, conn := range conns {
		_ = conn.Close()
	}

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && gql.subscriptions.liveConnections() > 0 {
		time.Sleep(10 * time.Millisecond)
	}
	if got := gql.subscriptions.liveConnections(); got != 0 {
		t.Errorf("liveConnections() = %d after every client hung up, want 0", got)
	}
	eventuallyLive(t, res, 0, "every client hung up")
}

// 🔴 A STOP MUST LEAVE THE SERVER STARTABLE. LifecycleComponent's contract says a
// start "may happen on startup or after stop", and this repository has already been
// bitten four times by teardown that latched something permanently — http.Server's
// own shuttingDown flag being the canonical one, which is why ExecuteStart builds a
// fresh HttpServer each time. SubscriptionHandler is built ONCE, in
// ExecuteInitialize, and is therefore the same object across a restart: if Shutdown
// recorded it as closed, subscriptions would come back dead while every probe
// reported healthy.
func TestSubscriptionsWorkAgainAfterARestart(t *testing.T) {
	gql, res, addr := startDrainServer(t, 0)

	conn := dialSubscribed(t, addr)
	if err := gql.ExecuteStop(context.Background()); err != nil {
		t.Fatalf("ExecuteStop: %v", err)
	}
	_ = readUntilError(conn)
	eventuallyLive(t, res, 0, "the first stop ended it")

	if err := gql.ExecuteStart(context.Background()); err != nil {
		t.Fatalf("restart refused: %v", err)
	}
	restarted := gql.Server.Addr()
	if restarted == addr {
		t.Fatal("the restarted server reports the first server's address; it was reused rather than rebuilt")
	}

	// The whole point: a subscription over the restarted server still streams.
	dialSubscribed(t, restarted)
	if got := gql.subscriptions.liveConnections(); got != 1 {
		t.Errorf("liveConnections() = %d after a restart, want 1 — the handler refused to serve again", got)
	}
	if got := res.live.Load(); got != 1 {
		t.Errorf("%d subscription streams are live after a restart, want 1", got)
	}
}

// The gate for the read-limit half.
//
// 🔴 THE FRAME IS READ BEFORE THE CONNECTION IS AUTHENTICATED. The credential arrives
// inside connection_init — the first frame — so an unbounded read is reachable by any
// peer that can complete the upgrade, and gorilla's read limit is unlimited until
// something sets it (it gates its own check on a positive value). The sibling on the
// SAME /graphql route, an HTTP POST, has been capped all along.
func TestOversizedSubscriptionFrameIsRefused(t *testing.T) {
	const limit = 4096
	_, _, addr := startDrainServer(t, limit)

	dialer := websocket.Dialer{Subprotocols: []string{wsSubprotocol}}
	conn, _, err := dialer.Dial("ws://"+addr+"/graphql", nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))

	// A connection_init whose params are comfortably over the ceiling. Nothing has
	// authenticated at this point, which is the reason the ceiling has to be here.
	oversized, err := json.Marshal(map[string]interface{}{"token": strings.Repeat("A", limit*4)})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	// 🔴 THE WRITE'S OUTCOME IS RECORDED, NEVER FATAL, BECAUSE A REFUSAL RESETS IT.
	// The server stops reading the moment the frame passes the ceiling and closes the
	// socket, which leaves the rest of this frame unread and makes the kernel answer
	// the remainder of the write with RST. So `connection reset by peer` here is what
	// success looks like from the peer's side, not a failure of it — a frame large
	// enough to matter cannot be refused any other way. Whether it lands or is reset
	// is a socket-buffer race (measured: ~1% at this size, ~100% at a few MiB), so
	// treating it as a precondition made the ASSERTION BELOW — the only thing that
	// proves the refusal carries 1009 rather than an arbitrary disconnect — skippable
	// by timing. Data already queued for us is still delivered after a reset, so the
	// close frame arrives either way and every assertion below runs either way.
	writeErr := conn.WriteJSON(wsMessage{Type: msgConnectionInit, Payload: oversized})

	_, _, err = conn.ReadMessage()
	var closeErr *websocket.CloseError
	if !errors.As(err, &closeErr) {
		t.Fatalf("an oversized frame was answered with %v; want the connection closed — the frame was "+
			"accepted and materialised on the heap instead (writing it returned %v)", err, writeErr)
	}
	if closeErr.Code != websocket.CloseMessageTooBig {
		t.Errorf("an oversized frame closed the connection with code %d, want %d (message too big)",
			closeErr.Code, websocket.CloseMessageTooBig)
	}

	// The counterweight: refusing one peer must not take the server with it. A ceiling
	// that closed the listener, or wedged the handler, would satisfy the assertion
	// above and be far worse than the hole it closed.
	resp, err := http.Get("http://" + addr + "/healthz")
	if err != nil {
		t.Fatalf("GET /healthz after the refusal: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("GET /healthz after the refusal = %d, want 200", resp.StatusCode)
	}
	dialSubscribed(t, addr) // a well-behaved client still connects, subscribes and streams
}

// 🔴 ZERO MUST MEAN THE DEFAULT, NOT UNLIMITED. gorilla gates its check on
// `readLimit > 0`, so every path that can leave the field at zero — a dropped
// assignment, a handler a test builds, an instance config that never ran
// ApplyDefaults — restores the unbounded read, silently, because an absent ceiling
// looks exactly like a generous one until someone sends a large frame.
func TestZeroMaxMessageBytesMeansTheDefaultNotUnlimited(t *testing.T) {
	if got := (&SubscriptionHandler{}).readLimit(); got != config.DefaultGraphQLMaxSubscriptionMessageBytes {
		t.Errorf("readLimit() on an unset handler = %d, want %d", got, config.DefaultGraphQLMaxSubscriptionMessageBytes)
	}
	if got := (&SubscriptionHandler{MaxMessageBytes: -1}).readLimit(); got != config.DefaultGraphQLMaxSubscriptionMessageBytes {
		t.Errorf("readLimit() on a negative handler = %d, want %d", got, config.DefaultGraphQLMaxSubscriptionMessageBytes)
	}
	if got := (&SubscriptionHandler{MaxMessageBytes: 1234}).readLimit(); got != 1234 {
		t.Errorf("readLimit() = %d, want the configured 1234", got)
	}
	// And the constructor agrees, so a handler built the production way is bounded
	// even if nothing ever assigns the field.
	h := NewSubscriptionHandler(MustParseSchema(drainSchema, &drainResolver{}), nil, nil)
	if h.readLimit() != config.DefaultGraphQLMaxSubscriptionMessageBytes {
		t.Errorf("NewSubscriptionHandler read limit = %d, want %d",
			h.readLimit(), config.DefaultGraphQLMaxSubscriptionMessageBytes)
	}
}

// The two transports on the ONE /graphql route must not disagree about how large an
// operation may be. A POST is capped by DefaultGraphQLMaxBodyBytes; a subscribe frame
// carries the same thing — a GraphQL document and its variables — over a WebSocket.
// This package can see both constants, which is why the check belongs here: moving
// one and not the other leaves a hole in whichever is larger, and neither file's
// prose would notice.
func TestSubscriptionCeilingMatchesTheHttpBodyCeiling(t *testing.T) {
	if config.DefaultGraphQLMaxSubscriptionMessageBytes != int64(DefaultGraphQLMaxBodyBytes) {
		t.Errorf("the subscription frame ceiling is %d and the HTTP body ceiling is %d; one operation over "+
			"two transports on one route should not have two answers",
			config.DefaultGraphQLMaxSubscriptionMessageBytes, DefaultGraphQLMaxBodyBytes)
	}
}

// The operator's key has to reach the handler. Without this the ceiling is real but
// unmovable, and a deployment that needs a larger one would edit a value nothing
// reads.
func TestExecuteInitializeCarriesTheConfiguredReadLimit(t *testing.T) {
	const configured = 7 << 20

	ms := &core.Microservice{InstanceId: "test", FunctionalArea: "limit-wiring"}
	ms.UseMetricsRegistry(prometheus.NewRegistry())
	ms.InstanceConfiguration.Infrastructure.GraphQL.MaxSubscriptionMessageBytes = configured

	gql := &GraphQLManager{
		Microservice: ms,
		Schema:       MustParseSchema(drainSchema, &drainResolver{}),
		Gate:         core.NewReadinessGate(),
	}
	if err := gql.ExecuteInitialize(context.Background()); err != nil {
		t.Fatalf("ExecuteInitialize: %v", err)
	}
	if got := gql.subscriptions.readLimit(); got != configured {
		t.Errorf("the subscription handler's read limit = %d, want the configured %d", got, configured)
	}
}

// ExecuteStop must still be a no-op on a manager that never started, now that it has
// a second thing to tear down. A teardown runs after a startup that refused partway
// through, and a panic there replaces the real error with its own.
func TestStopBeforeAnyStartWithSubscriptionsIsANoOp(t *testing.T) {
	ms := &core.Microservice{InstanceId: "test", FunctionalArea: "never-started-subs"}
	ms.UseMetricsRegistry(prometheus.NewRegistry())
	gql := &GraphQLManager{
		Microservice: ms,
		Schema:       MustParseSchema(drainSchema, &drainResolver{}),
		Gate:         core.NewReadinessGate(),
	}
	if err := gql.ExecuteInitialize(context.Background()); err != nil {
		t.Fatalf("ExecuteInitialize: %v", err)
	}
	if err := gql.ExecuteStop(context.Background()); err != nil {
		t.Errorf("ExecuteStop before any start = %v, want nil", err)
	}
	// And on a manager that never even initialised, where the handler is nil.
	if err := (&GraphQLManager{}).ExecuteStop(context.Background()); err != nil {
		t.Errorf("ExecuteStop on an uninitialised manager = %v, want nil", err)
	}
}

// httptest, rather than the manager, so this covers the handler on its own: a
// Shutdown with nothing live must not block, and must not be confused by being
// called twice.
func TestShutdownWithNoConnectionsIsIdempotent(t *testing.T) {
	h := NewSubscriptionHandler(MustParseSchema(drainSchema, &drainResolver{}), nil, nil)
	srv := httptest.NewServer(h)
	defer srv.Close()

	for i := 0; i < 3; i++ {
		if err := h.Shutdown(context.Background()); err != nil {
			t.Fatalf("Shutdown %d: %v", i, err)
		}
	}
	// And it did not latch: the handler still serves.
	dialer := websocket.Dialer{Subprotocols: []string{wsSubprotocol}}
	conn, _, err := dialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	if err != nil {
		t.Fatalf("dial after Shutdown: %v", err)
	}
	defer conn.Close()
	_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	writeMsg(t, conn, wsMessage{Type: msgConnectionInit})
	if got := readMsg(t, conn); got.Type != msgConnectionAck {
		t.Errorf("after Shutdown the handler answered %s, want %s — it latched itself closed",
			got.Type, msgConnectionAck)
	}
	if err := h.Shutdown(context.Background()); err != nil {
		t.Fatalf("final Shutdown: %v", err)
	}
}
