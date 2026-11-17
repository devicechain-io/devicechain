// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package upload

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// A blocked body proves admission by observing the first read, rather than timing
// a request or assuming that acquiring an artifact lease means streaming started.
type heldShareBody struct {
	io.Reader
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (b *heldShareBody) Read(p []byte) (int, error) {
	b.once.Do(func() { close(b.started) })
	<-b.release
	return b.Reader.Read(p)
}

func (e *env) shareRequest(tenant, token string, body io.Reader) *http.Request {
	e.t.Helper()
	r := httptest.NewRequest(http.MethodPut, e.url(token), body)
	r.SetPathValue("token", token)
	r.ContentLength = 4
	r.Header.Set("Authorization", "Bearer "+e.writer(tenant))
	return r
}

func (e *env) holdShare(tenant, token string) func() {
	e.t.Helper()
	b := &heldShareBody{Reader: strings.NewReader("data"), started: make(chan struct{}), release: make(chan struct{})}
	r := e.shareRequest(tenant, token, b)
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		w := httptest.NewRecorder()
		e.h.ServeHTTP(w, r)
		done <- w
	}()
	var finish sync.Once
	stop := func() {
		finish.Do(func() {
			close(b.release)
			select {
			case w := <-done:
				require.Equal(e.t, http.StatusOK, w.Code, w.Body.String())
			case <-time.After(5 * time.Second):
				e.t.Error("held upload did not finish")
			}
		})
	}
	e.t.Cleanup(stop)
	select {
	case <-b.started:
	case w := <-done:
		// Put the response back so cleanup can consume it too.
		done <- w
		e.t.Fatalf("upload refused before reading body: %d %s", w.Code, w.Body.String())
	case <-time.After(5 * time.Second):
		e.t.Fatal("upload did not start reading its body")
	}
	return stop
}

func (e *env) refuseShare(tenant, token string, want int) *httptest.ResponseRecorder {
	e.t.Helper()
	w := httptest.NewRecorder()
	e.h.ServeHTTP(w, e.shareRequest(tenant, token, panicBody{e.t}))
	require.Equal(e.t, want, w.Code, w.Body.String())
	require.Equal(e.t, "close", w.Header().Get("Connection"))
	if want == http.StatusServiceUnavailable {
		require.Equal(e.t, "5", w.Header().Get("Retry-After"))
	}
	require.False(e.t, e.state(tenant, token).UploadingSince.Valid)
	return w
}

func TestUploadTenantShareAndGlobalCap(t *testing.T) {
	e := newEnvWith(t, 4)
	for tenant, tokens := range map[string][]string{
		"a": {"a-1", "a-2", "a-3"}, "b": {"b-1"}, "c": {"c-1"}, "d": {"d-1"},
	} {
		for _, token := range tokens {
			e.declare(tenant, token, []byte("data"))
		}
	}
	finishA := e.holdShare("a", "a-1")
	e.holdShare("a", "a-2")
	w := e.refuseShare("a", "a-3", http.StatusServiceUnavailable)
	require.Contains(t, w.Body.String(), "its share of 2 concurrent uploads")
	e.holdShare("b", "b-1")
	finishC := e.holdShare("c", "c-1")
	// Repeated global refusals must not consume D's tenant share.
	e.refuseShare("d", "d-1", http.StatusServiceUnavailable)
	e.refuseShare("d", "d-1", http.StatusServiceUnavailable)
	finishC()
	e.holdShare("d", "d-1")
	finishA()
	e.holdShare("a", "a-3")
}

func TestUploadTenantShareRoundsUp(t *testing.T) {
	e := newEnvWith(t, 3)
	for _, token := range []string{"a-1", "a-2", "a-3"} {
		e.declare("a", token, []byte("data"))
	}
	e.holdShare("a", "a-1")
	e.holdShare("a", "a-2")
	w := e.refuseShare("a", "a-3", http.StatusServiceUnavailable)
	require.Contains(t, w.Body.String(), "its share of 2 concurrent uploads")
}

func TestUploadClaimRefusalsReleaseTenantShare(t *testing.T) {
	e := newEnvWith(t, 4)
	e.declare("a", "a-1", []byte("data"))
	e.declare("a", "a-2", []byte("data"))
	e.holdShare("a", "a-1")
	for range 3 {
		w := httptest.NewRecorder()
		e.h.ServeHTTP(w, e.shareRequest("a", "a-1", panicBody{t}))
		require.Equal(t, http.StatusConflict, w.Code, w.Body.String())
	}
	e.holdShare("a", "a-2")
}

func TestFailedUploadReleasesTenantShare(t *testing.T) {
	e := newEnvWith(t, 1)
	e.declare("a", "a-1", []byte("data"))
	status(t, e.put(e.writer("a"), "a-1", strings.NewReader("evil")), http.StatusUnprocessableEntity)
	e.holdShare("a", "a-1")
}
