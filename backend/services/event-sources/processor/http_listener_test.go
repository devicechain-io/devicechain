// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/devicechain-io/dc-event-sources/config"
	"github.com/devicechain-io/dc-event-sources/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// 🔴 EVERY ASSERTION IN THIS FILE RUNS ON THE TEST GOROUTINE. The listener serves on its
// own goroutines, and an assertion made there would be a silent pass: FailNow's
// runtime.Goexit unwinds only the goroutine it runs on. Results come back through
// counters and returned values, and are asserted here.

// startSlowClient opens a raw TCP connection to a listening source and writes the given
// bytes without completing the request, then waits for the server to do something about
// it. It returns how long the server took to answer or hang up, and whether it did so at
// all within the patience given.
//
// The measurement is what makes this a bound rather than a mood: a listener that never
// hangs up is indistinguishable from one whose bound is simply longer than the test's
// patience, so the caller asserts on the elapsed time, not merely on the outcome.
func startSlowClient(t *testing.T, addr string, partial string, patience time.Duration) (time.Duration, bool) {
	t.Helper()
	conn, err := net.Dial("tcp", addr)
	require.NoError(t, err)
	defer conn.Close()

	_, err = conn.Write([]byte(partial))
	require.NoError(t, err)

	require.NoError(t, conn.SetReadDeadline(time.Now().Add(patience)))
	started := time.Now()
	buf := make([]byte, 512)
	_, err = conn.Read(buf)
	elapsed := time.Since(started)
	if err != nil {
		if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
			// The read deadline expired with the server still holding the connection
			// open — the hold outlived the test's patience.
			return elapsed, false
		}
	}
	// Either the server answered or it closed the connection; both end the hold.
	return elapsed, true
}

// postCanonicalEventWithoutKeepAlive posts the canonical event and then lets the
// connection close, rather than leaving it in a client's idle pool. Anything asserting
// on what happens when a SERVED connection ends has to make it end.
func postCanonicalEventWithoutKeepAlive(addr string) (int, error) {
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		return 0, fmt.Errorf("splitting listener address %q: %w", addr, err)
	}
	client := &http.Client{
		Timeout:   5 * time.Second,
		Transport: &http.Transport{DisableKeepAlives: true},
	}
	defer client.CloseIdleConnections()
	resp, err := client.Post(fmt.Sprintf("http://127.0.0.1:%s/inst-1/acme/events", port),
		"application/json", strings.NewReader(canonicalMeasurementBody))
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode, nil
}

// newListeningSource builds a source with the given listener bounds, starts it on an
// OS-chosen port, and registers its stop. It returns the source and the address it is
// actually serving on.
func newListeningSource(t *testing.T, id string, ingest config.HttpIngest,
	earlyClose func(string)) (*HttpEventSource, *capturedDecode, string) {
	t.Helper()
	dec := &capturedDecode{}
	es, err := NewHttpEventSource(id, map[string]string{}, "inst-1", ingest,
		NewJsonDecoder(map[string]string{}, 0),
		func(string, []byte) {},
		func(source string, tenant string, event *model.UnresolvedEvent, payload interface{}, captureSeq uint64) error {
			dec.called = true
			dec.tenant = tenant
			dec.event = event
			return dec.publishErr
		},
		func(string, string, []byte, error) error { return nil },
		nil, earlyClose)
	require.NoError(t, err)
	es.Port = 0 // let the OS choose, so the test needs no fixed port

	ctx := context.Background()
	require.NoError(t, es.Initialize(ctx))
	require.NoError(t, es.Start(ctx))
	t.Cleanup(func() { _ = es.Stop(context.Background()) })
	require.NotNil(t, es.server)
	return es, dec, es.server.Addr()
}

// A device that has not finished sending its headers is cut off at the CONFIGURED bound,
// not at the shared constant the management surfaces use.
//
// The bound applies to a device-facing listener whose callers may be on NB-IoT, 2G or
// satellite links, where a multi-second round trip before the request is complete is
// ordinary. A device that exceeds it has its connection closed with nothing in the
// pipeline to attribute it to — the request never became an event — so the value has to
// be the operator's to set.
func TestHttpEventSource_HeaderTimeoutIsConfigured(t *testing.T) {
	_, _, addr := newListeningSource(t, "http-slow-headers",
		config.HttpIngest{HeaderTimeoutSeconds: 1}, nil)

	// Headers begun and never finished: no blank line, so net/http is still waiting for
	// the rest of the request head.
	elapsed, ended := startSlowClient(t, addr, "POST /inst-1/acme/events HTTP/1.1\r\nHost: example\r\n", 4*time.Second)

	require.True(t, ended, "a request that never finishes its headers must be closed, not held")
	assert.Less(t, elapsed, 3*time.Second,
		"the connection must be closed at the CONFIGURED 1s bound; a close at ~5s is the shared default, which means the configured value never reached the listener")
}

// The whole-request read is bounded too. The body was capped in BYTES and not in TIME,
// so a client that dribbles one held a connection and a goroutine indefinitely — the
// larger of the two holds, and the only one that was unbounded at all.
func TestHttpEventSource_RequestTimeoutBoundsASlowBody(t *testing.T) {
	var closedEarly atomic.Int64
	_, _, addr := newListeningSource(t, "http-slow-body",
		config.HttpIngest{RequestTimeoutSeconds: 1}, func(string) { closedEarly.Add(1) })

	// Complete headers promising a body, then a fragment of one. The header bound is
	// satisfied and left at its default, so only the whole-request bound can end this.
	partial := "POST /inst-1/acme/events HTTP/1.1\r\nHost: example\r\nContent-Length: 400\r\n\r\n{\"device\":"
	elapsed, ended := startSlowClient(t, addr, partial, 4*time.Second)

	require.True(t, ended, "a request that never finishes its body must not be held indefinitely")
	assert.Less(t, elapsed, 3*time.Second,
		"the read must end at the configured 1s whole-request bound rather than waiting on a body that is not coming")

	// A body cut short is NOT an early close: its headers were complete, so its request
	// did reach the handler. The counter names connections that produced no request at
	// all, and it has to stay that narrow to mean anything.
	time.Sleep(300 * time.Millisecond)
	assert.Equal(t, int64(0), closedEarly.Load(),
		"a request whose headers completed must not be counted as a connection that never delivered one")
}

// 🔑 THE COUNTERWEIGHT TO BOTH BOUNDS. A listener that closed every connection would
// pass both tests above. A normal request well inside the bounds must still be served,
// and must still reach the pipeline.
func TestHttpEventSource_NormalRequestIsServedInsideTheBounds(t *testing.T) {
	_, dec, addr := newListeningSource(t, "http-normal",
		config.HttpIngest{HeaderTimeoutSeconds: 1, RequestTimeoutSeconds: 1}, nil)

	code, err := postCanonicalEvent(addr)
	require.NoError(t, err)
	assert.Equal(t, http.StatusAccepted, code, "a prompt request must be accepted, not cut off")
	assert.True(t, dec.called, "the accepted event must reach the pipeline")
}

// A header-timeout close is otherwise invisible: net/http closes the connection itself,
// before any handler runs, so there is no event, no error and nothing to tell an
// operator whether the bound they can now configure is costing them traffic.
//
// The counter moves on a connection that closed without delivering a request, and does
// NOT move on a served one — the second half being what stops it counting every device
// that ever disconnects.
func TestHttpEventSource_CountsConnectionsClosedBeforeAnyRequest(t *testing.T) {
	var closedEarly atomic.Int64
	var countedSource atomic.Value
	count := func(source string) {
		countedSource.Store(source)
		closedEarly.Add(1)
	}

	_, _, addr := newListeningSource(t, "http-counted",
		config.HttpIngest{HeaderTimeoutSeconds: 1}, count)

	// A served request whose connection then CLOSES must not be counted.
	//
	// 🔴 THE CLOSE IS THE WHOLE ASSERTION, WHICH IS WHY KEEP-ALIVE IS TURNED OFF. The
	// count is only ever taken when a connection ends, so a served request left sitting
	// in a client's idle pool never reaches the code under test — and this half of the
	// test passed against a deliberately broken build until the connection was made to
	// close. A control that cannot fail is not a control.
	code, err := postCanonicalEventWithoutKeepAlive(addr)
	require.NoError(t, err)
	require.Equal(t, http.StatusAccepted, code)
	time.Sleep(300 * time.Millisecond)
	require.Equal(t, int64(0), closedEarly.Load(),
		"a connection that delivered a request must never be counted as one that did not")

	// A connection cut off before its request was read is counted.
	_, ended := startSlowClient(t, addr, "POST /inst-1/acme/events HTTP/1.1\r\nHost: example\r\n", 4*time.Second)
	require.True(t, ended, "the slow-header connection must be closed by the server")

	// The state hook runs on the connection's own goroutine, so the count is observed
	// with a bounded wait rather than assumed to have landed already.
	deadline := time.Now().Add(2 * time.Second)
	for closedEarly.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	assert.Equal(t, int64(1), closedEarly.Load(), "a connection closed before any request must be counted")
	assert.Equal(t, "http-counted", countedSource.Load(), "the count must be attributed to the source that holds the listener")
}

// 🔑 THE COUNTERWEIGHT TO THE PORT VALIDATION, on the serving side. The configuration
// gates prove that a document naming two distinct listener ports still loads; this
// proves that two listeners in this one process actually serve when it does. A
// validation that refused everything, or a change that left the second source bound to
// nothing, would pass a refusal-only suite.
func TestHttpEventSource_TwoSourcesOnDistinctPortsBothServe(t *testing.T) {
	_, firstDec, firstAddr := newListeningSource(t, "http1", config.HttpIngest{}, nil)
	_, secondDec, secondAddr := newListeningSource(t, "http2", config.HttpIngest{}, nil)
	require.NotEqual(t, firstAddr, secondAddr, "two started sources must hold two different addresses")

	for _, listener := range []struct {
		name string
		addr string
		dec  *capturedDecode
	}{
		{"http1", firstAddr, firstDec},
		{"http2", secondAddr, secondDec},
	} {
		code, err := postCanonicalEvent(listener.addr)
		require.NoErrorf(t, err, "source %s must answer on the address it reports", listener.name)
		assert.Equalf(t, http.StatusAccepted, code, "source %s must accept events", listener.name)
		assert.Truef(t, listener.dec.called, "source %s must hand its event to the pipeline", listener.name)
	}
}

// The bind error remains the backstop, and it must not read like the configuration
// refusal. Configuration validation sees only what configuration knows; something
// outside this process can still hold the port, and that is an environment problem with
// a different remedy from an operator writing one port twice.
func TestHttpEventSource_BindFailureIsDistinctFromAConfigCollision(t *testing.T) {
	blocker, err := net.Listen("tcp", ":0")
	require.NoError(t, err)
	defer blocker.Close()

	es, _, _ := newTestHttpSource(t, nil)
	es.Port = blocker.Addr().(*net.TCPAddr).Port

	ctx := context.Background()
	require.NoError(t, es.Initialize(ctx))
	err = es.Start(ctx)

	require.Error(t, err, "a port held outside this process must still fail the start")
	assert.Contains(t, err.Error(), fmt.Sprintf(":%d", es.Port), "the bind failure must name the address")
	assert.NotContains(t, strings.ToLower(err.Error()), "two listeners",
		"a bind failure is an environment problem and must not be phrased as the configuration collision")
}
