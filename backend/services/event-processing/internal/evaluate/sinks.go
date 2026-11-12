// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package evaluate

import (
	"context"
	"time"

	"github.com/devicechain-io/dc-event-processing/internal/react"
	"github.com/devicechain-io/dc-event-processing/internal/rules"
	"github.com/devicechain-io/dc-event-processing/internal/runtime"
)

// recorder is the Dispatcher's three sinks in one value. Each Dispatch/Send appends the request it is
// handed to the current edge's intents and returns nil, so the Dispatcher counts the action as
// delivered and moves on. Nothing leaves the process: there is no writer, no client and no network
// in this type, which is what "executes nothing" means here.
//
// It is used from one goroutine (a run is sequential), so it takes no lock.
type recorder struct {
	edge    Edge
	current []Intent
}

// begin starts collecting the intents of one edge.
func (r *recorder) begin(e Edge) { r.edge, r.current = e, nil }

// take returns what the current edge caused and clears it.
func (r *recorder) take() []Intent {
	out := r.current
	r.current = nil
	return out
}

func (r *recorder) base(kind, token string) Intent {
	return Intent{At: r.edge.At, Series: r.edge.Series, RuleToken: r.edge.RuleToken, Raise: r.edge.Raise, Kind: kind, Token: token}
}

// Send records a sendCommand request.
func (r *recorder) Send(_ context.Context, req react.CommandRequest) error {
	in := r.base(KindSendCommand, req.Token)
	in.Command, in.Payload = req.Command, req.Payload
	if !req.ExpiresAt.IsZero() {
		t := req.ExpiresAt.UTC()
		in.ExpiresAt = &t
	}
	r.current = append(r.current, in)
	return nil
}

// alarmSink and connectorSink adapt the recorder to the other two sink interfaces; the
// method is named Dispatch on both, which one type cannot implement twice.
type alarmSink struct{ r *recorder }

type connectorSink struct{ r *recorder }

// newSinks is deliberately private. Internal tests replace it with publishing sinks to prove
// the stream probe detects an isolation breach; service callers cannot replace the sinks.
var newSinks = func(rec *recorder) (react.CommandSink, react.AlarmSink, react.ConnectorSink) {
	return rec, alarmSink{rec}, connectorSink{rec}
}

// Dispatch records a raise or a clear.
func (a alarmSink) Dispatch(_ context.Context, req react.AlarmRequest) error {
	kind := KindRaiseAlarm
	if req.Edge == runtime.EdgeResolved {
		kind = KindClearAlarm
	}
	in := a.r.base(kind, req.Token)
	in.AlarmKey, in.Severity, in.Value = req.AlarmKey, req.Severity, req.Value
	a.r.current = append(a.r.current, in)
	return nil
}

// Dispatch records a connector request.
func (c connectorSink) Dispatch(_ context.Context, req react.ConnectorRequest) error {
	in := c.r.base(string(req.Action.Type), req.Token)
	in.Body = req.Payload
	switch {
	case req.Action.HTTPCall != nil:
		in.URL, in.Method, in.TimeoutMs = req.Action.HTTPCall.URL, req.Action.HTTPCall.Method, req.Action.HTTPCall.TimeoutMs
		if in.Method == "" {
			in.Method = "POST"
		}
	case req.Action.Publish != nil:
		in.Connector, in.TimeoutMs = req.Action.Publish.ConnectorRef, req.Action.Publish.TimeoutMs
	}
	c.r.current = append(c.r.current, in)
	return nil
}

// defResolver resolves a rule id to the definition the run compiled it from: the REACT layer reads
// the rule from a store in production and from this map here.
type defResolver map[string]rules.Rule

func (d defResolver) Resolve(_ context.Context, ruleID string) (rules.Rule, bool, error) {
	r, ok := d[ruleID]
	return r, ok, nil
}

// nopRuntimeMetrics and nopReactMetrics discard the counters a run has no use for.
type nopRuntimeMetrics struct{}

func (nopRuntimeMetrics) RecordFanout(int, int)                      {}
func (nopRuntimeMetrics) RecordDerivedPublished()                    {}
func (nopRuntimeMetrics) RecordDerivedRejected(runtime.RejectReason) {}
func (nopRuntimeMetrics) RecordDerivedPublishAck(time.Duration)      {}

type nopReactMetrics struct{}

func (nopReactMetrics) RecordDispatched(string)          {}
func (nopReactMetrics) RecordOrphan()                    {}
func (nopReactMetrics) RecordNotEnabled(string)          {}
func (nopReactMetrics) RecordConnectorShed(string)       {}
func (nopReactMetrics) RecordPermanentlyRejected(string) {}
func (nopReactMetrics) RecordDropped(string)             {}
