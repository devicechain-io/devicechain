// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package installjson

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"time"
	"unicode/utf8"

	"github.com/devicechain-io/dcctl/internal/installflow"
)

// ErrNotAnEvent: a line of the stream is not exactly one event envelope as the
// presenter writes it.
var ErrNotAnEvent = errors.New("installjson: not an event line")

// wireEnvelope is the envelope as it travels.
type wireEnvelope struct {
	Schema    string                `json:"schema"`
	RunID     string                `json:"run_id"`
	Seq       uint64                `json:"seq"`
	Time      string                `json:"time"`
	ElapsedMS int64                 `json:"elapsed_ms"`
	Operation installflow.Operation `json:"operation"`
	Kind      installflow.Kind      `json:"kind"`
	StepID    installflow.StepID    `json:"step_id"`
	ParentID  installflow.StepID    `json:"parent_id"`
	Attempt   int                   `json:"attempt"`
	Payload   json.RawMessage       `json:"payload"`
}

// Decode reads one line of the stream (without its newline) back into the
// envelope it was written from. It is strict: an unknown kind, an unknown field,
// trailing bytes, a terminal escape, invalid UTF-8, or any line that is not
// byte for byte what the presenter writes for the envelope it decodes to, is
// ErrNotAnEvent. It is the reference reader of the stream, and the check its
// tests make of every line.
//
// A consumer that wants to tolerate a later minor's additions (new kinds, new
// fields) should not use it: it is a conformance check, not a lenient parser.
func Decode(line []byte) (installflow.Envelope, error) {
	bad := func(format string, a ...any) (installflow.Envelope, error) {
		return installflow.Envelope{}, fmt.Errorf("%w: "+format, append([]any{ErrNotAnEvent}, a...)...)
	}
	if !utf8.Valid(line) {
		return bad("invalid UTF-8")
	}
	if bytes.ContainsAny(line, "\x1b\r\n") || bytes.ContainsRune(line, 0x9b) {
		return bad("control bytes on the line")
	}
	var w wireEnvelope
	if err := strictUnmarshal(line, &w); err != nil {
		return bad("%v", err)
	}
	at, err := time.Parse(time.RFC3339Nano, w.Time)
	if err != nil {
		return bad("time: %v", err)
	}
	p, err := decodePayload(w.Kind, w.Payload)
	if err != nil {
		return bad("%s payload: %v", w.Kind, err)
	}
	env := installflow.Envelope{
		Schema: w.Schema, RunID: w.RunID, Seq: w.Seq, Time: at, ElapsedMS: w.ElapsedMS,
		Operation: w.Operation, Kind: w.Kind, StepID: w.StepID, ParentID: w.ParentID,
		Attempt: w.Attempt, Payload: p,
	}
	again, err := json.Marshal(env)
	if err != nil {
		return bad("re-encoding: %v", err)
	}
	if !bytes.Equal(again, line) {
		return bad("the line is not the presenter's encoding of the event it holds")
	}
	return env, nil
}

func decodePayload(k installflow.Kind, raw json.RawMessage) (installflow.Payload, error) {
	switch k {
	case installflow.KindRunStarted:
		return into[installflow.RunStarted](raw)
	case installflow.KindStepStarted:
		return into[installflow.StepStarted](raw)
	case installflow.KindStepProgress:
		return into[installflow.StepProgress](raw)
	case installflow.KindStepFinished:
		return into[installflow.StepFinished](raw)
	case installflow.KindStepFailed:
		return into[installflow.StepFailed](raw)
	case installflow.KindLogLine:
		return into[installflow.LogLine](raw)
	case installflow.KindPromptRequested:
		return into[installflow.PromptRequested](raw)
	case installflow.KindPromptResolved:
		return into[installflow.PromptResolved](raw)
	case installflow.KindRunCancelling:
		return into[installflow.RunCancelling](raw)
	case installflow.KindPreflightObservation:
		return into[installflow.PreflightObservation](raw)
	case installflow.KindRunSummary:
		return into[installflow.Summary](raw)
	}
	return nil, fmt.Errorf("unknown kind %q", k)
}

func into[T installflow.Payload](raw json.RawMessage) (installflow.Payload, error) {
	var v T
	if err := strictUnmarshal(raw, &v); err != nil {
		return nil, err
	}
	return v, nil
}

func strictUnmarshal(b []byte, v any) error {
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		return err
	}
	if d.More() {
		return errors.New("trailing data")
	}
	if _, err := d.Token(); err == nil {
		return errors.New("trailing data")
	}
	return nil
}

// DecodeStream splits data into lines and decodes each. Every line must end in
// a newline and decode; anything else — an empty line, a stray byte, a final
// line with no newline — is ErrNotAnEvent, naming the line.
func DecodeStream(data []byte) ([]installflow.Envelope, error) {
	var out []installflow.Envelope
	for n := 1; len(data) > 0; n++ {
		i := bytes.IndexByte(data, '\n')
		if i < 0 {
			return out, fmt.Errorf("%w: line %d has no newline", ErrNotAnEvent, n)
		}
		env, err := Decode(data[:i])
		if err != nil {
			return out, fmt.Errorf("line %d: %w", n, err)
		}
		out = append(out, env)
		data = data[i+1:]
	}
	return out, nil
}
