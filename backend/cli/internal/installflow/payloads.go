// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package installflow

import (
	"encoding/json"
	"fmt"
	"time"
)

// Envelope is one event as a presenter receives it.
type Envelope struct {
	Schema    string
	RunID     string
	Seq       uint64 // from 1, contiguous, assigned at delivery
	Time      time.Time
	ElapsedMS int64 // monotonic, from the hub's start
	Operation Operation
	Kind      Kind
	StepID    StepID
	ParentID  StepID
	Attempt   int // 1-based; 0 when the event is not about a step attempt
	Payload   Payload
}

// MarshalJSON writes the envelope with its time as UTC RFC 3339.
func (e Envelope) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		Schema    string    `json:"schema"`
		RunID     string    `json:"run_id"`
		Seq       uint64    `json:"seq"`
		Time      string    `json:"time"`
		ElapsedMS int64     `json:"elapsed_ms"`
		Operation Operation `json:"operation"`
		Kind      Kind      `json:"kind"`
		StepID    StepID    `json:"step_id,omitempty"`
		ParentID  StepID    `json:"parent_id,omitempty"`
		Attempt   int       `json:"attempt,omitempty"`
		Payload   Payload   `json:"payload"`
	}{e.Schema, e.RunID, e.Seq, e.Time.UTC().Format(time.RFC3339Nano), e.ElapsedMS,
		e.Operation, e.Kind, e.StepID, e.ParentID, e.Attempt, e.Payload})
}

// Payload is the sealed set of event bodies. Only the types in this file
// implement it, so a map, an options struct or an error can never be sent.
type Payload interface {
	Kind() Kind
	// validate checks the closed vocabularies and identifier grammar, and
	// refuses an identifier that is a known secret.
	validate(r *redactor) error
	// scrub returns a copy with every SafeText passed through redaction.
	scrub(r *redactor) Payload
}

// Param is a named value for a message code's template.
type Param struct {
	Name  string   `json:"name"`
	Value SafeText `json:"value"`
}

func validateParams(r *redactor, ps []Param) error {
	for _, p := range ps {
		if err := checkID(r, "param name", p.Name); err != nil {
			return err
		}
	}
	return nil
}

func scrubParams(r *redactor, ps []Param) []Param {
	if ps == nil {
		return nil
	}
	out := make([]Param, len(ps))
	for i, p := range ps {
		out[i] = Param{Name: p.Name, Value: r.text(p.Value)}
	}
	return out
}

// checkID validates an identifier field, including that it is not a secret.
func checkID(r *redactor, field, v string) error {
	if err := validIdentifier(field, v); err != nil {
		return err
	}
	if r.contains(v) {
		return fmt.Errorf("%w: %s holds a secret value", ErrInvalidEvent, field)
	}
	return nil
}

func checkOptionalID(r *redactor, field, v string) error {
	if v == "" {
		return nil
	}
	return checkID(r, field, v)
}

// Target is the safe description of where a run is going. A context name is a
// label, not an identity: ClusterUID stays empty until the run has read it.
type Target struct {
	Mode       MessageCode `json:"mode"`
	Context    SafeText    `json:"context,omitempty"`
	ClusterUID string      `json:"cluster_uid,omitempty"`
}

// ManifestStep is one step the run may execute. An optional step that turns
// out not to apply still finishes, as not_applicable.
type ManifestStep struct {
	ID       StepID `json:"id"`
	Parent   StepID `json:"parent,omitempty"`
	Optional bool   `json:"optional,omitempty"`
}

// RunStarted opens a run.
type RunStarted struct {
	Target   Target         `json:"target"`
	DryRun   bool           `json:"dry_run"`
	Manifest []ManifestStep `json:"manifest"`
}

func (RunStarted) Kind() Kind { return KindRunStarted }

func (p RunStarted) validate(r *redactor) error {
	if err := checkID(r, "target mode", string(p.Target.Mode)); err != nil {
		return err
	}
	if err := checkOptionalID(r, "cluster uid", p.Target.ClusterUID); err != nil {
		return err
	}
	if len(p.Manifest) == 0 {
		return fmt.Errorf("%w: run.started needs a manifest", ErrInvalidEvent)
	}
	seen := map[StepID]bool{}
	for _, s := range p.Manifest {
		if err := checkID(r, "manifest step", string(s.ID)); err != nil {
			return err
		}
		if seen[s.ID] {
			return fmt.Errorf("%w: manifest lists a step twice", ErrInvalidEvent)
		}
		if s.Parent != "" && !seen[s.Parent] {
			return fmt.Errorf("%w: manifest lists a child before its parent", ErrInvalidEvent)
		}
		seen[s.ID] = true
	}
	return nil
}

func (p RunStarted) scrub(r *redactor) Payload {
	p.Target.Context = r.text(p.Target.Context)
	p.Manifest = append([]ManifestStep(nil), p.Manifest...)
	return p
}

// StepStarted opens one attempt of a step. The attempt and the parent travel
// in the Envelope.
type StepStarted struct {
	Message  MessageCode `json:"message"`
	Params   []Param     `json:"params,omitempty"`
	BudgetMS int64       `json:"budget_ms,omitempty"`
}

func (StepStarted) Kind() Kind { return KindStepStarted }

func (p StepStarted) validate(r *redactor) error {
	if err := checkID(r, "message", string(p.Message)); err != nil {
		return err
	}
	if p.BudgetMS < 0 {
		return fmt.Errorf("%w: negative budget", ErrInvalidEvent)
	}
	return validateParams(r, p.Params)
}

func (p StepStarted) scrub(r *redactor) Payload {
	p.Params = scrubParams(r, p.Params)
	return p
}

// StepProgress is a snapshot of one metric of an active step attempt. Values
// may go down: readiness regresses. A full count is never success.
type StepProgress struct {
	MetricID string       `json:"metric_id"`
	Mode     ProgressMode `json:"mode"`
	Current  int64        `json:"current"`
	Total    *int64       `json:"total,omitempty"`
	Unit     Unit         `json:"unit"`
	Source   string       `json:"source"`
}

func (StepProgress) Kind() Kind { return KindStepProgress }

func (p StepProgress) validate(r *redactor) error {
	if err := checkID(r, "metric id", p.MetricID); err != nil {
		return err
	}
	if err := checkID(r, "progress source", p.Source); err != nil {
		return err
	}
	if err := checkEnum("progress mode", p.Mode); err != nil {
		return err
	}
	if err := checkEnum("progress unit", p.Unit); err != nil {
		return err
	}
	if p.Current < 0 || (p.Total != nil && *p.Total < 0) {
		return fmt.Errorf("%w: negative progress", ErrInvalidEvent)
	}
	if p.Mode == ProgressIndeterminate && p.Total != nil {
		return fmt.Errorf("%w: indeterminate progress has no total", ErrInvalidEvent)
	}
	return nil
}

func (p StepProgress) scrub(*redactor) Payload {
	if p.Total != nil {
		t := *p.Total
		p.Total = &t
	}
	return p
}

// StepFinished closes a step attempt that did not fail. The hub sets the
// duration from its own clock.
type StepFinished struct {
	Outcome    StepOutcome `json:"outcome"`
	DurationMS int64       `json:"duration_ms"`
}

func (StepFinished) Kind() Kind { return KindStepFinished }

func (p StepFinished) validate(*redactor) error { return checkEnum("step outcome", p.Outcome) }

func (p StepFinished) scrub(*redactor) Payload { return p }

// StepFailed closes a step attempt that failed or was cancelled. It never
// carries an error's text: Code and Remedy are identifiers a presenter maps
// to its own words. The hub sets the duration.
type StepFailed struct {
	Code         ErrorCode       `json:"code"`
	Retry        RetryClass      `json:"retry"`
	Remedy       MessageCode     `json:"remedy,omitempty"`
	RemedyParams []Param         `json:"remedy_params,omitempty"`
	Cancelled    bool            `json:"cancelled"`
	Effect       EffectCertainty `json:"effect"`
	DurationMS   int64           `json:"duration_ms"`
}

func (StepFailed) Kind() Kind { return KindStepFailed }

func (p StepFailed) validate(r *redactor) error {
	if err := checkID(r, "error code", string(p.Code)); err != nil {
		return err
	}
	if err := checkOptionalID(r, "remedy", string(p.Remedy)); err != nil {
		return err
	}
	if err := checkEnum("retry class", p.Retry); err != nil {
		return err
	}
	if err := checkEnum("effect certainty", p.Effect); err != nil {
		return err
	}
	return validateParams(r, p.RemedyParams)
}

func (p StepFailed) scrub(r *redactor) Payload {
	p.RemedyParams = scrubParams(r, p.RemedyParams)
	return p
}

// LogLine is one sanitised line of output. Logs inform; they never decide
// whether anything succeeded.
type LogLine struct {
	Level  LogLevel    `json:"level"`
	Source string      `json:"source"`
	Code   MessageCode `json:"code,omitempty"`
	Text   SafeText    `json:"text"`
}

func (LogLine) Kind() Kind { return KindLogLine }

func (p LogLine) validate(r *redactor) error {
	if err := checkEnum("log level", p.Level); err != nil {
		return err
	}
	if err := checkID(r, "log source", p.Source); err != nil {
		return err
	}
	return checkOptionalID(r, "log code", string(p.Code))
}

func (p LogLine) scrub(r *redactor) Payload {
	p.Text = r.text(p.Text)
	return p
}

// PromptRequested says the run is waiting on an answer.
type PromptRequested struct {
	ID       PromptID    `json:"id"`
	Prompt   PromptKind  `json:"prompt"`
	Question MessageCode `json:"question"`
	Params   []Param     `json:"params,omitempty"`
}

func (PromptRequested) Kind() Kind { return KindPromptRequested }

func (p PromptRequested) validate(r *redactor) error {
	if err := checkID(r, "prompt id", string(p.ID)); err != nil {
		return err
	}
	if err := checkID(r, "question", string(p.Question)); err != nil {
		return err
	}
	if err := checkEnum("prompt kind", p.Prompt); err != nil {
		return err
	}
	return validateParams(r, p.Params)
}

func (p PromptRequested) scrub(r *redactor) Payload {
	p.Params = scrubParams(r, p.Params)
	return p
}

// PromptResolved says how a prompt ended. It has no field for the answer, by
// construction.
type PromptResolved struct {
	ID          PromptID          `json:"id"`
	Disposition PromptDisposition `json:"disposition"`
}

func (PromptResolved) Kind() Kind { return KindPromptResolved }

func (p PromptResolved) validate(r *redactor) error {
	if err := checkID(r, "prompt id", string(p.ID)); err != nil {
		return err
	}
	return checkEnum("prompt disposition", p.Disposition)
}

func (p PromptResolved) scrub(*redactor) Payload { return p }

// RunCancelling says the run is stopping and what it is still waiting for. It
// comes once, when the cancel is requested; the step the stop ends and the
// summary follow only once the work in flight has settled.
type RunCancelling struct {
	Reason         CancelReason `json:"reason"`
	ActiveStep     StepID       `json:"active_step,omitempty"`
	WaitingForTool bool         `json:"waiting_for_tool"`
	// StopBudgetMS is how long the tool being waited for was given to stop
	// gracefully, when WaitingForTool. It bounds nothing itself.
	StopBudgetMS int64 `json:"stop_budget_ms,omitempty"`
}

func (RunCancelling) Kind() Kind { return KindRunCancelling }

func (p RunCancelling) validate(r *redactor) error {
	if err := checkEnum("cancel reason", p.Reason); err != nil {
		return err
	}
	if p.StopBudgetMS < 0 {
		return fmt.Errorf("%w: negative stop budget", ErrInvalidEvent)
	}
	return checkOptionalID(r, "active step", string(p.ActiveStep))
}

func (p RunCancelling) scrub(*redactor) Payload { return p }

// NextCommand is a safe, structured command a presenter may suggest.
type NextCommand struct {
	Purpose MessageCode `json:"purpose"`
	Argv    []SafeText  `json:"argv"`
}

// Summary is the run's verdict: the run.summary payload and the engine's
// return value. A stream without one means the outcome is unknown.
type Summary struct {
	Outcome       RunOutcome      `json:"outcome"`
	InstallRecord RecordState     `json:"install_record"`
	Readiness     Readiness       `json:"readiness"`
	Claim         ClaimState      `json:"claim"`
	FailedStep    StepID          `json:"failed_step,omitempty"`
	PartialState  EffectCertainty `json:"partial_state"`
	Next          []NextCommand   `json:"next,omitempty"`
	Credentials   []SecretRef     `json:"credentials,omitempty"`
	// DeliveryIncomplete is set by the hub when a control event could not be
	// delivered. Such a run never reports success.
	DeliveryIncomplete bool `json:"delivery_incomplete,omitempty"`
}

func (Summary) Kind() Kind { return KindRunSummary }

// Succeeded reports whether the outcome is one dcctl exits 0 for.
func (s Summary) Succeeded() bool { return s.Outcome == RunSucceeded || s.Outcome == RunPlanned }

// ExitCode is dcctl's exit status for this summary: 0 or 1, as today.
func (s Summary) ExitCode() int {
	if s.Succeeded() {
		return 0
	}
	return 1
}

func (s Summary) validate(r *redactor) error {
	for _, c := range []struct {
		f string
		e enum
	}{
		{"run outcome", s.Outcome}, {"install record", s.InstallRecord},
		{"readiness", s.Readiness}, {"claim", s.Claim}, {"partial state", s.PartialState},
	} {
		if err := checkEnum(c.f, c.e); err != nil {
			return err
		}
	}
	if err := checkOptionalID(r, "failed step", string(s.FailedStep)); err != nil {
		return err
	}
	for _, n := range s.Next {
		if err := checkID(r, "next command purpose", string(n.Purpose)); err != nil {
			return err
		}
	}
	for _, c := range s.Credentials {
		if err := c.validate(); err != nil {
			return err
		}
		if r.contains(c.Namespace) || r.contains(c.Name) || r.contains(c.Key) {
			return fmt.Errorf("%w: secret reference holds a secret value", ErrInvalidEvent)
		}
	}
	return nil
}

// normalise replaces every invalid field with its most cautious value, so a
// malformed summary still closes the stream, as a failure.
func (s Summary) normalise(r *redactor) Summary {
	if !s.Outcome.valid() {
		s.Outcome = RunFailed
	}
	if !s.InstallRecord.valid() {
		s.InstallRecord = RecordUnknown
	}
	if !s.Readiness.valid() {
		s.Readiness = ReadinessUnknown
	}
	if !s.Claim.valid() {
		s.Claim = ClaimUnknown
	}
	if !s.PartialState.valid() {
		s.PartialState = EffectMayHaveChanged
	}
	if s.FailedStep != "" && checkID(r, "failed step", string(s.FailedStep)) != nil {
		s.FailedStep = ""
	}
	var next []NextCommand
	for _, n := range s.Next {
		if checkID(r, "purpose", string(n.Purpose)) == nil {
			next = append(next, n)
		}
	}
	s.Next = next
	var creds []SecretRef
	for _, c := range s.Credentials {
		if c.validate() == nil && !r.contains(c.Namespace) && !r.contains(c.Name) && !r.contains(c.Key) {
			creds = append(creds, c)
		}
	}
	s.Credentials = creds
	return s
}

func (s Summary) scrub(r *redactor) Payload {
	if s.Next != nil {
		next := make([]NextCommand, len(s.Next))
		for i, n := range s.Next {
			argv := make([]SafeText, len(n.Argv))
			for j, a := range n.Argv {
				argv[j] = r.text(a)
			}
			next[i] = NextCommand{Purpose: n.Purpose, Argv: argv}
		}
		s.Next = next
	}
	s.Credentials = append([]SecretRef(nil), s.Credentials...)
	return s
}
