// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package installflow

import (
	"context"
	"errors"
	"sync"
)

// CancelRequest is the cause a presenter cancels a run's context with, so the
// engine can say WHY it is stopping. It matches context.Canceled under
// errors.Is, so code below the engine that tests for a cancellation still sees
// one.
//
// A run is cancelled once: a second request — the signal and the keypress that
// are the same Ctrl+C, or a second press — finds the context already done and
// changes nothing, so its reason is the first one's.
type CancelRequest struct{ Reason CancelReason }

func (c CancelRequest) Error() string { return "run cancelled: " + string(c.Reason) }

// Is makes a CancelRequest a context.Canceled.
func (c CancelRequest) Is(target error) bool { return target == context.Canceled }

// CancelReasonOf is why ctx was cancelled: the reason a CancelRequest carries,
// deadline for a context that ran out of time, and otherwise signal — the root
// context's only other canceller is the process's signal handler.
func CancelReasonOf(ctx context.Context) CancelReason {
	cause := context.Cause(ctx)
	var req CancelRequest
	switch {
	case errors.As(cause, &req) && req.Reason.valid():
		return req.Reason
	case errors.Is(cause, context.DeadlineExceeded):
		return CancelDeadline
	}
	return CancelSignal
}

// ErrNotSettled refuses a fresh run while the previous run in this process has
// not settled: its engine has not returned yet (it is still stopping), or it
// returned having stopped waiting for a tool that is still running.
var ErrNotSettled = errors.New("the previous run has not settled yet: it is still stopping, " +
	"or a tool it stopped waiting for is still running. Wait for it, or exit dcctl and run the same command again")

// Admission admits engine runs into a process one at a time, and a fresh run
// only once the previous one has SETTLED.
//
// 🔴 SETTLED IS NOT "THE SUMMARY WAS SENT". A run has settled when its engine
// has returned — every stage it started has stopped, so it holds no cluster
// claim any more — AND every tool it stopped waiting for has actually exited.
// The second half is the abandoned OpenTofu apply: the engine gives up on an
// interrupted command after its budget and reports the run, but the command's
// goroutine, and whatever provider plugin is keeping its pipe open, may still
// be changing the cluster. A re-run admitted then would apply over it.
//
// The zero value is ready to use. One Admission serves every run a process
// makes; a presenter that offers a re-run in the same process holds one for
// its lifetime. Admit never waits: Settled says when to try again.
type Admission struct {
	mu      sync.Mutex
	holds   int           // the run itself, plus every tool it outlived
	settled chan struct{} // closed when holds reaches zero; nil before the first run
}

// Admit admits a run, or refuses with ErrNotSettled.
func (a *Admission) Admit() (*Ticket, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.holds > 0 {
		return nil, ErrNotSettled
	}
	a.holds = 1
	a.settled = make(chan struct{})
	t := &Ticket{a: a}
	return t, nil
}

// Settled is closed once the last run admitted has settled. Before any run it
// is already closed.
func (a *Admission) Settled() <-chan struct{} {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.settled == nil {
		c := make(chan struct{})
		close(c)
		return c
	}
	return a.settled
}

func (a *Admission) release() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.holds--
	if a.holds == 0 {
		close(a.settled)
	}
}

// Ticket is one admitted run's hold on its Admission.
type Ticket struct {
	a    *Admission
	once sync.Once
}

// Outlive registers work the run is leaving behind — a tool it stopped waiting
// for — and returns the function that says it has ended. The run is not
// settled until it has been called. Each returned function counts once.
func (t *Ticket) Outlive() (ended func()) {
	t.a.mu.Lock()
	t.a.holds++
	t.a.mu.Unlock()
	var once sync.Once
	return func() { once.Do(t.a.release) }
}

// Done says the run's engine has returned. It counts once.
func (t *Ticket) Done() { t.once.Do(t.a.release) }
