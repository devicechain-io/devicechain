// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package testkit

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/devicechain-io/dcctl/internal/installflow"
)

// Action is one step of a ScriptedEngine's script.
type Action struct {
	emit    *installflow.Event
	ask     *askAction
	ackName string
	advance time.Duration
}

type askAction struct {
	label  string
	prompt installflow.Prompt
	// stopOnRefusal ends the run as failed when the prompt is not answered
	// yes or with a value.
	stopOnRefusal bool
}

// Emit plays ev.
func Emit(ev installflow.Event) Action { return Action{emit: &ev} }

// Ask asks p through the runtime's broker and records the answer under label.
// A prompt that is declined, unavailable or invalid ends the run as failed, as
// a refused confirmation does today.
func Ask(label string, p installflow.Prompt) Action {
	return Action{ask: &askAction{label: label, prompt: p, stopOnRefusal: true}}
}

// WaitAck pauses the script until the test calls Ack(name) or the run is
// cancelled. Reached(name) is closed when the script gets here.
func WaitAck(name string) Action { return Action{ackName: name} }

// Advance moves the engine's manual clock forward.
func Advance(d time.Duration) Action { return Action{advance: d} }

// RecordedAnswer is one prompt answer the engine received.
type RecordedAnswer struct {
	Label  string
	Answer installflow.Answer
	Err    error
}

// ScriptedEngine implements installflow.Engine by playing a fixed script. It
// tracks the steps it has open, so a cancellation closes them with
// step.failed{cancelled:true} children first, after a run.cancelling.
type ScriptedEngine struct {
	Script []Action
	// Clock, when set, is what Advance moves.
	Clock *ManualClock
	// Summary is sent when the script completes. Cancellation and failure
	// send their own.
	Summary installflow.Summary

	mu        sync.Mutex
	acks      map[string]chan struct{}
	reached   map[string]chan struct{}
	answers   []RecordedAnswer
	emitErrs  []error
	cancelled bool
	open      []installflow.Event // started step attempts, in start order
}

func (e *ScriptedEngine) chans(name string) (ack, reached chan struct{}) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.acks == nil {
		e.acks, e.reached = map[string]chan struct{}{}, map[string]chan struct{}{}
	}
	if e.acks[name] == nil {
		e.acks[name], e.reached[name] = make(chan struct{}), make(chan struct{})
	}
	return e.acks[name], e.reached[name]
}

// Ack releases WaitAck(name).
func (e *ScriptedEngine) Ack(name string) {
	ack, _ := e.chans(name)
	close(ack)
}

// Reached is closed once the script arrives at WaitAck(name).
func (e *ScriptedEngine) Reached(name string) <-chan struct{} {
	_, r := e.chans(name)
	return r
}

// Answers returns the prompt answers received, in order.
func (e *ScriptedEngine) Answers() []RecordedAnswer {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]RecordedAnswer(nil), e.answers...)
}

// EmitErrors returns the errors the hub returned to the script's Emits.
func (e *ScriptedEngine) EmitErrors() []error {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]error(nil), e.emitErrs...)
}

// Cancelled reports whether the engine saw its context cancelled.
func (e *ScriptedEngine) Cancelled() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.cancelled
}

// Run plays the script.
func (e *ScriptedEngine) Run(ctx context.Context, req installflow.Request, rt installflow.Runtime) (installflow.Summary, error) {
	for _, a := range e.Script {
		if ctx.Err() != nil {
			return e.cancel(ctx, rt)
		}
		switch {
		case a.emit != nil:
			if err := rt.Events.Emit(*a.emit); err != nil {
				e.mu.Lock()
				e.emitErrs = append(e.emitErrs, err)
				e.mu.Unlock()
				if errors.Is(err, installflow.ErrDeliveryStalled) {
					continue // the hub has requested a cancel; the next check sees it
				}
				return e.fail(rt, fmt.Errorf("emit %s: %w", a.emit.Payload.Kind(), err))
			}
			e.track(*a.emit)
		case a.ask != nil:
			ans, err := rt.Prompts.Ask(ctx, a.ask.prompt)
			e.mu.Lock()
			e.answers = append(e.answers, RecordedAnswer{Label: a.ask.label, Answer: ans, Err: err})
			e.mu.Unlock()
			if ctx.Err() != nil {
				return e.cancel(ctx, rt)
			}
			if err != nil && a.ask.stopOnRefusal {
				return e.fail(rt, err)
			}
		case a.ackName != "":
			ack, reached := e.chans(a.ackName)
			e.mu.Lock()
			select {
			case <-reached:
			default:
				close(reached)
			}
			e.mu.Unlock()
			select {
			case <-ack:
			case <-ctx.Done():
				return e.cancel(ctx, rt)
			}
		case a.advance > 0:
			if e.Clock == nil {
				return e.fail(rt, errors.New("testkit: Advance needs a Clock"))
			}
			e.Clock.Advance(a.advance)
		}
	}
	return e.Summary, rt.Events.Finish(e.Summary)
}

func (e *ScriptedEngine) track(ev installflow.Event) {
	e.mu.Lock()
	defer e.mu.Unlock()
	switch ev.Payload.(type) {
	case installflow.StepStarted:
		e.open = append(e.open, ev)
	case installflow.StepFinished, installflow.StepFailed:
		for i, o := range e.open {
			if o.Step == ev.Step && o.Attempt == ev.Attempt {
				e.open = append(e.open[:i], e.open[i+1:]...)
				break
			}
		}
	}
}

func (e *ScriptedEngine) closeOpen(rt installflow.Runtime, cancelled bool) installflow.StepID {
	e.mu.Lock()
	open := append([]installflow.Event(nil), e.open...)
	e.open = nil
	e.mu.Unlock()
	var innermost installflow.StepID
	for i := len(open) - 1; i >= 0; i-- {
		o := open[i]
		if innermost == "" {
			innermost = o.Step
		}
		code := installflow.ErrorCode("run.failed")
		if cancelled {
			code = "run.cancelled"
		}
		_ = rt.Events.Emit(installflow.Event{Step: o.Step, Parent: o.Parent, Attempt: o.Attempt,
			Payload: installflow.StepFailed{Code: code, Retry: installflow.RetryUnknown, Cancelled: cancelled,
				Effect: installflow.EffectMayHaveChanged}})
	}
	return innermost
}

func (e *ScriptedEngine) cancel(ctx context.Context, rt installflow.Runtime) (installflow.Summary, error) {
	e.mu.Lock()
	e.cancelled = true
	var active installflow.StepID
	if n := len(e.open); n > 0 {
		active = e.open[n-1].Step
	}
	e.mu.Unlock()
	_ = rt.Events.Emit(installflow.Event{Payload: installflow.RunCancelling{Reason: installflow.CancelSignal, ActiveStep: active}})
	e.closeOpen(rt, true)
	s := installflow.Summary{Outcome: installflow.RunCancelled, InstallRecord: installflow.RecordUnknown,
		Readiness: installflow.ReadinessUnknown, Claim: installflow.ClaimUnknown,
		FailedStep: active, PartialState: installflow.EffectMayHaveChanged}
	_ = rt.Events.Finish(s)
	return s, ctx.Err()
}

func (e *ScriptedEngine) fail(rt installflow.Runtime, cause error) (installflow.Summary, error) {
	step := e.closeOpen(rt, false)
	s := installflow.Summary{Outcome: installflow.RunFailed, InstallRecord: installflow.RecordUnknown,
		Readiness: installflow.ReadinessUnknown, Claim: installflow.ClaimUnknown,
		FailedStep: step, PartialState: installflow.EffectMayHaveChanged}
	_ = rt.Events.Finish(s)
	return s, cause
}

// Reply is one scripted answer.
type Reply struct {
	Response installflow.Response
	Err      error
	// WaitForCancel makes the responder block until the prompt is cancelled.
	WaitForCancel bool
}

// ScriptedResponder answers questions from a queue and records them.
type ScriptedResponder struct {
	mu        sync.Mutex
	replies   []Reply
	questions []installflow.Question
	asked     chan struct{}
}

// NewScriptedResponder returns a responder that replies in order and reports
// ErrPromptUnavailable once the replies run out.
func NewScriptedResponder(replies ...Reply) *ScriptedResponder {
	return &ScriptedResponder{replies: replies, asked: make(chan struct{}, 64)}
}

// Asked receives once per question.
func (r *ScriptedResponder) Asked() <-chan struct{} { return r.asked }

// Questions returns the questions shown so far.
func (r *ScriptedResponder) Questions() []installflow.Question {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]installflow.Question(nil), r.questions...)
}

// Respond pops the next reply.
func (r *ScriptedResponder) Respond(ctx context.Context, q installflow.Question) (installflow.Response, error) {
	r.mu.Lock()
	r.questions = append(r.questions, q)
	var next *Reply
	if len(r.replies) > 0 {
		next = &r.replies[0]
		r.replies = r.replies[1:]
	}
	r.mu.Unlock()
	select {
	case r.asked <- struct{}{}:
	default:
	}
	if next == nil {
		return installflow.Response{}, installflow.ErrPromptUnavailable
	}
	if next.WaitForCancel {
		<-ctx.Done()
		return installflow.Response{}, ctx.Err()
	}
	return next.Response, next.Err
}
