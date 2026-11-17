// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPollerCapPerDevice(t *testing.T) {
	h := newPullHarness(t, func(rt *CommandPullRoute) {
		rt.MaxPollersPerTenant = maxPollersPerDevice + 2
		rt.PollInterval = time.Hour
	})
	h.backend.tokens["tok-d3"] = &PullDevice{Token: "d3", OptedIn: true}
	// Exercise the handler with cancellable requests so completion and slot reuse are
	// synchronized, without waiting for the long-poll deadline.
	poll := func(tenant, device, wait string) (context.CancelFunc, <-chan *httptest.ResponseRecorder) {
		ctx, cancel := context.WithCancel(context.Background())
		t.Cleanup(cancel)
		r := httptest.NewRequest(http.MethodGet, "/commands?wait="+wait, nil).WithContext(ctx)
		r.SetPathValue("tenant", tenant)
		r.SetPathValue("device", device)
		r.Header.Set("Authorization", "Bearer tok-"+device)
		done := make(chan *httptest.ResponseRecorder, 1)
		go func() {
			w := httptest.NewRecorder()
			h.es.handleCommandPoll(w, r)
			done <- w
		}()
		return cancel, done
	}
	finish := func(done <-chan *httptest.ResponseRecorder) *httptest.ResponseRecorder {
		t.Helper()
		select {
		case w := <-done:
			return w
		case <-time.After(2 * time.Second):
			t.Fatal("poll did not finish")
			return nil
		}
	}
	held := func(tenant string, n int) {
		t.Helper()
		require.Eventually(t, func() bool {
			h.rt.mu.Lock()
			defer h.rt.mu.Unlock()
			return h.rt.pollers[tenant] == n
		}, time.Second, time.Millisecond)
	}
	// d1 fills its own allowance, and d2 waits beside it.
	cancelFirst, first := poll("acme", "d1", "25")
	held("acme", 1)
	for i := 1; i < maxPollersPerDevice; i++ {
		poll("acme", "d1", "25")
		held("acme", 1+i)
	}
	_, second := poll("acme", "d2", "25")
	held("acme", maxPollersPerDevice+1)
	_, over := poll("acme", "d1", "1")
	w := finish(over)
	require.Equal(t, http.StatusTooManyRequests, w.Code)
	assert.NotEmpty(t, w.Header().Get("Retry-After"))
	assert.Equal(t, 1, h.count("poll/poller_cap"))
	select {
	case <-second:
		t.Fatal("another device's poll must remain held")
	default:
	}
	// The refusal consumed no tenant slot: a third device can still wait.
	poll("acme", "d3", "25")
	held("acme", maxPollersPerDevice+2)
	_, instant := poll("acme", "d1", "0")
	assert.Equal(t, http.StatusNoContent, finish(instant).Code)
	// The same device token in a different tenant has an independent allowance.
	poll("globex", "d1", "25")
	held("globex", 1)
	cancelFirst()
	finish(first)
	held("acme", maxPollersPerDevice+1)
	_, again := poll("acme", "d1", "25")
	held("acme", maxPollersPerDevice+2)
	select {
	case <-again:
		t.Fatal("a finished poll must let the device wait again")
	default:
	}
}
