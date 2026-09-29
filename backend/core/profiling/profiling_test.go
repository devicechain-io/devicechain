// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package profiling

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"runtime/pprof"
	"runtime/trace"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// None of these may call t.Parallel: the CPU profiler and the tracer are one per process.

func serve(t *testing.T) (*httptest.Server, chan struct{}) {
	t.Helper()
	stop := make(chan struct{})
	srv := httptest.NewServer(Handler(stop))
	t.Cleanup(srv.Close)
	return srv, stop
}

func fetch(t *testing.T, method, url string) (int, http.Header, []byte) {
	t.Helper()
	req, err := http.NewRequest(method, url, nil)
	require.NoError(t, err)
	resp, err := (&http.Client{Timeout: 20 * time.Second}).Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return resp.StatusCode, resp.Header, body
}

// cpuStarted replaces the test hook with one that signals, restoring it afterwards.
func cpuStarted(t *testing.T) <-chan struct{} {
	t.Helper()
	ch := make(chan struct{}, 1)
	prev := cpuProfileStarted
	cpuProfileStarted = func() { ch <- struct{}{} }
	t.Cleanup(func() { cpuProfileStarted = prev })
	return ch
}

func TestTheIndexListsExactlyWhatIsServed(t *testing.T) {
	srv, _ := serve(t)
	status, hdr, body := fetch(t, http.MethodGet, srv.URL+Prefix)
	require.Equal(t, http.StatusOK, status)
	assert.Equal(t, "nosniff", hdr.Get("X-Content-Type-Options"))
	for _, name := range []string{"allocs", "goroutine", "heap", "threadcreate", "profile", "trace"} {
		assert.Contains(t, string(body), Prefix+name)
	}
	for _, name := range []string{"block", "mutex", "cmdline", "symbol"} {
		assert.NotContains(t, string(body), Prefix+name)
	}
}

func TestEveryServedProfileAnswers(t *testing.T) {
	srv, _ := serve(t)
	for _, name := range Served {
		status, hdr, body := fetch(t, http.MethodGet, srv.URL+Prefix+name)
		assert.Equal(t, http.StatusOK, status, name)
		assert.Equal(t, "application/octet-stream", hdr.Get("Content-Type"), name)
		assert.True(t, bytes.HasPrefix(body, []byte{0x1f, 0x8b}), "%s is not a gzipped proto profile", name)
	}
	status, hdr, body := fetch(t, http.MethodGet, srv.URL+Prefix+"heap?debug=1&gc=1")
	assert.Equal(t, http.StatusOK, status)
	assert.Equal(t, "text/plain; charset=utf-8", hdr.Get("Content-Type"))
	assert.True(t, strings.HasPrefix(string(body), "heap profile: "), "%.60q", body)
	status, _, body = fetch(t, http.MethodGet, srv.URL+Prefix+"goroutine?debug=2")
	assert.Equal(t, http.StatusOK, status)
	assert.True(t, strings.HasPrefix(string(body), "goroutine "), "%.60q", body)
}

// Block and mutex exist in the runtime but are never sampled here, so they would be
// empty — and an empty contention profile reads as "no contention".
func TestUncollectedAndUnknownProfilesAre404(t *testing.T) {
	srv, _ := serve(t)
	for _, name := range []string{"block", "mutex"} {
		status, _, body := fetch(t, http.MethodGet, srv.URL+Prefix+name)
		assert.Equal(t, http.StatusNotFound, status, name)
		assert.Contains(t, string(body), "not collected", name)
	}
	for _, name := range []string{"nosuch", "cmdline", "symbol"} {
		status, _, body := fetch(t, http.MethodGet, srv.URL+Prefix+name)
		assert.Equal(t, http.StatusNotFound, status, name)
		assert.Contains(t, string(body), "unknown profile", name)
	}
	status, _, _ := fetch(t, http.MethodPost, srv.URL+Prefix)
	assert.Equal(t, http.StatusMethodNotAllowed, status)
}

func TestBadParametersAreRefused(t *testing.T) {
	srv, _ := serve(t)
	for _, path := range []string{
		"heap?seconds=5",
		"heap?debug=3",
		"heap?debug=x",
		"profile?seconds=0",
		"profile?seconds=-1",
		"profile?seconds=61",
		"profile?seconds=abc",
		"trace?seconds=0",
		"trace?seconds=61",
	} {
		status, _, body := fetch(t, http.MethodGet, srv.URL+Prefix+path)
		assert.Equal(t, http.StatusBadRequest, status, "%s: %s", path, body)
	}
}

func TestACPUProfileIsReturned(t *testing.T) {
	srv, _ := serve(t)
	status, hdr, body := fetch(t, http.MethodGet, srv.URL+Prefix+"profile?seconds=1")
	require.Equal(t, http.StatusOK, status, "%s", body)
	assert.Equal(t, "application/octet-stream", hdr.Get("Content-Type"))
	assert.True(t, bytes.HasPrefix(body, []byte{0x1f, 0x8b}), "not a gzipped proto profile")
}

func TestATraceIsReturned(t *testing.T) {
	srv, _ := serve(t)
	status, _, body := fetch(t, http.MethodGet, srv.URL+Prefix+"trace?seconds=1")
	require.Equal(t, http.StatusOK, status, "%s", body)
	assert.True(t, bytes.HasPrefix(body, []byte("go 1.")), "not an execution trace: %.20q", body)
}

// One CPU profile and one trace at a time; the second of each is told so.
func TestASecondProfileIsAConflict(t *testing.T) {
	srv, _ := serve(t)

	require.NoError(t, pprof.StartCPUProfile(io.Discard))
	status, _, body := fetch(t, http.MethodGet, srv.URL+Prefix+"profile?seconds=1")
	pprof.StopCPUProfile()
	assert.Equal(t, http.StatusConflict, status, "%s", body)

	require.NoError(t, trace.Start(io.Discard))
	status, hdr, body := fetch(t, http.MethodGet, srv.URL+Prefix+"trace?seconds=1")
	trace.Stop()
	assert.Equal(t, http.StatusConflict, status, "%s", body)
	assert.Empty(t, hdr.Get("Content-Disposition"))
}

// A CPU profile abandoned by the owner's stop answers 503, not a short 200.
func TestStopAbandonsACPUProfileWith503(t *testing.T) {
	started := cpuStarted(t)
	srv, stop := serve(t)

	type result struct {
		status int
		body   []byte
	}
	done := make(chan result, 1)
	go func() {
		resp, err := http.Get(srv.URL + Prefix + "profile?seconds=30")
		if err != nil {
			done <- result{status: -1}
			return
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		done <- result{status: resp.StatusCode, body: b}
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("the CPU profile never started")
	}
	close(stop)
	select {
	case r := <-done:
		assert.Equal(t, http.StatusServiceUnavailable, r.status)
		assert.Contains(t, string(r.body), "shutting down")
	case <-time.After(5 * time.Second):
		t.Fatal("the CPU profile was not abandoned")
	}
	// The profiler was released: a new profile can start.
	require.NoError(t, pprof.StartCPUProfile(io.Discard))
	pprof.StopCPUProfile()
}

// A trace abandoned by the owner's stop cuts the connection: the client must see an
// error, never a clean end to a trace that stopped part-way.
func TestStopAbandonsATraceWithACutConnection(t *testing.T) {
	srv, stop := serve(t)
	done := make(chan error, 1)
	go func() {
		resp, err := http.Get(srv.URL + Prefix + "trace?seconds=30")
		if err != nil {
			done <- err
			return
		}
		defer resp.Body.Close()
		_, err = io.ReadAll(resp.Body)
		done <- err
	}()
	require.Eventually(t, trace.IsEnabled, 5*time.Second, 5*time.Millisecond, "the trace never started")
	close(stop)
	select {
	case err := <-done:
		assert.Error(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("the trace was not abandoned")
	}
	// The client can see the cut connection a moment before the handler's trace.Stop
	// returns, so the tracer being released is waited for, within a bound far shorter
	// than the trace's 30 seconds.
	assert.Eventually(t, func() bool { return !trace.IsEnabled() }, 2*time.Second, 5*time.Millisecond,
		"the tracer was left on")
}

// churn keeps the scheduler busy until the test ends, so an execution trace produces
// data fast enough to fill a stalled client's socket buffers.
func churn(t *testing.T) {
	t.Helper()
	quit := make(chan struct{})
	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-quit:
					return
				default:
				}
				ch := make(chan struct{})
				go func() { close(ch) }()
				<-ch
			}
		}()
	}
	t.Cleanup(func() { close(quit); wg.Wait() })
}

// stalledClient requests a profile over a raw connection and never reads the reply, as
// a suspended curl or a stalled port-forward does. The connection is closed when the
// test ends, which is the only thing that would unblock a writer with no deadline.
func stalledClient(t *testing.T, addr, path string) {
	t.Helper()
	conn, err := net.Dial("tcp", addr)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	require.NoError(t, conn.(*net.TCPConn).SetReadBuffer(1)) // the kernel rounds it up to its minimum
	_, err = fmt.Fprintf(conn, "GET %s%s HTTP/1.1\r\nHost: x\r\n\r\n", Prefix, path)
	require.NoError(t, err)
}

func stalledTraceClient(t *testing.T, addr string, secs int) {
	t.Helper()
	stalledClient(t, addr, fmt.Sprintf("trace?seconds=%d", secs))
}

// stoppableServer serves Handler(stop) on a real http.Server, so a test can time a
// graceful Shutdown the way the owner of the listener runs one.
func stoppableServer(t *testing.T) (*http.Server, string, chan struct{}) {
	t.Helper()
	stop := make(chan struct{})
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	srv := &http.Server{Handler: Handler(stop), ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	return srv, ln.Addr().String(), stop
}

// shutdownPromptly closes stop and asserts a graceful shutdown then completes well
// inside its budget.
func shutdownPromptly(t *testing.T, srv *http.Server, stop chan struct{}) {
	t.Helper()
	close(stop)
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	began := time.Now()
	err := srv.Shutdown(ctx)
	took := time.Since(began)
	require.NoError(t, err, "the shutdown waited out its budget (%s) for a client that stopped reading", took)
	assert.Less(t, took, 3*time.Second)
}

// A client that stops reading must not keep the tracer on past the trace's duration:
// the response's overall write deadline fails the blocked write, and the runtime's
// trace writer then drains and stops. Before the deadline existed the tracer stayed on
// for as long as the client stayed stalled.
func TestAStalledClientCannotHoldTheTracerPastItsDuration(t *testing.T) {
	prev := writeMargin
	writeMargin = 500 * time.Millisecond
	t.Cleanup(func() { writeMargin = prev })
	churn(t)
	srv, _ := serve(t)

	stalledTraceClient(t, srv.Listener.Addr().String(), 1)
	require.Eventually(t, trace.IsEnabled, 5*time.Second, 5*time.Millisecond, "the trace never started")
	// 1 s of trace plus the 0.5 s margin; the rest is slack for a loaded CI runner.
	assert.Eventually(t, func() bool { return !trace.IsEnabled() }, 6*time.Second, 20*time.Millisecond,
		"a client that stopped reading held the tracer on past the trace's duration and its write margin")
}

// A stop reaches a trace whose client has stopped reading: the handler returns and a
// graceful shutdown completes promptly, with the tracer off. Before the stop moved the
// write deadline, trace.Stop blocked on the stalled write and the shutdown waited out
// its whole budget. (A stop that arrives after the trace's duration has ended, while the
// response is still being written, is not the handler's to end; core's test of the
// listener's stop covers it.)
func TestStopAbandonsATraceWhoseClientStoppedReading(t *testing.T) {
	churn(t)
	srv, addr, stop := stoppableServer(t)
	stalledTraceClient(t, addr, 30)
	require.Eventually(t, trace.IsEnabled, 5*time.Second, 5*time.Millisecond, "the trace never started")
	time.Sleep(3 * time.Second) // long enough for the stalled client's buffers to fill

	shutdownPromptly(t, srv, stop)
	assert.False(t, trace.IsEnabled(), "the tracer was left on")
}

// A profile some dependency registers with pprof.NewProfile is not served: pprof.Lookup
// finds it, and only the Served allow-list keeps it off this endpoint — where it would
// appear with whatever it records, and without anyone here having decided to serve it.
func TestAProfileADependencyRegistersIsNotServed(t *testing.T) {
	const name = "devicechain.test.registered"
	if pprof.Lookup(name) == nil {
		pprof.NewProfile(name)
	}
	srv, _ := serve(t)
	status, _, body := fetch(t, http.MethodGet, srv.URL+Prefix+name)
	assert.Equal(t, http.StatusNotFound, status, "%s", body)
	assert.Contains(t, string(body), "unknown profile")
}
