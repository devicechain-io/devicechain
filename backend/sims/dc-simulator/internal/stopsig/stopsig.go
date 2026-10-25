// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package stopsig lets a load-test binary tell "I was stopped" from "I failed".
//
// signal.NotifyContext cancels the context and forgets why, so a run that returns an error
// because it was told to stop looks exactly like one that broke, and exits 1. The release
// candidate script stops the other rungs when one fails; those rungs then reported exit 1 and
// the summary could not tell the rung that failed from the rungs the script stopped. The shell
// convention is 128 + the signal number (143 for SIGTERM, 130 for SIGINT), and the script
// reads 143 as "stopped".
package stopsig

import (
	"context"
	"os"
	"os/signal"
	"sync"
	"syscall"
)

// Stop remembers the signal that cancelled the context.
type Stop struct {
	mu  sync.Mutex
	sig os.Signal
}

// Notify is signal.NotifyContext for SIGINT and SIGTERM that also records which one arrived.
func Notify(parent context.Context) (context.Context, context.CancelFunc, *Stop) {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, os.Interrupt, syscall.SIGTERM)
	ctx, cancel, s := notify(parent, ch)
	return ctx, func() { signal.Stop(ch); cancel() }, s
}

// notify is Notify over a caller-supplied channel, so a test can deliver a signal without
// sending one to its own process (which Windows cannot do).
func notify(parent context.Context, ch <-chan os.Signal) (context.Context, context.CancelFunc, *Stop) {
	ctx, cancel := context.WithCancel(parent)
	s := &Stop{}
	go func() {
		select {
		case sig := <-ch:
			s.mu.Lock()
			s.sig = sig
			s.mu.Unlock()
			cancel()
		case <-ctx.Done():
		}
	}()
	return ctx, cancel, s
}

// ExitCode is 128 + the signal number when a signal stopped the run, and false when none did.
func (s *Stop) ExitCode() (int, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sig, ok := s.sig.(syscall.Signal)
	if !ok {
		if s.sig == nil {
			return 0, false
		}
		// os.Interrupt on a platform whose signal type is not syscall.Signal.
		return 130, true
	}
	return 128 + int(sig), true
}
