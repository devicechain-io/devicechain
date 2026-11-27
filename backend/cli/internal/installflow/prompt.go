// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package installflow

import (
	"context"
	"errors"
	"fmt"
	"sync"
)

// Errors returned by Ask.
var (
	// ErrPromptDeclined: a confirmation was answered no.
	ErrPromptDeclined = errors.New("installflow: prompt declined")
	// ErrPromptUnavailable: no one can answer, as in a non-interactive run.
	// A missing secret is a failure with a remedy, never a default.
	ErrPromptUnavailable = errors.New("installflow: no one can answer this prompt")
	// ErrInvalidAnswer: the responder's answer does not fit the prompt.
	ErrInvalidAnswer = errors.New("installflow: answer does not fit the prompt")
)

// Prompt is a question the engine needs answered.
type Prompt struct {
	Step     StepID
	Kind     PromptKind
	Question MessageCode
	Params   []Param
	// Choices are the allowed answers of a choice prompt.
	Choices []string
	// YesResolvable marks a confirmation that --yes already answers today.
	// --yes never answers anything else, and never a secret.
	YesResolvable bool
}

// Answer is the engine's view of a resolved prompt.
type Answer struct {
	Disposition PromptDisposition
	Confirmed   bool
	Choice      string
	// Secret is the answer of a secret prompt. The engine owns it from here
	// and zeroes it when done.
	Secret Secret
}

// PromptBroker is the single owner of interactive input while a run is live.
type PromptBroker interface {
	Ask(ctx context.Context, p Prompt) (Answer, error)
}

// Question is what a Responder is shown.
type Question struct {
	ID       PromptID
	Kind     PromptKind
	Question MessageCode
	Params   []Param
	Choices  []string
}

// Response is a Responder's answer.
type Response struct {
	Confirmed bool
	Choice    string
	Secret    Secret
}

// Responder answers questions: the plain adapter reads stdin, the terminal UI
// shows a form. A Responder must return when ctx is done.
type Responder interface {
	Respond(ctx context.Context, q Question) (Response, error)
}

// Broker is the PromptBroker every presenter uses. It serialises prompts, so
// only one is ever outstanding, emits prompt.requested and prompt.resolved
// (disposition only), applies the --yes policy, and checks answers against
// the prompt.
type Broker struct {
	events    Reporter
	responder Responder // nil: nothing can answer
	assumeYes bool

	turn chan struct{}
	mu   sync.Mutex
	n    int
}

// NewBroker returns a broker that reports through events and asks responder.
// A nil responder makes every prompt that --yes does not settle unavailable.
func NewBroker(events Reporter, responder Responder, assumeYes bool) *Broker {
	b := &Broker{events: events, responder: responder, assumeYes: assumeYes, turn: make(chan struct{}, 1)}
	b.turn <- struct{}{}
	return b
}

// Ask resolves one prompt. A declined confirmation returns ErrPromptDeclined
// with the answer; cancellation returns ctx's error.
func (b *Broker) Ask(ctx context.Context, p Prompt) (Answer, error) {
	if !p.Kind.valid() {
		return Answer{}, fmt.Errorf("%w: unknown prompt kind", ErrInvalidEvent)
	}
	if p.Kind == PromptChoice && len(p.Choices) == 0 {
		return Answer{}, fmt.Errorf("%w: a choice prompt needs choices", ErrInvalidEvent)
	}
	select {
	case <-b.turn:
	case <-ctx.Done():
		return Answer{Disposition: PromptCancelled}, ctx.Err()
	}
	defer func() { b.turn <- struct{}{} }()

	b.mu.Lock()
	b.n++
	id := PromptID(fmt.Sprintf("prompt-%d", b.n))
	b.mu.Unlock()

	if err := b.events.Emit(Event{Step: p.Step, Payload: PromptRequested{
		ID: id, Prompt: p.Kind, Question: p.Question, Params: p.Params,
	}}); err != nil {
		return Answer{}, err
	}
	ans, err := b.resolve(ctx, id, p)
	if rerr := b.events.Emit(Event{Step: p.Step, Payload: PromptResolved{ID: id, Disposition: ans.Disposition}}); rerr != nil && err == nil {
		err = rerr
	}
	return ans, err
}

func (b *Broker) resolve(ctx context.Context, id PromptID, p Prompt) (Answer, error) {
	if b.assumeYes && p.Kind == PromptConfirm && p.YesResolvable {
		return Answer{Disposition: PromptAutoConfirmed, Confirmed: true}, nil
	}
	if b.responder == nil {
		return Answer{Disposition: PromptUnavailable}, ErrPromptUnavailable
	}
	resp, err := b.responder.Respond(ctx, Question{ID: id, Kind: p.Kind, Question: p.Question, Params: p.Params, Choices: p.Choices})
	if err != nil || ctx.Err() != nil {
		resp.Secret.Zero()
		if ctx.Err() != nil {
			return Answer{Disposition: PromptCancelled}, ctx.Err()
		}
		if errors.Is(err, ErrPromptUnavailable) {
			return Answer{Disposition: PromptUnavailable}, err
		}
		return Answer{Disposition: PromptCancelled}, err
	}
	switch p.Kind {
	case PromptConfirm:
		resp.Secret.Zero()
		if !resp.Confirmed {
			return Answer{Disposition: PromptDeclined}, ErrPromptDeclined
		}
		return Answer{Disposition: PromptConfirmed, Confirmed: true}, nil
	case PromptChoice:
		resp.Secret.Zero()
		for _, c := range p.Choices {
			if c == resp.Choice {
				return Answer{Disposition: PromptChosen, Choice: c}, nil
			}
		}
		return Answer{Disposition: PromptCancelled}, ErrInvalidAnswer
	default: // PromptSecret
		if resp.Secret.IsEmpty() {
			return Answer{Disposition: PromptCancelled}, ErrInvalidAnswer
		}
		// Best-effort defence in depth: strict registration can refuse an answer
		// whose raw or sanitised form is too short. It is still accepted without
		// coverage here; producers keep exposed answers out of events and output.
		_ = b.events.AddSecret(string(resp.Secret.Expose()))
		return Answer{Disposition: PromptProvided, Secret: resp.Secret}, nil
	}
}
