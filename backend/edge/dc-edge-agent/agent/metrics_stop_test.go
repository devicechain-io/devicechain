// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"io"
	"net"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// stop must release the metrics port before it returns, even when it follows start so
// closely that the serve loop has not begun: start and an immediate stop, through the real
// start, and a rebind of the same port, many times over. Whether a round lands in that
// window is up to the scheduler, so this is not the gate for it. The endpoint runs on
// core's HttpServer, and the window is held deterministically by that type's own test
// (TestHttpServerStopWaitsForAListenerCloseServeHasBegun); this one is here because it goes
// through start and stop the way Run does. The rebind is never retried: a retry is
// exactly what passes a port released late.
func TestEdgeMetricsStartThenImmediateStopReleasesThePort(t *testing.T) {
	healthz := func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }
	for i := 0; i < 300; i++ {
		m := &edgeMetrics{reg: prometheus.NewRegistry()}
		if err := m.start("127.0.0.1:0", healthz); err != nil {
			t.Fatalf("round %d: start: %v", i, err)
		}
		addr := m.addr
		m.stop()
		ln, err := net.Listen("tcp", addr)
		if err != nil {
			t.Fatalf("round %d: rebinding %s after stop returned: %v", i, addr, err)
		}
		ln.Close()
	}
}

// stop is a GRACEFUL stop: it waits for a scrape already being served to finish, and the
// scrape gets its answer. Closing the listener alone would also release the port, which
// is all the test above can see, so without this one the Shutdown in stop could be
// deleted and every test would still pass while stop stopped draining: it would return
// with the request still running, and an in-process restart would leave that connection
// and its serve goroutine behind.
func TestEdgeMetricsStopWaitsForAnInFlightScrape(t *testing.T) {
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	// A failed assertion must not leave the handler blocked, nor a stop waiting on it: the
	// cleanup releases the handler first, then waits for a stop that was started.
	stopped := make(chan struct{})
	var stopStarted bool
	t.Cleanup(func() {
		unblock()
		if !stopStarted {
			return
		}
		select {
		case <-stopped:
		case <-time.After(5 * time.Second):
		}
	})

	m := &edgeMetrics{reg: prometheus.NewRegistry()}
	healthz := func(w http.ResponseWriter, _ *http.Request) {
		entered <- struct{}{}
		<-release
		_, _ = io.WriteString(w, "drained")
	}
	if err := m.start("127.0.0.1:0", healthz); err != nil {
		t.Fatalf("start: %v", err)
	}

	type result struct {
		status int
		body   string
		err    error
	}
	results := make(chan result, 1)
	client := &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{DisableKeepAlives: true}}
	go func() {
		resp, err := client.Get("http://" + m.addr + "/healthz")
		if err != nil {
			results <- result{err: err}
			return
		}
		defer resp.Body.Close()
		body, err := io.ReadAll(resp.Body)
		results <- result{status: resp.StatusCode, body: string(body), err: err}
	}()

	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the scrape never reached the handler")
	}

	stopStarted = true
	go func() {
		m.stop()
		close(stopped)
	}()

	// stop's own deadline is 2s; this is well inside it, so a graceful stop is still
	// waiting here and one that does not drain has long since returned.
	select {
	case <-stopped:
		t.Fatal("stop returned while a scrape was still being served; it must wait for " +
			"in-flight requests to finish")
	case <-time.After(300 * time.Millisecond):
	}

	unblock()
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("stop did not return after the in-flight scrape was released")
	}
	var got result
	select {
	case got = <-results:
	case <-time.After(5 * time.Second):
		t.Fatal("the in-flight scrape never completed")
	}
	if got.err != nil || got.status != http.StatusOK || got.body != "drained" {
		t.Fatalf("in-flight scrape = (status %d, body %q, err %v), want (200, %q, nil)",
			got.status, got.body, got.err, "drained")
	}
}
