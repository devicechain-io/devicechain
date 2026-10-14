// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// drain must hold until the terminal callback of every accepted connection has finished,
// not merely until the connection is closed: net/http runs that callback on the
// connection's own goroutine, after Shutdown can already have returned.
func TestConnTrackerDrainWaitsForTheTerminalCallback(t *testing.T) {
	counted := make(chan struct{})
	release := make(chan struct{})
	tr := &connTracker{source: "s", served: map[net.Conn]struct{}{}, count: func(string) {
		close(counted)
		<-release
	}}
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()

	tr.connState(a, http.StateNew)
	go tr.connState(a, http.StateClosed)
	<-counted

	short, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	require.ErrorIs(t, tr.drain(short), context.DeadlineExceeded, "callback still running")

	done := make(chan error, 1)
	go func() { done <- tr.drain(context.Background()) }()
	close(release)
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("drain did not return after the callback finished")
	}
	require.NoError(t, tr.drain(context.Background()), "nothing live")
	var nilTracker *connTracker
	require.NoError(t, nilTracker.drain(context.Background()))
}
