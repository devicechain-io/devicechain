// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// A restart made without a client of the caller's own is bounded: an API server that accepts the
// request and never answers returns an error within the bound, not a hang.
func TestARestartAgainstAHungAPIServerFailsWithinItsBound(t *testing.T) {
	release := make(chan struct{})
	hung := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer hung.Close()
	defer close(release)

	orig := restartTimeout
	restartTimeout = 300 * time.Millisecond
	t.Cleanup(func() { restartTimeout = orig })

	r := Restarter{APIBase: hung.URL, Token: "t", Namespace: "ns", Deployment: "d"}
	done := make(chan error, 1)
	go func() { done <- r.Restart(context.Background(), time.Now()) }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("a hung API server was reported as a successful restart")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the restart was still waiting on a hung API server after 5s")
	}
}
