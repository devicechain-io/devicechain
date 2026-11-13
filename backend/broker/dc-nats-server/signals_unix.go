// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0
//
// The signal handling below follows nats-server's server/signal.go (handleSignals and
// handleSignal) at v2.15.3, Copyright 2012-2026 The NATS Authors, Apache License 2.0, changed
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

// handleSignal follows upstream v2.15.3 with three changes:
//
//   - SIGHUP calls b.reload, which keeps the device TLS setting and hot key-material path,
//     then re-checks the listener. Server.Reload would drop the certificate request.
//   - SIGTERM reads b.ldm instead of the server's unexported flag. SIGUSR2 sets it
//     synchronously before launching the blocking LameDuckShutdown, replacing upstream's
//     unexported startLameDuckMode. Requests during startup are refused, as upstream's
//     enterLameDuckMode refuses when the client listener is nil. After startup the flags
//     agree for subsequent SIGTERM handling: this binary only enters lame duck via SIGUSR2.
//     A request during shutdown may differ, but that shutdown ends the process anyway.
//   - The broker's started gate replaces the unexported startupComplete and waitForStartup.
//     It also waits for the client listener (Run has returned), so lame duck can enter.
//     A deferred reload returns on stopped if shutdown completes before startup.
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
		select {
		case <-b.started:
			b.ldm.Store(true)
			go s.LameDuckShutdown()
		default:
			s.Noticef("Lame duck mode is not entered because the server is still starting")
		}
	case syscall.SIGHUP:
		// Config reload, keeping the device TLS setting.
		reload := func() {
			if err := b.reload(); err != nil {
				s.Errorf("Failed to reload server configuration: %s", err)
			}
			b.check()
		}
		select {
		case <-b.started:
			reload()
		default:
			s.Noticef("Server is still starting, config reload will run once startup completes")
			go func() {
				select {
				case <-b.stopped:
					return
				case <-b.started:
				}
				// Run may return because the server shut down during startup.
				select {
				case <-b.stopped:
					return
				default:
					reload()
				}
			}()
		}
	}
}
