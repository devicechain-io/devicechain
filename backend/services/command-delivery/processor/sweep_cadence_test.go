// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"testing"
	"time"

	"github.com/devicechain-io/dc-command-delivery/config"
	"github.com/devicechain-io/dc-microservice/messaging"
)

// An unset field means "platform default", never zero. This struct is exported and
// assembled by literal in tests, so a zero SweepInterval is an unset field — and a
// time.Ticker built from a zero duration PANICS, which would take out the goroutine that
// delivers every command.
func TestUnsetSweepIntervalMeansThePlatformDefault(t *testing.T) {
	proc := procWith(&fakeApi{}, &recordingWriter{})
	if got := proc.sweepInterval(); got != config.DefaultSweepIntervalSeconds*time.Second {
		t.Fatalf("unset interval = %v, want the default %ds", got, config.DefaultSweepIntervalSeconds)
	}
}

func TestConfiguredSweepIntervalIsReturned(t *testing.T) {
	proc := procWith(&fakeApi{}, &recordingWriter{})
	proc.SweepInterval = 3 * time.Second
	if got := proc.sweepInterval(); got != 3*time.Second {
		t.Fatalf("interval = %v, want the configured 3s", got)
	}
}

// 🔑 THE DISCRIMINATOR. The test above passes against a ticker built from the package
// constant — the accessor would be right and its only caller wrong, which is the shape
// where a helper is certified and the call site is not. This one runs the ticker and
// counts sweeps, so a cadence the ticker ignores fails here and nowhere else.
//
// It asserts a LOWER BOUND on sweeps rather than an exact count: the assertion is "the
// configured cadence reached the ticker", and pinning an exact number would make a busy
// CI box fail a correct implementation.
func TestTheTickerSweepsOnTheConfiguredCadence(t *testing.T) {
	api := &fakeApi{lockAvailable: true}
	proc := procWith(api, &recordingWriter{})
	proc.SweepInterval = 20 * time.Millisecond
	proc.quit = make(chan struct{})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go proc.runSweepTicker(ctx)

	// Well under the 30s default: if the ticker used it, zero sweeps happen here.
	time.Sleep(300 * time.Millisecond)
	cancel()

	if got := api.sweepLockAttempts(); got < 3 {
		t.Fatalf("the ticker attempted %d sweeps in 300ms at a 20ms cadence; "+
			"want at least 3. Zero or one means it is running on the package default, "+
			"not on the configured interval", got)
	}
}

// The counterweight: the ticker must STOP. Without this the test above would pass just as
// well against a loop that ignores cancellation, and a processor that never releases its
// goroutine would leak one per restart.
func TestTheTickerStopsWhenCancelled(t *testing.T) {
	api := &fakeApi{lockAvailable: true}
	proc := procWith(api, &recordingWriter{})
	proc.SweepInterval = 20 * time.Millisecond
	proc.quit = make(chan struct{})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		proc.runSweepTicker(ctx)
		close(done)
	}()

	time.Sleep(60 * time.Millisecond)
	cancel()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("runSweepTicker did not return after its context was cancelled")
	}

	settled := api.sweepLockAttempts()
	time.Sleep(150 * time.Millisecond)
	if got := api.sweepLockAttempts(); got != settled {
		t.Fatalf("sweeps continued after cancellation: %d then %d", settled, got)
	}
}

// 🔴 THE COUPLING THAT MADE THE CADENCE CONFIGURABLE DANGEROUS, PINNED.
//
// StrandedSentGrace answers "could the platform still be working on this?", and one of its
// terms is the gap before the sweep next looks at the row. While the cadence was a
// constant, deriving from it was exact. Now that an operator can raise it, a grace derived
// from the DEFAULT would silently stop covering the window it names: the stranded pass
// would begin parking commands the sweep had simply not reached yet, and nothing would
// fail, log or alert — the reading would just quietly stop meaning what it says.
//
// The invariant is that the horizon covers the SLOWEST sweep the service will accept,
// whatever an operator configured.
func TestStrandedGraceCoversTheSlowestPermittedSweep(t *testing.T) {
	brokerBudget := messaging.MaxDeliver * messaging.AckWait
	slowest := time.Duration(config.MaxSweepIntervalSeconds) * time.Second

	if StrandedSentGrace < brokerBudget+slowest {
		t.Fatalf("StrandedSentGrace = %v, but the broker can still be redelivering for %v "+
			"and the sweep may not look again for %v; the pass would park commands still in flight",
			StrandedSentGrace, brokerBudget, slowest)
	}
}
