// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package testkit

import (
	"errors"
	"fmt"
	"sync"

	"github.com/devicechain-io/dcctl/internal/installflow"
)

// CaptureSink records every envelope it is handed. Hold stalls it, as a
// presenter stuck on terminal I/O would be, until Release.
type CaptureSink struct {
	mu   sync.Mutex
	envs []installflow.Envelope
	gate chan struct{}
}

// Deliver records e, first waiting while the sink is held.
func (s *CaptureSink) Deliver(e installflow.Envelope) {
	s.mu.Lock()
	g := s.gate
	s.mu.Unlock()
	if g != nil {
		<-g
	}
	s.mu.Lock()
	s.envs = append(s.envs, e)
	s.mu.Unlock()
}

// Hold makes Deliver block until Release.
func (s *CaptureSink) Hold() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.gate == nil {
		s.gate = make(chan struct{})
	}
}

// Release lets a held sink continue.
func (s *CaptureSink) Release() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.gate != nil {
		close(s.gate)
		s.gate = nil
	}
}

// Envelopes returns a copy of what has been delivered so far.
func (s *CaptureSink) Envelopes() []installflow.Envelope {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]installflow.Envelope(nil), s.envs...)
}

// Kinds returns the kinds delivered so far, in order.
func (s *CaptureSink) Kinds() []installflow.Kind {
	var out []installflow.Kind
	for _, e := range s.Envelopes() {
		out = append(out, e.Kind)
	}
	return out
}

type attemptKey struct {
	step    installflow.StepID
	attempt int
}

type metricKey struct {
	attemptKey
	metric string
}

// VerifyStream checks a complete stream against the protocol's invariants:
// one schema and run id; seq contiguous from 1; run.started first; exactly one
// run.summary and it last; every step attempt started once (parent first,
// attempts consecutive) and ended exactly once, with its progress inside it;
// every prompt requested once and resolved once. A count of actions — work a
// tool reports done, unlike a readiness snapshot, which may go down — never
// decreases within its attempt, never passes its total, and keeps its total
// once one is reported. It returns every violation.
func VerifyStream(envs []installflow.Envelope) error {
	var errs []error
	bad := func(i int, format string, args ...any) {
		errs = append(errs, fmt.Errorf("event %d: "+format, append([]any{i}, args...)...))
	}
	if len(envs) == 0 {
		return errors.New("empty stream: no summary, so the outcome is unknown")
	}
	active := map[attemptKey]bool{}
	lastAttempt := map[installflow.StepID]int{}
	activeStep := map[installflow.StepID]bool{}
	prompts := map[installflow.PromptID]bool{}
	counts := map[metricKey]installflow.StepProgress{}
	summaries := 0
	for i, e := range envs {
		if e.Schema != installflow.Schema {
			bad(i, "schema %q", e.Schema)
		}
		if e.RunID != envs[0].RunID {
			bad(i, "run id changed")
		}
		if e.Seq != uint64(i+1) {
			bad(i, "seq %d, want %d", e.Seq, i+1)
		}
		if e.Payload == nil || e.Payload.Kind() != e.Kind {
			bad(i, "kind %s does not match its payload", e.Kind)
			continue
		}
		if i == 0 && e.Kind != installflow.KindRunStarted && e.Kind != installflow.KindRunSummary {
			bad(i, "first event is %s, not run.started", e.Kind)
		}
		if summaries > 0 {
			bad(i, "%s after run.summary", e.Kind)
		}
		k := attemptKey{e.StepID, e.Attempt}
		switch p := e.Payload.(type) {
		case installflow.StepStarted:
			if activeStep[e.StepID] {
				bad(i, "step %s started while active", e.StepID)
			}
			if e.Attempt != lastAttempt[e.StepID]+1 {
				bad(i, "step %s attempt %d after attempt %d", e.StepID, e.Attempt, lastAttempt[e.StepID])
			}
			if e.ParentID != "" && !activeStep[e.ParentID] {
				bad(i, "step %s started before its parent %s", e.StepID, e.ParentID)
			}
			active[k], activeStep[e.StepID], lastAttempt[e.StepID] = true, true, e.Attempt
		case installflow.StepProgress:
			if !active[k] {
				bad(i, "progress for %s attempt %d outside the attempt", e.StepID, e.Attempt)
			}
			if p.Unit == installflow.UnitActions {
				mk := metricKey{k, p.MetricID}
				if prev, seen := counts[mk]; seen {
					if p.Current < prev.Current {
						bad(i, "%s went back from %d to %d within attempt %d", p.MetricID, prev.Current, p.Current, e.Attempt)
					}
					if prev.Total != nil && (p.Total == nil || *p.Total != *prev.Total) {
						bad(i, "%s changed its total within attempt %d", p.MetricID, e.Attempt)
					}
				}
				if p.Total != nil && p.Current > *p.Total {
					bad(i, "%s reports %d of %d", p.MetricID, p.Current, *p.Total)
				}
				counts[mk] = p
			}
		case installflow.PreflightObservation:
			if !active[k] {
				bad(i, "preflight observation for %s attempt %d outside the attempt", e.StepID, e.Attempt)
			}
		case installflow.StepFinished, installflow.StepFailed:
			if !active[k] {
				bad(i, "step %s attempt %d ended without being active", e.StepID, e.Attempt)
			}
			delete(active, k)
			activeStep[e.StepID] = false
		case installflow.PromptRequested:
			if _, seen := prompts[p.ID]; seen {
				bad(i, "prompt %s requested twice", p.ID)
			}
			prompts[p.ID] = true
		case installflow.PromptResolved:
			if !prompts[p.ID] {
				bad(i, "prompt %s resolved while not outstanding", p.ID)
			}
			prompts[p.ID] = false
		case installflow.Summary:
			summaries++
		}
	}
	if summaries != 1 {
		errs = append(errs, fmt.Errorf("%d run.summary events, want exactly 1", summaries))
	}
	for k := range active {
		errs = append(errs, fmt.Errorf("step %s attempt %d never ended", k.step, k.attempt))
	}
	for id, open := range prompts {
		if open {
			errs = append(errs, fmt.Errorf("prompt %s never resolved", id))
		}
	}
	return errors.Join(errs...)
}
