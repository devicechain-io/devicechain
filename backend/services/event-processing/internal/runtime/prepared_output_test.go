// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package runtime

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/devicechain-io/dc-event-processing/internal/detect/core"
	"github.com/devicechain-io/dc-event-processing/internal/rules"
	dccore "github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/messaging"
)

// idWriter records each write's tenant, bytes and dedup id.
type idWriter struct {
	writes []idWrite
	err    error
}

type idWrite struct {
	tenant, dedupID string
	payload         []byte
}

func (w *idWriter) WriteMessages(ctx context.Context, msgs ...messaging.Message) error {
	if w.err != nil {
		return w.err
	}
	tenant, _ := dccore.TenantFromContext(ctx)
	for _, m := range msgs {
		w.writes = append(w.writes, idWrite{tenant: tenant, dedupID: m.DedupID, payload: append([]byte(nil), m.Value...)})
	}
	return nil
}

func (w *idWriter) WriteToDevice(ctx context.Context, _ string, msgs ...messaging.Message) error {
	return w.WriteMessages(ctx, msgs...)
}

func (w *idWriter) HandleResponse(error) {}

// goldenDetections are fixed detections whose derived events are pinned byte for byte below.
func goldenDetections() []core.Detection {
	at := time.Date(2026, 10, 17, 12, 0, 0, 123456789, time.UTC)
	return []core.Detection{
		{RuleID: "acme/p@1/hot", Series: "d1", Kind: core.Threshold, At: at, Edge: core.EdgeRaised, Value: 91.5, HasValue: true},
		{RuleID: "acme/p@1/hot", Series: "d1", Kind: core.Threshold, At: at.Add(time.Second), Edge: core.EdgeResolved},
		{RuleID: "acme/p@1/quiet", Series: "d2", Kind: core.Absence, At: at, Edge: core.EdgeRaised},
	}
}

func goldenRegistry() *RuleRegistry {
	return NewRuleRegistry([]ScopedRule{
		{Tenant: "acme", ProfileVersionToken: "p@1", Compiled: &rules.CompiledRule{ID: "acme/p@1/hot", Type: rules.TypeThreshold, Severity: rules.SeverityMajor}},
		{Tenant: "acme", ProfileVersionToken: "p@1", Compiled: &rules.CompiledRule{ID: "acme/p@1/quiet", Type: rules.TypeAbsence}},
	})
}

// The derived event's wire bytes and dedup id, pinned. They are what downstream consumers parse
// and what the broker collapses re-publishes by; the pipelined publisher must not move either.
// These literals were produced by the serial publisher (b14af024) before it was split into
// Prepare and Send.
var goldenWrites = []idWrite{
	{tenant: "acme", dedupID: "de.d6657cc1f05b9f6f62aad19e39ab12ebbd91ff0d6308015ff34b24ec95eb474d",
		payload: []byte(`{"ruleId":"acme/p@1/hot","tenant":"acme","kind":"threshold","edge":"raised","series":"d1","occurredTime":"2026-10-17T12:00:00.123456789Z","severity":"major","value":91.5,"triggeredAt":"2026-10-17T12:00:05Z"}`)},
	{tenant: "acme", dedupID: "de.027ce113cd41c11991aa4747404bcadcb2037845858f1ad2f35c6733c923a40a",
		payload: []byte(`{"ruleId":"acme/p@1/hot","tenant":"acme","kind":"threshold","edge":"resolved","series":"d1","occurredTime":"2026-10-17T12:00:01.123456789Z","severity":"major","triggeredAt":"2026-10-17T12:00:05Z"}`)},
	{tenant: "acme", dedupID: "de.32b178c8de7f6b8354756d4f713a4b67d5328ec3db3c9ed769c92567bce4c342",
		payload: []byte(`{"ruleId":"acme/p@1/quiet","tenant":"acme","kind":"absence","edge":"raised","series":"d2","occurredTime":"2026-10-17T12:00:00.123456789Z","triggeredAt":"2026-10-17T12:00:05Z"}`)},
}

var goldenTriggeredAt = time.Date(2026, 10, 17, 12, 0, 5, 0, time.UTC)

// TestPublishWritesTheGoldenBytes pins the serial path's output.
func TestPublishWritesTheGoldenBytes(t *testing.T) {
	w := &idWriter{}
	p := NewPublisher(w, goldenRegistry(), newFakeMetrics())
	for _, d := range goldenDetections() {
		if err := p.Publish(context.Background(), d, goldenTriggeredAt); err != nil {
			t.Fatal(err)
		}
	}
	if len(w.writes) != len(goldenWrites) {
		t.Fatalf("%d writes, want %d", len(w.writes), len(goldenWrites))
	}
	for i, got := range w.writes {
		want := goldenWrites[i]
		if got.tenant != want.tenant || !bytes.Equal(got.payload, want.payload) {
			t.Errorf("write %d:\n got %s %s\nwant %s %s", i, got.tenant, got.payload, want.tenant, want.payload)
		}
		if got.dedupID != want.dedupID {
			t.Errorf("write %d: dedup id %s, want %s", i, got.dedupID, want.dedupID)
		}
	}
}

// Prepare builds exactly what Publish writes: the same bytes, the same dedup id, for the same
// tenant. And Send writes exactly what Prepare built.
func TestPrepareIsWhatPublishWrites(t *testing.T) {
	serial := &idWriter{}
	ps := NewPublisher(serial, goldenRegistry(), newFakeMetrics())
	split := &idWriter{}
	pp := NewPublisher(split, goldenRegistry(), newFakeMetrics())
	for _, d := range goldenDetections() {
		if err := ps.Publish(context.Background(), d, goldenTriggeredAt); err != nil {
			t.Fatal(err)
		}
		po, ok := pp.Prepare(d, goldenTriggeredAt)
		if !ok {
			t.Fatalf("%+v: not prepared", d)
		}
		if _, err := pp.Send(context.Background(), po); err != nil {
			t.Fatal(err)
		}
		want := serial.writes[len(serial.writes)-1]
		if !bytes.Equal(po.Payload, want.payload) || po.DedupID != want.dedupID || po.Tenant != want.tenant {
			t.Fatalf("prepared %s %s %s, Publish wrote %s %s %s", po.Tenant, po.DedupID, po.Payload, want.tenant, want.dedupID, want.payload)
		}
		got := split.writes[len(split.writes)-1]
		if !bytes.Equal(got.payload, po.Payload) || got.dedupID != po.DedupID || got.tenant != po.Tenant {
			t.Fatalf("Send wrote %+v, prepared %+v", got, po)
		}
		if po.RuleID != d.RuleID || po.Series != d.Series || !po.At.Equal(d.At) || po.Edge != wireEdge(d.Edge) {
			t.Fatalf("prepared identity %+v does not match the detection %+v", po, d)
		}
	}
}

// Prepare refuses exactly what Publish drops, counted the same way, and writes nothing.
func TestPrepareRefusesWhatPublishDrops(t *testing.T) {
	reg := NewRuleRegistry([]ScopedRule{
		{Tenant: "beta", ProfileVersionToken: "p@1", Compiled: thresholdRule("acme/p@1/misfiled")},
		{Tenant: "bad.tenant", ProfileVersionToken: "p@1", Compiled: thresholdRule("bad.tenant/p@1/r")},
	})
	for _, tc := range []struct {
		rule   string
		reason RejectReason
	}{
		{"acme/p@1/gone", RejectOrphan},
		{"acme/p@1/misfiled", RejectBackstop},
		{"bad.tenant/p@1/r", RejectBackstop},
	} {
		d := core.Detection{RuleID: tc.rule, Series: "d", Kind: core.Threshold, At: time.Now()}
		ms, mp := newFakeMetrics(), newFakeMetrics()
		ws := &idWriter{}
		if err := NewPublisher(ws, reg, ms).Publish(context.Background(), d, time.Time{}); err != nil {
			t.Fatalf("%s: Publish returned %v for a terminal drop", tc.rule, err)
		}
		if _, ok := NewPublisher(&idWriter{}, reg, mp).Prepare(d, time.Time{}); ok {
			t.Fatalf("%s: prepared a detection Publish drops", tc.rule)
		}
		if len(ws.writes) != 0 || ms.rejected[tc.reason] != 1 || mp.rejected[tc.reason] != 1 {
			t.Fatalf("%s: Publish wrote %d and counted %v; Prepare counted %v", tc.rule, len(ws.writes), ms.rejected, mp.rejected)
		}
	}
}

// Recheck refuses a prepared output whose rule has gone or whose owning tenant changed, and
// leaves a still-eligible one alone; Send's failure is the writer's, wrapped, never recorded.
func TestRecheckAndSend(t *testing.T) {
	reg := goldenRegistry()
	m := newFakeMetrics()
	w := &idWriter{}
	p := NewPublisher(w, reg, m)
	d := goldenDetections()[0]
	po, ok := p.Prepare(d, goldenTriggeredAt)
	if !ok || !p.Recheck(d, po) {
		t.Fatal("an eligible prepared output was refused")
	}
	stolen := po
	stolen.Tenant = "other"
	if p.Recheck(d, stolen) || m.rejected[RejectBackstop] != 1 {
		t.Fatalf("a prepared output for a tenant that no longer owns its rule passed: %v", m.rejected)
	}
	reg.Remove(d.RuleID)
	if p.Recheck(d, po) || m.rejected[RejectOrphan] != 1 {
		t.Fatalf("a prepared output whose rule is gone passed: %v", m.rejected)
	}
	w.err = errors.New("broker down")
	if _, err := p.Send(context.Background(), po); !errors.Is(err, w.err) {
		t.Fatalf("Send returned %v", err)
	}
	if m.published != 0 || len(m.ackWaits) != 0 {
		t.Fatal("Send recorded an outcome; recording is the loop's")
	}
}
