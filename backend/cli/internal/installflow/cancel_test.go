// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package installflow_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/devicechain-io/dcctl/internal/installflow"
	"github.com/devicechain-io/dcctl/internal/installflow/testkit"
)

func TestCancelReasonOf(t *testing.T) {
	ctx, cancel := context.WithCancelCause(t.Context())
	cancel(installflow.CancelRequest{Reason: installflow.CancelUser})
	// The first request is the request: a second is the same Ctrl+C.
	cancel(installflow.CancelRequest{Reason: installflow.CancelDeliveryFailure})
	if got := installflow.CancelReasonOf(ctx); got != installflow.CancelUser {
		t.Errorf("reason = %s, want user", got)
	}
	if !errors.Is(context.Cause(ctx), context.Canceled) {
		t.Error("a cancel request is not a context.Canceled, so code below the engine would not see a cancellation")
	}

	plain, stop := context.WithCancel(t.Context())
	stop()
	if got := installflow.CancelReasonOf(plain); got != installflow.CancelSignal {
		t.Errorf("an unexplained cancel = %s, want signal", got)
	}
	late, stop2 := context.WithDeadline(t.Context(), time.Unix(1, 0))
	defer stop2()
	if got := installflow.CancelReasonOf(late); got != installflow.CancelDeadline {
		t.Errorf("an expired deadline = %s, want deadline", got)
	}
}

func settled(a *installflow.Admission) bool {
	select {
	case <-a.Settled():
		return true
	default:
		return false
	}
}

// 🔴 A RE-RUN IS ADMITTED ONLY ONCE THE PREVIOUS RUN HAS SETTLED: its engine returned,
// AND every tool it stopped waiting for has exited. Done alone is not settled.
func TestAdmissionWaitsForTheRunAndEveryToolItOutlived(t *testing.T) {
	var a installflow.Admission
	if !settled(&a) {
		t.Fatal("an admission that has admitted nothing is not settled")
	}
	first, err := a.Admit()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.Admit(); !errors.Is(err, installflow.ErrNotSettled) {
		t.Fatalf("a second run was admitted while the first was live: %v", err)
	}
	toolEnded := first.Outlive()
	first.Done()
	first.Done() // counts once
	if settled(&a) {
		t.Fatal("settled while a tool the run gave up on is still running")
	}
	if _, err := a.Admit(); !errors.Is(err, installflow.ErrNotSettled) {
		t.Fatalf("a re-run was admitted over an abandoned tool: %v", err)
	}
	toolEnded()
	toolEnded() // counts once
	if !settled(&a) {
		t.Fatal("not settled once the run and its tool had both ended")
	}
	if _, err := a.Admit(); err != nil {
		t.Fatalf("a settled admission refused a fresh run: %v", err)
	}
}

// Hub.Run applies the admission: a refused run sends one failed summary, starts
// nothing, and does not reach the engine; an admitted one settles when the engine
// returns — even when the engine panics.
func TestHubRunAppliesAdmission(t *testing.T) {
	var a installflow.Admission
	held, err := a.Admit()
	if err != nil {
		t.Fatal(err)
	}

	sink := &testkit.CaptureSink{}
	hub, err := installflow.NewHub(installflow.Options{Sink: sink})
	if err != nil {
		t.Fatal(err)
	}
	e := &testkit.ScriptedEngine{Script: []testkit.Action{testkit.WaitAck("never")}}
	sum, err := hub.Run(t.Context(), e, installflow.Request{}, installflow.Runtime{Admission: &a})
	<-hub.Done()
	if !errors.Is(err, installflow.ErrNotSettled) || sum.Outcome != installflow.RunFailed ||
		sum.PartialState != installflow.EffectNoneObserved {
		t.Fatalf("refused run: summary %+v, err %v", sum, err)
	}
	if kinds := sink.Kinds(); len(kinds) != 1 || kinds[0] != installflow.KindRunSummary {
		t.Errorf("a refused run's stream = %v, want only its summary", kinds)
	}
	held.Done()

	hub2, err := installflow.NewHub(installflow.Options{Sink: &testkit.CaptureSink{}})
	if err != nil {
		t.Fatal(err)
	}
	pe := &panicEngine{a: &a}
	func() {
		defer func() { _ = recover() }()
		_, _ = hub2.Run(t.Context(), pe, installflow.Request{}, installflow.Runtime{Admission: &a})
	}()
	if pe.settledInside {
		t.Error("the admission settled while the engine was still running")
	}
	if !settled(&a) {
		t.Error("an engine that panicked left the admission unsettled for good")
	}
}

type panicEngine struct {
	a             *installflow.Admission
	settledInside bool
}

func (p *panicEngine) Run(context.Context, installflow.Request, installflow.Runtime) (installflow.Summary, error) {
	p.settledInside = settled(p.a)
	panic("boom")
}
