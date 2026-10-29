// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0
//
// The signal handling below follows nats-server's server/signal.go (handleSignals and
// handleSignal) at v2.15.2, Copyright 2012-2026 The NATS Authors, Apache License 2.0, changed
// only where this file says so.

//go:build !windows

package main

import (
	"os"
	"os/signal"
	"syscall"
)

// platformSupported reports whether this binary can run here. It can wherever upstream's own
// signal handling compiles.
func platformSupported() error { return nil }

// notifySignals subscribes to the signals upstream's handler takes. Called BEFORE the server is
// created, with the built-in handler switched off (Options.NoSigs), so there is no moment at which
// a SIGHUP takes its default action, which is to terminate the process. Signals that arrive before
// the server is running wait in the buffer.
func notifySignals() chan os.Signal {
	c := make(chan os.Signal, 8)
	signal.Notify(c, syscall.SIGINT, syscall.SIGTERM, syscall.SIGUSR1, syscall.SIGUSR2, syscall.SIGHUP)
	return c
}

// handleSignals runs the signal loop. Upstream's loop also stops on the server's (unexported)
// quit channel; this one needs no stop, because the process exits when WaitForShutdown returns in
// main.
func (b *broker) handleSignals(c chan os.Signal) {
	go func() {
		for sig := range c {
			b.handleSignal(sig)
		}
	}()
}

// handleSignal is upstream's, case for case, with two changes:
//
//   - SIGHUP calls b.reload, which re-applies the device TLS setting between parsing the config and
//     handing it to the server, then re-checks the listener. Upstream calls Server.Reload, which
//     would SUCCEED and leave the listener no longer requesting certificates (see applyDeviceTLS).
//   - SIGTERM's "is a lame duck shutdown already in progress?" reads b.ldm, set when SIGUSR2 is
//     handled here, instead of the server's own unexported flag. They agree for every SIGTERM that
//     matters: lame duck mode is entered only through SIGUSR2 (which is all `-sl=ldm` sends, and
//     what the chart's preStop runs) or LameDuckShutdown, which nothing in this binary calls
//     otherwise. They could differ only if SIGUSR2 arrived while the server was already shutting
//     down — when upstream's lameDuckMode returns without setting its flag — and then the shutdown
//     in progress ends the process anyway.
func (b *broker) handleSignal(sig os.Signal) {
	s := b.s
	s.Noticef("Trapped %q signal", sig)
	switch sig {
	case syscall.SIGINT:
		s.Shutdown()
		s.WaitForShutdown()
		os.Exit(0)
	case syscall.SIGTERM:
		// Shutdown unless graceful shutdown already in progress.
		if !b.ldm.Load() {
			s.Shutdown()
			s.WaitForShutdown()
			os.Exit(0)
		}
	case syscall.SIGUSR1:
		// File log re-open for rotating file logs.
		s.ReOpenLogFile()
	case syscall.SIGUSR2:
		b.ldm.Store(true)
		go s.LameDuckShutdown()
	case syscall.SIGHUP:
		// Config reload, keeping the device TLS setting.
		if err := b.reload(); err != nil {
			s.Errorf("Failed to reload server configuration: %s", err)
		}
		b.check()
	}
}
