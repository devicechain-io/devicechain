// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/devicechain-io/dc-event-processing/internal/detect/core"
	"github.com/devicechain-io/dc-event-processing/internal/rules"
	dccore "github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/messaging"
)

// capturedWrite records one WriteMessages call: the tenant the writer would scope the
// subject to (from context) and the payload.
type capturedWrite struct {
	tenant  string
	payload []byte
}

// fakeWriter records writes and, when err is set, fails them (a broker outage).
type fakeWriter struct {
	writes []capturedWrite
	err    error
}

func (w *fakeWriter) WriteMessages(ctx context.Context, msgs ...messaging.Message) error {
	if w.err != nil {
		return w.err
	}
	tenant, _ := dccore.TenantFromContext(ctx)
	for _, m := range msgs {
		w.writes = append(w.writes, capturedWrite{tenant: tenant, payload: m.Value})
	}
	return nil
}

func (w *fakeWriter) HandleResponse(error) {}

// fakeMetrics counts publisher outcomes.
type fakeMetrics struct {
	published int
	rejected  map[RejectReason]int
}

func newFakeMetrics() *fakeMetrics { return &fakeMetrics{rejected: map[RejectReason]int{}} }

func (m *fakeMetrics) RecordFanout(int, int)                {}
func (m *fakeMetrics) RecordDerivedPublished()              { m.published++ }
func (m *fakeMetrics) RecordDerivedRejected(r RejectReason) { m.rejected[r]++ }

func thresholdRule(id string) *rules.CompiledRule {
	return &rules.CompiledRule{ID: id, Type: rules.TypeThreshold}
}

// TestPublishStampsSeverity proves the derived event carries the rule's severity (the ADR-037
// subscriber field), stamped from the CURRENT registry rule like Kind. A rule with no severity
// omits it (omitempty), keeping the wire lean.
func TestPublishStampsSeverity(t *testing.T) {
	withSev := &rules.CompiledRule{ID: "acme/r1", Type: rules.TypeThreshold, Severity: rules.SeverityMajor}
	reg := NewRuleRegistry([]ScopedRule{{Tenant: "acme", ProfileVersionToken: "p@1", Compiled: withSev}})
	w := &fakeWriter{}
	p := NewPublisher(w, reg, newFakeMetrics())
	if err := p.Publish(context.Background(), core.Detection{RuleID: "acme/r1", Series: "d1", Kind: core.Threshold, At: time.Now()}); err != nil {
		t.Fatalf("publish: %v", err)
	}
	var de DerivedEvent
	if err := json.Unmarshal(w.writes[0].payload, &de); err != nil {
		t.Fatalf("payload: %v", err)
	}
	if de.Severity != "major" {
		t.Fatalf("severity = %q, want major", de.Severity)
	}

	// A rule with no severity omits the field entirely (omitempty), keeping the wire lean.
	noSev := &rules.CompiledRule{ID: "acme/r2", Type: rules.TypeThreshold}
	reg2 := NewRuleRegistry([]ScopedRule{{Tenant: "acme", ProfileVersionToken: "p@1", Compiled: noSev}})
	w2 := &fakeWriter{}
	p2 := NewPublisher(w2, reg2, newFakeMetrics())
	if err := p2.Publish(context.Background(), core.Detection{RuleID: "acme/r2", Series: "d1", Kind: core.Threshold, At: time.Now()}); err != nil {
		t.Fatalf("publish: %v", err)
	}
	if strings.Contains(string(w2.writes[0].payload), "severity") {
		t.Fatalf("payload should omit severity when unset: %s", w2.writes[0].payload)
	}
}

// TestPublishStampsValue proves slice 6a's value carriage: a value-bearing detection stamps its
// scalar as a pointer (so a raiseAlarm action carries the real triggering value), and a value-less
// detection (silence-driven absence/duration) omits it entirely (nil pointer + omitempty). A stamped
// 0.0 is distinct from absent — the whole reason Value is a pointer.
func TestPublishStampsValue(t *testing.T) {
	reg := NewRuleRegistry([]ScopedRule{{Tenant: "acme", ProfileVersionToken: "p@1", Compiled: thresholdRule("acme/r1")}})

	// Value-bearing: HasValue detection -> pointer set to the exact value.
	w := &fakeWriter{}
	p := NewPublisher(w, reg, newFakeMetrics())
	if err := p.Publish(context.Background(), core.Detection{RuleID: "acme/r1", Series: "d1", Kind: core.Threshold, At: time.Now(), Value: 42.5, HasValue: true}); err != nil {
		t.Fatalf("publish: %v", err)
	}
	var de DerivedEvent
	if err := json.Unmarshal(w.writes[0].payload, &de); err != nil {
		t.Fatalf("payload: %v", err)
	}
	if de.Value == nil || *de.Value != 42.5 {
		t.Fatalf("value = %v, want *42.5", de.Value)
	}

	// A stamped 0.0 is still present (pointer non-nil) — distinct from a value-less fire.
	w0 := &fakeWriter{}
	p0 := NewPublisher(w0, reg, newFakeMetrics())
	if err := p0.Publish(context.Background(), core.Detection{RuleID: "acme/r1", Series: "d1", Kind: core.Threshold, At: time.Now(), Value: 0, HasValue: true}); err != nil {
		t.Fatalf("publish: %v", err)
	}
	var de0 DerivedEvent
	if err := json.Unmarshal(w0.writes[0].payload, &de0); err != nil {
		t.Fatalf("payload: %v", err)
	}
	if de0.Value == nil || *de0.Value != 0 {
		t.Fatalf("a stamped zero must be present, not absent; got %v", de0.Value)
	}

	// Value-less: HasValue=false -> omitted from the wire entirely.
	w2 := &fakeWriter{}
	p2 := NewPublisher(w2, reg, newFakeMetrics())
	if err := p2.Publish(context.Background(), core.Detection{RuleID: "acme/r1", Series: "d1", Kind: core.Threshold, At: time.Now()}); err != nil {
		t.Fatalf("publish: %v", err)
	}
	if strings.Contains(string(w2.writes[0].payload), "value") {
		t.Fatalf("payload should omit value when the detection carries none: %s", w2.writes[0].payload)
	}
}

// A well-formed detection publishes one derived event, scoped to the rule's owning tenant,
// carrying the stable dedup identity (rule id, series, kind, event time).
func TestPublishScopesToOwningTenant(t *testing.T) {
	reg := NewRuleRegistry([]ScopedRule{{Tenant: "acme", ProfileVersionToken: "p@1", Compiled: thresholdRule("acme/r1")}})
	w := &fakeWriter{}
	m := newFakeMetrics()
	p := NewPublisher(w, reg, m)

	at := time.Date(2026, 7, 9, 12, 0, 0, 0, time.UTC)
	err := p.Publish(context.Background(), core.Detection{RuleID: "acme/r1", Series: "d1", Kind: core.Threshold, At: at})
	if err != nil {
		t.Fatalf("publish: %v", err)
	}
	if len(w.writes) != 1 {
		t.Fatalf("want 1 write; got %d", len(w.writes))
	}
	if w.writes[0].tenant != "acme" {
		t.Fatalf("derived event must be scoped to the owning tenant; got %q", w.writes[0].tenant)
	}
	var de DerivedEvent
	if err := json.Unmarshal(w.writes[0].payload, &de); err != nil {
		t.Fatalf("payload: %v", err)
	}
	if de.RuleID != "acme/r1" || de.Series != "d1" || de.Kind != "threshold" || !de.OccurredTime.Equal(at) {
		t.Fatalf("derived event identity wrong: %+v", de)
	}
	if m.published != 1 {
		t.Fatalf("published metric not recorded")
	}
}

// THE TENANT BACKSTOP. A detection whose rule-id tenant prefix disagrees with the tenant the
// rule is registered under is refused — no write to any tenant subject, dead-lettered and
// counted. This is the fail-closed guard against a mis-generated rule.
func TestPublishTenantBackstopRejectsMismatch(t *testing.T) {
	// Rule filed under "acme" but its id claims "beta" — a mis-minted rule.
	reg := NewRuleRegistry([]ScopedRule{{Tenant: "acme", ProfileVersionToken: "p@1", Compiled: thresholdRule("beta/r1")}})
	w := &fakeWriter{}
	m := newFakeMetrics()
	p := NewPublisher(w, reg, m)

	err := p.Publish(context.Background(), core.Detection{RuleID: "beta/r1", Series: "d1", Kind: core.Threshold, At: time.Now()})
	if err != nil {
		t.Fatalf("backstop reject must not be a retryable error; got %v", err)
	}
	if len(w.writes) != 0 {
		t.Fatalf("backstop must prevent any publish; got %d writes", len(w.writes))
	}
	if m.rejected[RejectBackstop] != 1 {
		t.Fatalf("backstop rejection not counted: %+v", m.rejected)
	}
	dl := p.DeadLetters()
	if len(dl) != 1 || dl[0].Reason != RejectBackstop || dl[0].OwningTenant != "acme" || dl[0].ClaimedTenant != "beta" {
		t.Fatalf("dead-letter record wrong: %+v", dl)
	}
}

// A rule whose tenant token violates the ADR-042 grammar is a TERMINAL backstop drop, not a
// retryable error — otherwise the writer's deterministic rejection would defer the checkpoint
// forever and stall detection for every tenant on the singleton.
func TestPublishRejectsInvalidTenantGrammarTerminally(t *testing.T) {
	// Internally consistent (id-tenant == owning tenant) but the tenant token has a ".".
	reg := NewRuleRegistry([]ScopedRule{{Tenant: "bad.tenant", ProfileVersionToken: "p@1", Compiled: thresholdRule("bad.tenant/r1")}})
	w := &fakeWriter{}
	m := newFakeMetrics()
	p := NewPublisher(w, reg, m)

	err := p.Publish(context.Background(), core.Detection{RuleID: "bad.tenant/r1", Series: "d1", Kind: core.Threshold, At: time.Now()})
	if err != nil {
		t.Fatalf("an invalid-tenant rule must be a terminal drop, not a retryable error; got %v", err)
	}
	if len(w.writes) != 0 {
		t.Fatalf("must not attempt to publish for an invalid tenant; got %d writes", len(w.writes))
	}
	if m.rejected[RejectBackstop] != 1 {
		t.Fatalf("invalid-tenant rejection should count as a backstop drop: %+v", m.rejected)
	}
}

// A detection for a rule no longer in the registry is dropped (orphan) — a tenant cannot be
// safely attributed to an unknown rule.
func TestPublishOrphanRuleDropped(t *testing.T) {
	reg := NewRuleRegistry(nil)
	w := &fakeWriter{}
	m := newFakeMetrics()
	p := NewPublisher(w, reg, m)

	err := p.Publish(context.Background(), core.Detection{RuleID: "acme/ghost", Series: "d1", Kind: core.Threshold, At: time.Now()})
	if err != nil {
		t.Fatalf("orphan drop must not be a retryable error; got %v", err)
	}
	if len(w.writes) != 0 {
		t.Fatalf("orphan detection must not publish; got %d", len(w.writes))
	}
	if m.rejected[RejectOrphan] != 1 {
		t.Fatalf("orphan rejection not counted: %+v", m.rejected)
	}
}

// A broker failure is retryable: Publish returns an error so the caller defers the
// checkpoint (deliver-before-checkpoint), and nothing is counted as published.
func TestPublishBrokerErrorIsRetryable(t *testing.T) {
	reg := NewRuleRegistry([]ScopedRule{{Tenant: "acme", ProfileVersionToken: "p@1", Compiled: thresholdRule("acme/r1")}})
	w := &fakeWriter{err: errors.New("broker down")}
	m := newFakeMetrics()
	p := NewPublisher(w, reg, m)

	err := p.Publish(context.Background(), core.Detection{RuleID: "acme/r1", Series: "d1", Kind: core.Threshold, At: time.Now()})
	if err == nil {
		t.Fatalf("a broker failure must return a retryable error")
	}
	if m.published != 0 {
		t.Fatalf("a failed publish must not count as published")
	}
}
