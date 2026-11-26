// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/devicechain-io/dcctl/internal/installflow"
)

// runTool runs one interruptible tool command the way runUntilAbandoned always has,
// and keeps the engine run's books on it: that a tool is running (so a stop can say
// it is waiting for one, and for how long that tool was given), and, when the run
// stops waiting for it, that the stage it belongs to was abandoned mid-command and
// that the command is still out there.
//
// 🔴 AN ABANDONED COMMAND HOLDS THE RUN UNSETTLED UNTIL IT REALLY EXITS. Giving up
// on it ends the stage and lets the run report, but the command's goroutine is
// parked inside terraform-exec and the provider plugin keeping its pipe open may
// still be applying. The hold taken here (Runtime.Outlive) is what keeps a re-run in
// the same process from being admitted on top of it; it is let go by the goroutine
// itself, on its way out, whenever that is.
//
// Outside an engine run (bootstrap, upgrade, destroy) it is runUntilAbandoned and
// nothing else.
func runTool(ctx context.Context, budget, gracefulStop time.Duration, run func() error) error {
	tool := runFrom(ctx).toolStarted(gracefulStop)
	err := runUntilAbandoned(ctx, budget, tool.wrap(run))
	tool.ended(errors.Is(err, errTofuAbandoned))
	return err
}

// trackedTool is one tool command an engine run has started.
type trackedTool struct {
	run *installRun

	mu      sync.Mutex
	exited  bool   // the command's own goroutine has returned
	release func() // the run's hold on an abandoned command, until it exits
}

// toolStarted counts a tool command as running, given gracefulStop to stop in once
// interrupted. Nil outside an engine run.
func (r *installRun) toolStarted(gracefulStop time.Duration) *trackedTool {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.tools++
	r.toolStop = gracefulStop
	return &trackedTool{run: r}
}

// wrap is run, reporting its own return.
func (t *trackedTool) wrap(run func() error) func() error {
	if t == nil {
		return run
	}
	return func() error {
		defer t.exit()
		return run()
	}
}

// exit is the command's goroutine returning: the tool is no longer running, and if
// the run had already given up on it, the run's hold on it goes.
func (t *trackedTool) exit() {
	t.mu.Lock()
	t.exited = true
	release := t.release
	t.release = nil
	t.mu.Unlock()

	t.run.mu.Lock()
	t.run.tools--
	t.run.mu.Unlock()
	if release != nil {
		release()
	}
}

// ended is runUntilAbandoned returning. A command it gave up on marks the stage
// abandoned — which is never none_observed — and, unless it has exited in the
// meantime, is held open until it does.
func (t *trackedTool) ended(abandoned bool) {
	if t == nil || !abandoned {
		return
	}
	t.mu.Lock()
	if !t.exited && t.run.outlive != nil {
		t.release = t.run.outlive()
	}
	t.mu.Unlock()

	r := t.run
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.step != "" {
		r.abandoned = true
		r.raiseLocked(installflow.EffectMayHaveChanged)
	}
}
