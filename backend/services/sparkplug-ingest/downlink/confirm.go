// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package downlink

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/devicechain-io/dc-microservice/cmddownlink"
	"github.com/devicechain-io/dc-sparkplug-ingest/config"
	"github.com/devicechain-io/dc-sparkplug-ingest/host"
)

// Confirmation (G19 S4).
//
// 🔴 "CONFIRMED" MEANS THE NODE REPORTED THE REQUESTED VALUE; IT DOES NOT MEAN THE NODE OBEYED.
// Sparkplug has no command acknowledgement. A published write is waited for, and it is
// SUCCESSFUL only when a later report says the metric is at the value that was asked for:
//
//   - the report must not be historical, not null, and not older than the command
//     (its timestamp is no earlier than publishedAt minus the source's clock skew);
//   - a rebirth is confirmed by any NBIRTH of the node after the publish, and a reboot by an NBIRTH
//     whose bdSeq differs from the one before it;
//   - a write to a value the node already held is confirmed at the end of the window as UNCHANGED,
//     but only on a PRIMARY host (a secondary host cannot tell "the node kept the value" from "the
//     primary host overwrote it") and only if nothing contrary was seen;
//   - a window that ends with none of those is UNANSWERED, which command-delivery records as
//     TIMEOUT/UNKNOWN at once instead of at the command's TTL.
//
// The awaiting set lives in the KV bucket (written before the publish) and is mirrored in memory
// for matching. The mirror is matched against reports AFTER the session tracker's lock has been
// released and every response is published off the receive goroutine.

const (
	// defaultSweepEvery is how often the awaiting set is looked at for windows that have ended and
	// for responses that could not be published.
	defaultSweepEvery = time.Second
	// responseRetryEvery is how long a response that could not be published waits for its next try.
	responseRetryEvery = 5 * time.Second
	// seedAttempts bounds the synchronous load at the start of a term; past it the load keeps going
	// in the background while reboots stay refused.
	seedAttempts = 3
	// awaitingLifetime is the lifetime of the awaiting bucket's entries (core/kv declares it).
	awaitingLifetime = 2 * time.Hour
)

// seedRetryDelay is the wait between the synchronous load attempts and seedBackground the one
// between the background ones. Variables so a test can shorten them.
var (
	seedRetryDelay = time.Second
	seedBackground = 5 * time.Second
)

type targetKey struct{ tenant, source, target string }

// Reason is the machine-readable reason a command was resolved the way it was. It travels with the
// stored response (in the evidence for a success, in a small document for an unanswered command), so
// a client such as the console can tell the endings apart without reading the human error text,
// which is free to change. The set is closed: a client that meets a code it does not know must say
// nothing extra rather than guess. reasons_test.go pins it.
type Reason string

const (
	// ReasonConfirmed: the node reported the requested value (or, for a rebirth or reboot, its birth).
	ReasonConfirmed Reason = "confirmed"
	// ReasonUnchanged: the node already held the value and reported nothing different (primary only).
	ReasonUnchanged Reason = "unchanged"
	// ReasonUnconfirmedTimeout: the confirm window ended with no report that settled the command.
	ReasonUnconfirmedTimeout Reason = "unconfirmed_timeout"
	// ReasonSuperseded: a newer write to the same metric on the same device replaced this one.
	ReasonSuperseded Reason = "superseded"
	// ReasonSessionDropped: the broker dropped the Host session right after the publish; the command
	// was most likely refused.
	ReasonSessionDropped Reason = "session_dropped"
	// ReasonSourceDisabled: the source the command went through no longer has commands enabled, so
	// its outcome can no longer be learned.
	ReasonSourceDisabled Reason = "source_disabled"
)

// Reasons lists every Reason this service produces.
var Reasons = []Reason{
	ReasonConfirmed, ReasonUnchanged, ReasonUnconfirmedTimeout, ReasonSuperseded,
	ReasonSessionDropped, ReasonSourceDisabled,
}

// The refusal reasons: a command refused BEFORE the wire is answered UNSENT, and its stored response
// carries {"reason": <one of these>} the same way an unanswered command does. The set is closed and
// pinned by reasons_test.go; the console maps each code to its own sentence.
const (
	// ReasonCommandsNotEnabled: commands are off for the tenant or for the source the device is on.
	ReasonCommandsNotEnabled Reason = "commands_not_enabled"
	// ReasonBrokerRefused: the customer broker refused an earlier publish; commands are paused.
	ReasonBrokerRefused Reason = "broker_refused"
	// ReasonExpired: the command's own expiry passed before it could be sent.
	ReasonExpired Reason = "expired"
	// ReasonInvalidParameters: the command's parameters are not the shape the command takes.
	ReasonInvalidParameters Reason = "invalid_parameters"
	// ReasonUnknownCommand: not one of the commands this service executes.
	ReasonUnknownCommand Reason = "unknown_command"
	// ReasonMetricProtocolControl: the metric is a protocol control metric (Rebirth, Reboot, ...).
	ReasonMetricProtocolControl Reason = "metric_protocol_control"
	// ReasonMetricNotWritable: the metric is not on the source's writable-metric allowlist.
	ReasonMetricNotWritable Reason = "metric_not_writable"
	// ReasonMetricNotDeclared: the node's BIRTH does not declare the metric.
	ReasonMetricNotDeclared Reason = "metric_not_declared"
	// ReasonDatatypeUnsupported: the metric's declared datatype is one commands do not carry.
	ReasonDatatypeUnsupported Reason = "datatype_unsupported"
	// ReasonValueNotAccepted: the value cannot be encoded as the metric's declared datatype.
	ReasonValueNotAccepted Reason = "value_not_accepted"
	// ReasonDatatypeChanged: the node re-birthed with a different datatype for the metric.
	ReasonDatatypeChanged Reason = "datatype_changed"
	// ReasonNodeRebirthing: the node kept re-birthing while the command was being recorded.
	ReasonNodeRebirthing Reason = "node_rebirthing"
	// ReasonNodeOffline: a reboot is sent only to a node that is online when it is dispatched.
	ReasonNodeOffline Reason = "node_offline"
	// ReasonRebootImpact: the command is not declared DISRUPTIVE or DESTRUCTIVE on the profile.
	ReasonRebootImpact Reason = "reboot_impact"
	// ReasonRebootNotAllowed: commands.allowReboot is off on the source.
	ReasonRebootNotAllowed Reason = "reboot_not_allowed"
	// ReasonRebootNotANode: a reboot was addressed to a device rather than an edge node.
	ReasonRebootNotANode Reason = "reboot_not_a_node"
	// ReasonRebootNotDeclared: the node did not declare Node Control/Reboot in its NBIRTH.
	ReasonRebootNotDeclared Reason = "reboot_not_declared"
	// ReasonRebootNotBoolean: Node Control/Reboot is declared with a datatype other than Boolean.
	ReasonRebootNotBoolean Reason = "reboot_not_boolean"
	// ReasonRebootUnverified: the one-reboot-per-window record could not be read yet.
	ReasonRebootUnverified Reason = "reboot_unverified"
	// ReasonRebootRateLimited: the node was rebooted within the window.
	ReasonRebootRateLimited Reason = "reboot_rate_limited"
	// ReasonNoDispatch: the delivery names no dispatch, so it cannot be parked or confirmed.
	ReasonNoDispatch Reason = "no_dispatch"
	// ReasonPublishFailed: the publish failed for a reason none of the above names.
	ReasonPublishFailed Reason = "publish_failed"
)

// RefusalReasons lists every Reason a command refused before the wire can carry.
var RefusalReasons = []Reason{
	ReasonCommandsNotEnabled, ReasonBrokerRefused, ReasonExpired, ReasonInvalidParameters,
	ReasonUnknownCommand, ReasonMetricProtocolControl, ReasonMetricNotWritable, ReasonMetricNotDeclared,
	ReasonDatatypeUnsupported, ReasonValueNotAccepted, ReasonDatatypeChanged, ReasonNodeRebirthing,
	ReasonNodeOffline, ReasonRebootImpact, ReasonRebootNotAllowed, ReasonRebootNotANode,
	ReasonRebootNotDeclared, ReasonRebootNotBoolean, ReasonRebootUnverified, ReasonRebootRateLimited,
	ReasonNoDispatch, ReasonPublishFailed,
}

// outcome is what a command is resolved with.
type outcome struct {
	success bool
	// reason is the machine-readable ending; see Reason.
	reason Reason
	text   string
	// payload is the evidence of a success: a JSON document stored with the command.
	payload string
	// result is the sparkplug_commands_total result counted when the response is published.
	result string
	// confirmed marks an outcome that observes sparkplug_command_confirm_seconds.
	confirmed bool
}

// entry is one awaiting command in memory. Everything but the flags is the stored Awaiting.
type entry struct {
	key    string
	tenant string
	a      Awaiting
	// src is the source the command was published on; it is resolved again when an entry is
	// re-armed by a new term.
	src Source
	// want is the requested value of a write, in the form a report of it has.
	want    host.WireValue
	hasWant bool
	// ref names the BIRTH table a write was addressed against.
	ref host.MetricRef

	// contrary is set when a report of the metric arrived that is not the requested value.
	contrary bool
	// rearmed marks an entry loaded by a new term: it cannot be confirmed UNCHANGED, because the
	// reports that would have shown a contrary value may have been the previous leader's to see.
	rearmed bool
	// decided is the outcome chosen for the entry, held until its response has been published.
	decided *outcome
	// resolving is set while one goroutine owns the entry's resolution.
	resolving bool
	retryAt   time.Time
}

func (o *outcome) stored() *StoredOutcome {
	return &StoredOutcome{Success: o.success, Reason: string(o.reason), Text: o.text, Payload: o.payload, Result: o.result, Confirmed: o.confirmed}
}

func outcomeOf(s *StoredOutcome) *outcome {
	return &outcome{success: s.Success, reason: Reason(s.Reason), text: s.Text, payload: s.Payload, result: s.Result, confirmed: s.Confirmed}
}

func (d *Dispatcher) newEntry(tenant string, a Awaiting, src Source) *entry {
	e := &entry{key: AwaitingKey(tenant, a.Command), tenant: tenant, a: a, src: src}
	if t, ok := host.ParseTarget(a.Target); ok && a.Kind == KindWrite {
		// The table the command was addressed against: UNCHANGED is re-checked on it.
		e.ref = host.MetricRef{Target: t, Name: a.Metric, Datatype: a.Datatype, Epoch: a.SessionEpoch, Gen: a.TableGen}
	}
	if a.Kind == KindWrite {
		var v any
		dec := json.NewDecoder(bytes.NewReader(a.Requested))
		dec.UseNumber()
		if dec.Decode(&v) == nil {
			if w, err := host.RequestedWire(a.Datatype, v); err == nil {
				e.want, e.hasWant = w, true
			}
		}
		if !e.hasWant {
			log.Warn().Str("tenant", tenant).Str("command", a.Command).
				Msg("An awaiting Sparkplug write has no comparable requested value; it can only end with its window.")
		}
	}
	return e
}

func (e *entry) targetKey() targetKey { return targetKey{e.tenant, e.a.Source, e.a.Target} }

// addEntry puts an entry in the in-memory mirror. Called by the publish hook, so the entry is
// matchable before the bytes are on the wire: a node that answers in a millisecond must not
// outrun the record of what it is answering.
func (d *Dispatcher) addEntry(e *entry) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if old, ok := d.entries[e.key]; ok {
		d.dropLocked(old)
	}
	d.entries[e.key] = e
	tk := e.targetKey()
	d.byTarget[tk] = append(d.byTarget[tk], e)
	if e.a.Kind == KindWrite {
		d.byMetric[awaitKey{e.tenant, e.a.Device, e.a.Metric}] = e
	}
	d.gaugeLocked()
}

// addEntryIfAbsent is addEntry for the start of a term: the entry is added only if nothing of that
// key is in the mirror AND the key was not settled while the bucket could not be read (the
// listing the term is working from may predate that settlement). The check and the add are one
// critical section, so nothing can settle the key between them.
func (d *Dispatcher) addEntryIfAbsent(e *entry) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	if _, have := d.entries[e.key]; have {
		return false
	}
	if _, settled := d.settledUnseeded[e.key]; settled {
		return false
	}
	d.entries[e.key] = e
	tk := e.targetKey()
	d.byTarget[tk] = append(d.byTarget[tk], e)
	if e.a.Kind == KindWrite {
		d.byMetric[awaitKey{e.tenant, e.a.Device, e.a.Metric}] = e
	}
	d.gaugeLocked()
	return true
}

// forget removes an entry from the in-memory mirror by key.
func (d *Dispatcher) forget(key string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if e, ok := d.entries[key]; ok {
		d.dropLocked(e)
		d.gaugeLocked()
	}
}

func (d *Dispatcher) dropLocked(e *entry) {
	delete(d.entries, e.key)
	tk := e.targetKey()
	list := d.byTarget[tk]
	for i, x := range list {
		if x == e {
			list = append(list[:i:i], list[i+1:]...)
			break
		}
	}
	if len(list) == 0 {
		delete(d.byTarget, tk)
	} else {
		d.byTarget[tk] = list
	}
	if e.a.Kind == KindWrite {
		k := awaitKey{e.tenant, e.a.Device, e.a.Metric}
		if d.byMetric[k] == e {
			delete(d.byMetric, k)
		}
	}
}

func (d *Dispatcher) gaugeLocked() {
	d.nEntries.Store(int64(len(d.entries)))
	if d.metrics.Awaiting != nil {
		d.metrics.Awaiting.Set(float64(len(d.entries)))
	}
}

// ---- matching ----

// onReports is a source's callback with what an accepted message said. It is called on the
// receive goroutine, after the session tracker's lock was released: the matching is in memory, and
// every response is published on a goroutine of its own.
func (d *Dispatcher) onReports(src Source, tenant string, reports []host.Report) {
	if d.nEntries.Load() == 0 {
		return
	}
	skew := src.Commands().ClockSkew()
	sid := src.SourceID()
	var won []*entry
	d.mu.Lock()
	for _, r := range reports {
		for _, e := range d.byTarget[targetKey{tenant, sid, r.Target.String()}] {
			if e.resolving || e.decided != nil {
				continue
			}
			if out, ok := e.observe(r, skew); ok {
				e.decided, e.resolving = out, true
				won = append(won, e)
			}
		}
	}
	d.mu.Unlock()
	d.settleAsync(won)
}

// observe decides whether a report settles the entry, and notes a contrary one.
func (e *entry) observe(r host.Report, skew time.Duration) (*outcome, bool) {
	switch e.a.Kind {
	case KindWrite:
		if r.Kind != host.ReportMetric || r.Name != e.a.Metric || !e.hasWant {
			return nil, false
		}
		// A report older than the command says nothing about what the command did.
		if r.At.Before(e.a.PublishedAt.Add(-skew)) {
			return nil, false
		}
		if r.Value != e.want {
			e.contrary = true
			return nil, false
		}
		at := r.At
		return e.confirmed(r.Via, &at, nil, nil), true
	case KindRebirth:
		if r.Kind != host.ReportNodeBirth || r.ReceivedAt.Before(e.a.PublishedAt) {
			return nil, false
		}
		return e.confirmed(r.Via, &r.ReceivedAt, nil, nil), true
	case KindReboot:
		if r.Kind != host.ReportNodeBirth || r.ReceivedAt.Before(e.a.PublishedAt) {
			return nil, false
		}
		// A reboot is proved by a DIFFERENT birth sequence number, and only by one: the same bdSeq
		// is the node re-announcing the session it was in (a rebirth), and a birth, or a baseline,
		// without a bdSeq proves nothing either way. Without two comparable values the command stays
		// awaiting and ends UNANSWERED at its window, rather than reporting an unproven reboot.
		if !e.a.HasBdSeq || !r.HasBdSeq || r.BdSeq == e.a.BdSeq {
			return nil, false
		}
		after, before := r.BdSeq, e.a.BdSeq
		return e.confirmed(r.Via, &r.ReceivedAt, &before, &after), true
	}
	return nil, false
}

// evidence is the document stored with a confirmed command (response_payload).
type evidence struct {
	Kind        string          `json:"kind"`
	Reason      Reason          `json:"reason"`
	Metric      string          `json:"metric,omitempty"`
	Value       json.RawMessage `json:"value,omitempty"`
	ReportedAt  *time.Time      `json:"reportedAt,omitempty"`
	Via         string          `json:"via"`
	Since       *time.Time      `json:"since,omitempty"`
	HostRole    string          `json:"hostRole"`
	BdSeqBefore *uint64         `json:"bdSeqBefore,omitempty"`
	BdSeqAfter  *uint64         `json:"bdSeqAfter,omitempty"`
	Wire        AwaitingWire    `json:"wire"`
}

func (e *entry) confirmed(via string, at *time.Time, before, after *uint64) *outcome {
	return e.confirmedWith(via, at, nil, before, after, ResultConfirmed)
}

func (e *entry) confirmedWith(via string, at, since *time.Time, before, after *uint64, result string) *outcome {
	reason := ReasonConfirmed
	if via == "UNCHANGED" {
		reason = ReasonUnchanged
	}
	ev := evidence{
		Reason: reason, Kind: e.a.Kind, Metric: e.a.Metric, Value: e.a.Requested, ReportedAt: at, Via: via, Since: since,
		HostRole: e.a.HostRole, BdSeqBefore: before, BdSeqAfter: after, Wire: e.a.Wire,
	}
	b, _ := json.Marshal(ev)
	return &outcome{success: true, reason: ev.Reason, payload: string(b), result: result, confirmed: result == ResultConfirmed}
}

// expiry is the outcome of an entry whose window ended with no report that settled it. last reads
// the node's last-reported value from the source (nil: not available, as for a re-armed entry).
//
// UNCHANGED is decided HERE, from a fresh read, not from the value read when the command was
// published: that read came before the entry joined the mirror, so a contrary report could have
// landed between the two. The value must still be the requested one, on the same BIRTH table (a
// node that died or re-birthed in the meantime has a new table, and says nothing about the old).
// The caller must not hold d.mu: last takes the session tracker's lock.
func (e *entry) expiry(last func(host.MetricRef) (host.WireValue, time.Time, bool)) *outcome {
	if e.a.Kind == KindWrite && e.a.AtRequestedValue && !e.contrary && !e.rearmed && last != nil && e.hasWant &&
		e.a.HostRole == config.HostRolePrimary {
		if v, _, ok := last(e.ref); ok && v == e.want {
			since := e.a.LastReportedAt
			return e.confirmedWith("UNCHANGED", nil, &since, nil, nil, ResultConfirmedUnchanged)
		}
	}
	w := e.a.Deadline.Sub(e.a.PublishedAt).Round(time.Second)
	var what string
	switch e.a.Kind {
	case KindRebirth:
		what = fmt.Sprintf("no new birth from %s within %s", e.a.Target, w)
	case KindReboot:
		what = fmt.Sprintf("no birth with a new bdSeq from %s within %s", e.a.Target, w)
	default:
		what = fmt.Sprintf("no report of %s at the requested value within %s", e.a.Metric, w)
	}
	return &outcome{
		reason: ReasonUnconfirmedTimeout,
		text:   what + "; Sparkplug has no command acknowledgement — the node may have ignored or rejected it, or the broker dropped the publish",
		result: ResultUnconfirmed,
	}
}

// ---- resolution ----

// settleAsync resolves the entries on a goroutine of its own, off the receive goroutine. The
// goroutine is tracked, so the term waits for it.
func (d *Dispatcher) settleAsync(es []*entry) {
	if len(es) == 0 {
		return
	}
	d.mu.Lock()
	open := d.termOpen
	if open {
		d.bg.Add(1)
	}
	d.mu.Unlock()
	if !open {
		// The term has ended: whoever claimed these must give them back, or they are stuck. They
		// stay in KV for the next leader, which re-arms them.
		d.mu.Lock()
		for _, e := range es {
			e.resolving, e.decided = false, nil
		}
		d.mu.Unlock()
		return
	}
	go func() {
		defer d.bg.Done()
		for _, e := range es {
			d.settle(e)
		}
	}()
}

// settle publishes an entry's decided outcome and, once the response is out, deletes its record and
// forgets it. The caller owns the entry (resolving is set). A response that cannot be published
// leaves the entry decided, to be tried again by the sweep: the command is not forgotten.
func (d *Dispatcher) settle(e *entry) bool {
	out := e.decided
	env := cmddownlink.ResponseEnvelope{CommandToken: e.a.Command, Success: out.success, DispatchNonce: e.a.Nonce}
	if out.success {
		p := out.payload
		env.Payload = &p
	} else {
		env.Reach = cmddownlink.ReachUnanswered
		t := out.text
		env.Error = &t
		if out.reason != "" {
			// The stored response of an unanswered command carries only the reason code; the
			// human text stays in the error column.
			p := `{"reason":"` + string(out.reason) + `"}`
			env.Payload = &p
		}
	}
	if err := d.publishResponse(e.tenant, e.a.Device, env); err != nil {
		d.mu.Lock()
		e.resolving = false
		e.retryAt = d.now().Add(responseRetryEvery)
		persist := e.a.Decided == nil
		if persist {
			e.a.Decided = out.stored()
		}
		a := e.a
		d.mu.Unlock()
		if persist {
			// Keep what was decided with the record: if this leader goes before the response is
			// out, the next one settles the command with THIS outcome. A command that was confirmed
			// must not come back as a timeout because the confirmation could not be published.
			pctx, cancel := context.WithTimeout(context.Background(), bgTimeout)
			if perr := d.store.Put(pctx, e.key, a); perr != nil {
				d.count(ResultKVFailed)
				log.Warn().Err(perr).Str("key", e.key).Msg("Could not store a decided outcome whose response could not be published.")
			}
			cancel()
		}
		return false
	}
	d.count(out.result)
	if out.confirmed && d.metrics.ConfirmSeconds != nil {
		d.metrics.ConfirmSeconds.Observe(d.now().Sub(e.a.PublishedAt).Seconds())
	}
	d.deleteEntry(e.key)
	d.finish(e)
	return true
}

// finish takes an entry out of the mirror once it has been settled for good. While the bucket has
// not been read yet it also remembers the key, so a listing taken before this settlement cannot
// bring the entry back.
func (d *Dispatcher) finish(e *entry) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.unseeded.Load() {
		d.settledUnseeded[e.key] = struct{}{}
	}
	if cur, ok := d.entries[e.key]; ok {
		d.dropLocked(cur)
		d.gaugeLocked()
	}
}

// giveUpAt is when a response that cannot be published stops being retried: past the command's own
// expiry command-delivery has ended the row itself, and past the bucket's lifetime the record
// would have aged out anyway.
func (e *entry) giveUpAt() time.Time {
	t := e.a.PublishedAt.Add(awaitingLifetime)
	if !e.a.Expires.IsZero() && e.a.Expires.Before(t) {
		t = e.a.Expires
	}
	return t
}

// resolveNow claims an entry for a resolution of the caller's own (superseded, tripped) and
// settles it. It reports false if another resolution already owns it.
func (d *Dispatcher) resolveNow(e *entry, out *outcome) bool {
	d.mu.Lock()
	if e.resolving || e.decided != nil {
		d.mu.Unlock()
		return false
	}
	e.decided, e.resolving = out, true
	d.mu.Unlock()
	d.settle(e)
	return true
}

// sweep resolves the entries whose window has ended, and retries the responses that failed.
func (d *Dispatcher) sweep() {
	now := d.now()
	var due, ended, abandoned []*entry
	d.mu.Lock()
	for _, e := range d.entries {
		if e.resolving {
			continue
		}
		if e.decided != nil {
			if !now.Before(e.giveUpAt()) {
				e.resolving = true
				abandoned = append(abandoned, e)
			} else if !now.Before(e.retryAt) {
				e.resolving = true
				due = append(due, e)
			}
			continue
		}
		if !now.Before(e.a.Deadline) {
			e.resolving = true // claimed; its outcome is decided below, off the lock
			ended = append(ended, e)
		}
	}
	d.mu.Unlock()
	for _, e := range abandoned {
		log.Warn().Str("tenant", e.tenant).Str("command", e.a.Command).
			Msg("Giving up publishing a Sparkplug command's response: the command has expired, so command-delivery has ended it itself.")
		d.deleteEntry(e.key)
		d.finish(e)
	}
	for _, e := range ended {
		// Decided off d.mu: the UNCHANGED check reads the session tracker, which has a lock of its own.
		var last func(host.MetricRef) (host.WireValue, time.Time, bool)
		if e.src != nil {
			last = e.src.LastReported
		}
		out := e.expiry(last)
		d.mu.Lock()
		e.decided = out
		d.mu.Unlock()
		due = append(due, e)
	}
	for _, e := range due {
		d.settle(e)
	}
}

func (d *Dispatcher) sweepLoop(ctx context.Context) {
	t := time.NewTicker(d.sweepEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			d.sweep()
		}
	}
}

// ---- the start of a term ----

// seedTerm loads what the last term left in the bucket: the reboot marks, so the rule of one reboot
// per window survives a change of leader, and the awaiting entries, which are re-armed with the
// time left on their deadline (or resolved at once if it has passed). It runs before the term
// serves anything. Entries of tenants this service has no source for are left alone.
func (d *Dispatcher) seedTerm(ctx context.Context) error {
	stored, err := d.store.List(ctx)
	if err != nil {
		return err
	}
	for _, k := range stored.Unreadable {
		log.Warn().Str("key", k).Msg("An entry in the Sparkplug awaiting bucket could not be read; it is left to age out.")
	}
	var immediate []*entry
	rearmed := 0
	d.mu.Lock()
	for _, m := range stored.Reboots {
		if _, ours := d.byTenant[m.Tenant]; !ours {
			continue
		}
		k := nodeKey{m.Tenant, m.Group, m.Node}
		if m.At.After(d.reboots[k]) {
			d.reboots[k] = m.At
		}
	}
	d.mu.Unlock()
	for key, a := range stored.Entries {
		tenant, _, _ := strings.Cut(key, "|")
		if _, ours := d.byTenant[tenant]; !ours {
			continue
		}
		if d.tenantDeleted(tenant) {
			d.deleteEntry(key)
			continue
		}
		var src Source
		for _, s := range d.byTenant[tenant] {
			if s.Commands().Enabled && s.SourceID() == a.Source {
				src = s
			}
		}
		e := d.newEntry(tenant, a, src)
		e.rearmed = true
		// The outcome is decided, and the entry claimed, BEFORE it is added: once it is in the
		// mirror a report or a sweep can reach it, and an entry that is added first and claimed
		// afterwards can be resolved twice.
		var out *outcome
		switch {
		case a.Decided != nil:
			// Its response could not be published by the last leader: settle it with what was decided.
			out = outcomeOf(a.Decided)
		case src == nil:
			out = &outcome{
				reason: ReasonSourceDisabled,
				result: ResultUnconfirmed,
				text:   "the Sparkplug source this command was sent through no longer has commands enabled, so its outcome can no longer be learned",
			}
		case !d.now().Before(a.Deadline):
			out = e.expiry(nil)
		}
		if out != nil {
			e.decided, e.resolving = out, true
		}
		if !d.addEntryIfAbsent(e) {
			continue
		}
		if out != nil {
			immediate = append(immediate, e)
		} else {
			rearmed++
			d.count(ResultRearmed)
		}
	}
	for _, e := range immediate {
		d.settle(e)
	}
	if rearmed+len(immediate) > 0 {
		log.Info().Int("rearmed", rearmed).Int("resolved", len(immediate)).
			Msg("Picked up the Sparkplug commands the previous term left awaiting an outcome.")
	}
	return nil
}

// seeded ends the unseeded phase: the bucket has been read, so the keys remembered as settled
// meanwhile are no longer needed.
func (d *Dispatcher) seeded() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.settledUnseeded = map[string]struct{}{}
	d.unseeded.Store(false)
}

// seedStart loads the previous term's state before the term serves anything. Reboots are refused
// until it has loaded: the rule of one per window cannot be applied to a history that is not
// there. If the bucket cannot be read it keeps trying in the background.
func (d *Dispatcher) seedStart(ctx context.Context, spawn func(func())) {
	d.unseeded.Store(true)
	for attempt := 0; attempt < seedAttempts; attempt++ {
		if ctx.Err() != nil {
			return
		}
		err := d.seedTerm(ctx)
		if err == nil {
			d.seeded()
			return
		}
		log.Warn().Err(err).Int("attempt", attempt+1).Msg("Could not read the Sparkplug awaiting bucket at the start of the term.")
		select {
		case <-ctx.Done():
			return
		case <-time.After(seedRetryDelay):
		}
	}
	log.Error().Msg("The Sparkplug awaiting bucket is unreadable: commands are served, reboots are refused, and awaiting commands are not re-armed until it can be read.")
	spawn(func() {
		for ctx.Err() == nil {
			select {
			case <-ctx.Done():
				return
			case <-time.After(seedBackground):
			}
			if err := d.seedTerm(ctx); err == nil {
				d.seeded()
				return
			}
		}
	})
}
