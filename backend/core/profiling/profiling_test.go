// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package profiling

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"runtime/pprof"
	"runtime/trace"
	"strings"
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
	assert.False(t, trace.IsEnabled())
}
