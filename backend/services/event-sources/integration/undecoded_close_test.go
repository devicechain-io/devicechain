// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package integration

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestUndecodedCloseFlushesPending(t *testing.T) {
	f := newFakeBackend()
	u := NewUndecoded(f, time.Hour, nil, nil)
	runCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runDone := make(chan struct{})
	go func() {
		u.Run(runCtx)
		close(runDone)
	}()
	u.Note("acme", "cs", "right-token", "01")
	require.Eventually(t, func() bool {
		u.mu.Lock()
		defer u.mu.Unlock()
		return u.pending["acme\x00cs"].count == 0
	}, time.Second, time.Millisecond)
	for range 3 {
		u.Note("acme", "cs", "right-token", "02")
	}
	ctx, stop := context.WithTimeout(context.Background(), time.Second)
	defer stop()
	u.Close(ctx)
	select {
	case <-runDone:
	case <-time.After(time.Second):
		t.Fatal("Close did not stop Run")
	}
	f.mu.Lock()
	calls := append([]undecodedCall(nil), f.undecoded[1:]...)
	f.mu.Unlock()
	if len(calls) != 1 {
		t.Fatalf("final flush: got %d calls, want 1 with count=3", len(calls))
	}
	require.Equal(t, undecodedCall{"acme", "cs", "right-token", 3, "02"}, calls[0])
	u.Close(ctx)
	f.mu.Lock()
	defer f.mu.Unlock()
	require.Len(t, f.undecoded, 2, "Close must be idempotent")
}

type blockedUndecodedBackend struct {
	*fakeBackend
	started  chan struct{}
	finished chan error
}

func (f *blockedUndecodedBackend) RecordUndecoded(ctx context.Context, _, _, _ string, _ int, _ string) error {
	f.started <- struct{}{}
	<-ctx.Done()
	f.finished <- ctx.Err()
	return ctx.Err()
}

func TestUndecodedCloseCancelsInflight(t *testing.T) {
	f := &blockedUndecodedBackend{newFakeBackend(), make(chan struct{}, 4), make(chan error, 4)}
	u := NewUndecoded(f, time.Hour, nil, nil)
	u.Note("acme", "cs", "right-token", "01")
	select {
	case <-f.started:
	case <-time.After(time.Second):
		t.Fatal("immediate flush did not start")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	closed := make(chan struct{})
	start := time.Now()
	go func() {
		u.Close(ctx)
		close(closed)
	}()
	require.Eventually(t, func() bool {
		u.mu.Lock()
		defer u.mu.Unlock()
		return u.closed
	}, time.Second, time.Millisecond)
	u.Note("acme", "after-close", "right-token", "02")
	u.mu.Lock()
	pending := len(u.pending)
	u.mu.Unlock()
	require.Equal(t, 1, pending, "Note while Close waits must not admit a tally")
	select {
	case err := <-f.finished:
		require.Error(t, err)
	case <-time.After(time.Second):
		t.Fatal("in-flight RecordUndecoded context was not cancelled within 1s")
	}
	select {
	case <-closed:
		require.Less(t, time.Since(start), time.Second)
	case <-time.After(time.Second):
		t.Fatal("Close did not return within 1s")
	}
}

func TestUndecodedNoteAfterClose(t *testing.T) {
	f := newFakeBackend()
	u := NewUndecoded(f, time.Hour, nil, nil)
	u.Close(context.Background())
	u.Note("acme", "cs", "right-token", "01")
	u.mu.Lock()
	pending := len(u.pending)
	u.mu.Unlock()
	require.Zero(t, pending, "Note after Close must not admit a tally")
	f.mu.Lock()
	defer f.mu.Unlock()
	require.Empty(t, f.undecoded)
}
