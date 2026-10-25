// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package stopsig

import (
	"context"
	"os"
	"syscall"
	"testing"
	"time"
)

func deliver(t *testing.T, sig os.Signal) *Stop {
	t.Helper()
	ch := make(chan os.Signal, 1)
	ctx, cancel, s := notify(context.Background(), ch)
	defer cancel()
	ch <- sig
	select {
	case <-ctx.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("the context was not cancelled by the signal")
	}
	return s
}

func TestSigtermExits143(t *testing.T) {
	if code, ok := deliver(t, syscall.SIGTERM).ExitCode(); !ok || code != 143 {
		t.Fatalf("SIGTERM: got (%d, %v), want (143, true)", code, ok)
	}
}

func TestSigintExits130(t *testing.T) {
	if code, ok := deliver(t, os.Interrupt).ExitCode(); !ok || code != 130 {
		t.Fatalf("SIGINT: got (%d, %v), want (130, true)", code, ok)
	}
}

// The counterpart: a context cancelled by anything else (a deadline, the parent) is NOT a stop, so
// a run that failed for its own reasons is never relabelled 143.
func TestOtherCancelIsNotAStop(t *testing.T) {
	parent, cancelParent := context.WithCancel(context.Background())
	ch := make(chan os.Signal, 1)
	ctx, cancel, s := notify(parent, ch)
	defer cancel()
	cancelParent()
	<-ctx.Done()
	time.Sleep(20 * time.Millisecond)
	if code, ok := s.ExitCode(); ok {
		t.Fatalf("a parent cancel reported a stop (%d)", code)
	}
}
