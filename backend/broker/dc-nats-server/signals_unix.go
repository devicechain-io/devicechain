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

// handleSignal follows upstream v2.15.3, case for case, with three changes:
//
//   - SIGHUP calls b.reload, which re-applies the device TLS setting between parsing the config and
//     handing it to the server (and takes the hot key-material path when that is all that changed),
//     then re-checks the listener. Upstream calls Server.Reload, which would SUCCEED and leave the
//     listener no longer requesting certificates (see applyDeviceTLS).
//   - Upstream's "has startup finished?" reads the server's unexported startupComplete; this reads
//     b.started, closed by main when Run returns. Since v2.15.3 Server.ReloadOptions blocks until
//     startup has finished, and this loop starts BEFORE Run (main.go says why), so a SIGHUP during
//     a long JetStream recovery is handed to a goroutine that waits for b.started, exactly as
//     upstream waits outside its own loop, and SIGTERM, SIGINT and SIGUSR2 are still handled in the
//     meantime. b.started closes a little later than startupComplete (after the client listener
//     and, with ocsp_peer, the OCSP response cache are up); a reload in that interval only waits a
//     little longer. Like upstream's, a deferred reload is abandoned if the server stops first
//     (b.stopped), and is best effort against a shutdown that begins while it runs: the process is
//     exiting then anyway.
//   - SIGTERM's "is a lame duck shutdown already in progress?" reads b.ldm instead of the server's
//     own unexported flag, and SIGUSR2 sets it before returning, in place of upstream's unexported
//     startLameDuckMode, which enters lame duck mode before returning. Upstream's enterLameDuckMode
//     refuses (sets nothing) when the client listener is nil, which is the whole of startup and
//     any shutdown; this reads the same condition through s.Addr() and likewise sets nothing, so a
//     preStop SIGUSR2 during a long recovery does not make the SIGTERM that follows a no-op. With
//     the listener up the two flags agree for every SIGTERM that matters: lame duck mode is
//     entered only through SIGUSR2 (all `-sl=ldm` sends, and what the chart's preStop runs). They
//     could differ only if a shutdown began between the check and LameDuckShutdown, and then the
//     shutdown in progress ends the process anyway.
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
		switch {
		case b.ldm.Load():
			// Already in lame duck mode: upstream's enterLameDuckMode returns without a word.
		case s.Addr() == nil:
			s.Noticef("Lame duck mode is not entered: the client listener is not up (the server is still starting, or stopping)")
		default:
			b.ldm.Store(true)
			go s.LameDuckShutdown()
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
				// Run also returns when the server shut down during startup.
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
