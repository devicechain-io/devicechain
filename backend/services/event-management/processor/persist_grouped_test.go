// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"fmt"
	"reflect"
	"testing"
	"time"

	dmodel "github.com/devicechain-io/dc-device-management/model"
	dmproto "github.com/devicechain-io/dc-device-management/proto"
	"github.com/devicechain-io/dc-event-management/model"
	esmodel "github.com/devicechain-io/dc-event-sources/model"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/stretchr/testify/assert"
	"gorm.io/gorm"
)

// The grouped batch write, by VALUE: how many statements a batch costs, and that what it
// stores is what writing each message on its own stores. Every instant here is a whole
// microsecond: the event store keeps microseconds, and sqlite, which keeps more, would
// otherwise see two keys where PostgreSQL sees one.

// anchorsOf is n area anchors named from tag.
func anchorsOf(tag string, n int) []dmodel.ResolvedAnchor {
	var out []dmodel.ResolvedAnchor
	for i := 0; i < n; i++ {
		out = append(out, dmodel.ResolvedAnchor{AnchorType: "area", AnchorToken: fmt.Sprintf("%s-%d", tag, i)})
	}
	return out
}

func altOf(alt string) *string {
	if alt == "" {
		return nil
	}
	return &alt
}

// measurementOf is a measurement from dev at at, one entry per value with each entry's own
// instant a millisecond apart, under alt ("" for none) with n anchors.
func measurementOf(dev string, at time.Time, alt string, anchors int, values ...string) dmodel.ResolvedEvent {
	payload := &dmodel.ResolvedMeasurementsPayload{}
	for i, v := range values {
		payload.Entries = append(payload.Entries, dmodel.ResolvedMeasurementsEntry{
			OccurredTime: at.Add(time.Duration(i) * time.Millisecond),
			Entries:      []dmodel.ResolvedMeasurementEntry{{Name: "temperature", Value: v}},
		})
	}
	return dmodel.ResolvedEvent{Source: "mqtt1", AltId: altOf(alt), SourceDeviceToken: dev,
		EventType: esmodel.Measurement, OccurredTime: at, ProcessedTime: at, Payload: payload,
		Anchors: anchorsOf(dev, anchors)}
}

// threeReadings is a measurement entry of three readings at one instant, as batchEvent's.
func threeReadings(dev string, at time.Time, alt string, anchors int) dmodel.ResolvedEvent {
	ev := fenceCostMeasurement("", at, 0)
	ev.AltId = altOf(alt)
	ev.SourceDeviceToken = dev
	ev.Anchors = anchorsOf(dev, anchors)
	return ev
}

func locationOf(dev string, at time.Time, alt string, anchors int, lat string) dmodel.ResolvedEvent {
	lon := "-71.5"
	return dmodel.ResolvedEvent{Source: "mqtt1", AltId: altOf(alt), SourceDeviceToken: dev,
		EventType: esmodel.Location, OccurredTime: at, ProcessedTime: at,
		Payload: &dmodel.ResolvedLocationsPayload{Entries: []dmodel.ResolvedLocationEntry{
			{Latitude: &lat, Longitude: &lon, OccurredTime: at},
		}},
		Anchors: anchorsOf(dev, anchors)}
}

func alertOf(dev string, at time.Time, alt string, anchors int, message string) dmodel.ResolvedEvent {
	return dmodel.ResolvedEvent{Source: "mqtt1", AltId: altOf(alt), SourceDeviceToken: dev,
		EventType: esmodel.Alert, OccurredTime: at, ProcessedTime: at,
		Payload: &dmodel.ResolvedAlertsPayload{Entries: []dmodel.ResolvedAlertEntry{
			{Type: "overheat", Level: 2, Message: message, Source: "device", OccurredTime: at},
		}},
		Anchors: anchorsOf(dev, anchors)}
}

func stateChangeOf(dev string, at time.Time, alt string, state string, session uint64, anchors ...string) dmodel.ResolvedEvent {
	ev := dmodel.ResolvedEvent{Source: "mqtt1", AltId: altOf(alt), SourceDeviceToken: dev,
		EventType: esmodel.StateChange, OccurredTime: at, ProcessedTime: at,
		Payload: &dmodel.ResolvedStateChangePayload{State: state, Reason: "test", SessionId: session}}
	for _, a := range anchors {
		ev.Anchors = append(ev.Anchors, dmodel.ResolvedAnchor{AnchorType: "area", AnchorToken: a})
	}
	return ev
}

// sent is one message of a test batch: its tenant and its event.
type sent struct {
	tenant string
	ev     dmodel.ResolvedEvent
}

func (r *batchRig) consume(t *testing.T, batch []sent) []messaging.Message {
	t.Helper()
	out := make([]messaging.Message, 0, len(batch))
	for i, s := range batch {
		out = append(out, consumed(t, r.acks, i, s.tenant, 1, s.ev))
	}
	return out
}

// count counts a table's rows under a system context, optionally filtered.
func count(t *testing.T, db *gorm.DB, m any, where string, args ...any) int64 {
	t.Helper()
	q := sysDB(db).Model(m)
	if where != "" {
		q = q.Where(where, args...)
	}
	var n int64
	if err := q.Count(&n).Error; err != nil {
		t.Fatalf("count: %v", err)
	}
	return n
}

// The value this change exists for: a batch writes each table ONCE per tenant, not once per
// message. Each row is one run of the worker; `all` counts every statement the run made,
// `fence` the erasure-fence reads among them. Written one message at a time (main) the rows
// cost 257, 193, 258, 25 and 44 statements. The last row is the negative control: a batch
// of one is the per-message path, which the change leaves alone, so its count is the same
// before and after — the counter measures the path, not an artefact.
func TestABatchWritesEachTableOncePerTenant(t *testing.T) {
	mixed := func() []sent {
		var out []sent
		for i := 0; i < 3; i++ {
			out = append(out, sent{fenceCostTenant, threeReadings(fmt.Sprintf("dev-m%d", i), batchT0.Add(time.Duration(i)*time.Millisecond), "", 1)})
		}
		for i := 0; i < 3; i++ {
			out = append(out, sent{fenceCostTenant, locationOf(fmt.Sprintf("dev-l%d", i), batchT0.Add(time.Duration(i)*time.Millisecond), "", 1, "42.5")})
		}
		for i := 0; i < 2; i++ {
			out = append(out, sent{fenceCostTenant, alertOf(fmt.Sprintf("dev-a%d", i), batchT0.Add(time.Duration(i)*time.Millisecond), "", 1, "hot")})
		}
		return out
	}
	uniform := func(n int, alt bool, tenants ...string) []sent {
		var out []sent
		for i := 0; i < n; i++ {
			out = append(out, sent{tenants[i%len(tenants)], batchEvent(i, alt, batchT0)})
		}
		return out
	}
	for _, tc := range []struct {
		name                string
		maxBatch            int
		batch               []sent
		poison              int // index of a message whose value is not a number, or -1
		wantAll, wantFence  int64
		wantE, wantM, wantA int64
	}{
		// probe, events, measurement_events, event_anchors; and the fence read.
		{"one tenant, alternate ids", 64, uniform(64, true, fenceCostTenant), -1, 5, 1, 64, 192, 64},
		{"one tenant, no alternate ids", 64, uniform(64, false, fenceCostTenant), -1, 4, 1, 64, 192, 64},
		{"two tenants interleaved", 64, uniform(64, true, fenceCostTenant, "globex"), -1, 10, 2, 64, 192, 64},
		// events, location_events, measurement_events, alert_events, event_anchors, fence.
		{"one tenant, three kinds", 8, mixed(), -1, 6, 1, 8, 9, 8},
		// The batch is refused while its rows are built, before any SQL (0); dev-3 alone
		// probes and is refused building (1); the other seven are written grouped (5).
		{"a refused message", 8, uniform(8, true, fenceCostTenant), 3, 6, 1, 7, 21, 7},
		{"control: a batch of one", 1, uniform(4, true, fenceCostTenant), -1, 20, 4, 4, 12, 4},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newBatchRig(t, tc.maxBatch)
			msgs := r.consume(t, tc.batch)
			if tc.poison >= 0 {
				msgs[tc.poison] = poisoned(t, r.acks, tc.poison, fenceCostTenant)
			}
			r.counter.Reset()
			r.run(msgs)
			all, fence := r.counter.Counts()
			if all != tc.wantAll || fence != tc.wantFence {
				t.Errorf("the batch made %d statements with %d fence reads; want %d with %d",
					all, fence, tc.wantAll, tc.wantFence)
			}
			if e, m, a := rowCounts(t, r.db); e != tc.wantE || m != tc.wantM || a != tc.wantA {
				t.Errorf("stored %d/%d/%d event/measurement/anchor rows; want %d/%d/%d",
					e, m, a, tc.wantE, tc.wantM, tc.wantA)
			}
			if tc.name == "one tenant, three kinds" {
				if l, al := count(t, r.db, &model.LocationEvent{}, ""), count(t, r.db, &model.AlertEvent{}, ""); l != 3 || al != 2 {
					t.Errorf("stored %d location and %d alert rows; want 3 and 2", l, al)
				}
			}
			if got := len(r.acks.acked()); got != len(msgs) {
				t.Errorf("%d messages acknowledged; want %d", got, len(msgs))
			}
		})
	}
}

// An event whose alternate id and instant are already stored is skipped inside a batch, even
// when its content — and so its event id — differs: the events insert's arbiter would not
// absorb it, and the alternate-id index would refuse it, failing the batch. A message with
// the same alternate id at another instant is a different key and is stored.
func TestAnAlternateIdAlreadyStoredIsSkippedInABatch(t *testing.T) {
	r := newBatchRig(t, 8)
	at := batchT0.Add(time.Hour)
	r.run([]messaging.Message{consumed(t, r.acks, 100, fenceCostTenant, 1, measurementOf("dev-x", at, "x", 1, "21.5"))})
	r.api.txs.Store(0)

	var batch []sent
	for i := 0; i < 8; i++ {
		ev := batchEvent(i, true, batchT0)
		switch i {
		case 2:
			ev = measurementOf("dev-x", at, "x", 1, "99") // the stored key, other content
		case 5:
			ev = measurementOf("dev-y", at.Add(time.Millisecond), "x", 1, "7") // another instant
		}
		batch = append(batch, sent{fenceCostTenant, ev})
	}
	r.run(r.consume(t, batch))

	if got, fb := r.api.txs.Load(), r.metric(t, metricFallbacks, "", ""); got != 1 || fb != 0 {
		t.Errorf("%d transactions, %v fallbacks; want 1, 0", got, fb)
	}
	if got := count(t, r.db, &model.Event{}, "alt_id = ? AND occurred_time = ?", "x", at); got != 1 {
		t.Errorf("%d events stored for alternate id x at its instant; want 1", got)
	}
	var stored model.MeasurementEvent
	if err := sysDB(r.db).Where("device_token = ?", "dev-x").Take(&stored).Error; err != nil {
		t.Fatalf("read the stored measurement: %v", err)
	}
	if stored.Value.Float64 != 21.5 {
		t.Errorf("the stored value is %v; want 21.5, the first delivery's", stored.Value.Float64)
	}
	if got := count(t, r.db, &model.Event{}, "device_token = ?", "dev-y"); got != 1 {
		t.Errorf("the same alternate id at another instant stored %d events; want 1", got)
	}
	if got := count(t, r.db, &model.Event{}, ""); got != 8 {
		t.Errorf("%d events in all; want 8 (the first delivery and seven of the batch)", got)
	}
	if got := len(r.acks.acked()); got != 9 {
		t.Errorf("%d messages acknowledged; want 9", got)
	}
}

// Two messages of one batch with one alternate id and instant: the first is stored and the
// second skipped, as writing them one at a time does (the second's probe sees the first's
// row in the same transaction). An earlier message that writes NO events row — a payload
// with no readings — hides nothing: written one at a time, the later message's probe finds
// no row and stores it.
func TestTwoEventsWithOneAlternateIdInABatchStoreTheFirst(t *testing.T) {
	at := batchT0.Add(time.Hour)
	for _, tc := range []struct {
		name  string
		first dmodel.ResolvedEvent
		// wantValue is the stored reading for the key; wantEvents the events rows in all.
		wantValue  float64
		wantEvents int64
	}{
		{"both write an event", measurementOf("dev-y", at, "y", 1, "21.5"), 21.5, 3},
		{"the first has no readings", measurementOf("dev-y", at, "y", 1), 99, 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newBatchRig(t, 8)
			r.run(r.consume(t, []sent{
				{fenceCostTenant, batchEvent(0, true, batchT0)},
				{fenceCostTenant, tc.first},
				{fenceCostTenant, batchEvent(2, true, batchT0)},
				{fenceCostTenant, measurementOf("dev-y", at, "y", 1, "99")},
			}))
			if got, fb := r.api.txs.Load(), r.metric(t, metricFallbacks, "", ""); got != 1 || fb != 0 {
				t.Errorf("%d transactions, %v fallbacks; want 1, 0", got, fb)
			}
			if got := len(r.acks.acked()); got != 4 {
				t.Errorf("%d messages acknowledged; want 4", got)
			}
			if got := count(t, r.db, &model.Event{}, ""); got != tc.wantEvents {
				t.Errorf("%d events stored; want %d", got, tc.wantEvents)
			}
			var rows []model.MeasurementEvent
			if err := sysDB(r.db).Where("event_type = ? AND device_token = ?", esmodel.Measurement,
				"dev-y").Find(&rows).Error; err != nil {
				t.Fatalf("read the key's readings: %v", err)
			}
			if len(rows) != 1 || rows[0].Value.Float64 != tc.wantValue {
				t.Errorf("the key's readings are %+v; want one, of value %v", rows, tc.wantValue)
			}
		})
	}
}

// An alternate id is the SENDER's message id, so it dedups per device: two devices of one
// tenant that send the same alternate id at the same instant are two messages, not a
// redelivery, and both are stored. The same device sending it again is the redelivery and is
// skipped. Both shapes run in one batch, so the in-batch rule and the stored-key probe are
// each exercised.
func TestTwoDevicesWithOneAlternateIdAtOneInstantBothStore(t *testing.T) {
	at := batchT0.Add(time.Hour)
	r := newBatchRig(t, 8)
	r.run(r.consume(t, []sent{
		{fenceCostTenant, measurementOf("dev-a", at, "shared", 1, "1.5")},
		{fenceCostTenant, measurementOf("dev-b", at, "shared", 1, "2.5")},
		{fenceCostTenant, measurementOf("dev-a", at, "shared", 1, "9.9")}, // dev-a again: the redelivery
	}))
	// A second batch: both devices again, now against the stored keys.
	r.run(r.consume(t, []sent{
		{fenceCostTenant, measurementOf("dev-a", at, "shared", 1, "8.8")},
		{fenceCostTenant, measurementOf("dev-b", at, "shared", 1, "7.7")},
	}))

	for dev, want := range map[string]float64{"dev-a": 1.5, "dev-b": 2.5} {
		var rows []model.MeasurementEvent
		if err := sysDB(r.db).Where("device_token = ?", dev).Find(&rows).Error; err != nil {
			t.Fatalf("read %s: %v", dev, err)
		}
		if len(rows) != 1 || rows[0].Value.Float64 != want {
			t.Errorf("%s stored %+v; want exactly one reading of %v", dev, rows, want)
		}
	}
	if got := count(t, r.db, &model.Event{}, "alt_id = ?", "shared"); got != 2 {
		t.Errorf("%d events stored for the shared alternate id; want 2, one per device", got)
	}
}

// A connect or disconnect event redelivered with a different anchor set — its device was
// reassigned between deliveries — keeps the anchors it was stored with: its own insert is
// absorbed by the state-change index, and that is what skips its anchors.
func TestAStateChangeRedeliveredInABatchKeepsItsAnchors(t *testing.T) {
	r := newBatchRig(t, 8)
	at := batchT0.Add(time.Hour)
	r.run([]messaging.Message{consumed(t, r.acks, 100, fenceCostTenant, 1,
		stateChangeOf("dev-s", at, "", "CONNECTED", 7, "area-a"))})
	r.api.txs.Store(0)

	r.run(r.consume(t, []sent{
		{fenceCostTenant, stateChangeOf("dev-s", at, "", "CONNECTED", 7, "area-b")},
		{fenceCostTenant, batchEvent(1, true, batchT0)},
		{fenceCostTenant, batchEvent(2, false, batchT0)},
	}))
	if got := r.api.txs.Load(); got != 1 {
		t.Errorf("%d transactions; want 1", got)
	}
	var anchors []model.EventAnchor
	if err := sysDB(r.db).Where("device_token = ?", "dev-s").Find(&anchors).Error; err != nil {
		t.Fatalf("read the state change's anchors: %v", err)
	}
	if len(anchors) != 1 || anchors[0].AnchorToken != "area-a" {
		t.Errorf("the state change has anchors %+v; want exactly area-a", anchors)
	}
	for _, dev := range []string{"dev-1", "dev-2"} {
		if e, m := eventsFor(t, r.db, dev); e != 1 || m != 3 {
			t.Errorf("%s has %d/%d event/measurement rows; want 1/3", dev, e, m)
		}
	}
}

// A connect event that carries an alternate id comes before a reading with the same key:
// written one at a time the connect event is stored and the reading skipped. Grouping would
// write the reading first, so such a batch is written one message at a time.
func TestAStateChangeWithAnAlternateIdKeepsItsPlaceInTheBatch(t *testing.T) {
	r := newBatchRig(t, 8)
	at := batchT0.Add(time.Hour)
	r.run(r.consume(t, []sent{
		{fenceCostTenant, stateChangeOf("dev-s", at, "k", "CONNECTED", 7)},
		{fenceCostTenant, measurementOf("dev-s", at, "k", 0, "21.5")},
	}))
	if got, fb := r.api.txs.Load(), r.metric(t, metricFallbacks, "", ""); got != 1 || fb != 0 {
		t.Errorf("%d transactions, %v fallbacks; want 1, 0", got, fb)
	}
	var stored []model.Event
	if err := sysDB(r.db).Where("alt_id = ?", "k").Find(&stored).Error; err != nil {
		t.Fatalf("read the key's events: %v", err)
	}
	if len(stored) != 1 || stored[0].EventType != esmodel.StateChange {
		t.Errorf("the key's events are %+v; want the state change alone", stored)
	}
	if got := count(t, r.db, &model.MeasurementEvent{}, ""); got != 0 {
		t.Errorf("%d readings stored; want none", got)
	}
}

// A refused statement that carried one message's rows names that message, so its batch
// costs no second pass: tenant B's one message is refused at its anchor insert and set
// aside at once — the batch, dev-3 alone, the other seven — 3 transactions. The contrast
// row puts dev-3 in tenant A with the seven: the refused anchor insert carried eight
// messages, so the batch is first written again one message at a time to find dev-3 — 4
// transactions, 2 fallbacks — and the seven after it are written grouped again, in 5
// statements, not one message at a time (7 x 4 + 1 = 29).
func TestARefusalInAOneEventGroupNeedsNoSecondPass(t *testing.T) {
	for _, tc := range []struct {
		name          string
		dev3Tenant    string
		wantTxs       int64
		wantFallbacks float64
	}{
		{"dev-3 alone in its tenant", "globex", 3, 1},
		{"contrast: dev-3 among seven of its tenant", fenceCostTenant, 4, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newBatchRig(t, 8)
			r.api.failAnchorsFor = "dev-3"
			r.api.beforeTx = func() { r.counter.Reset() }
			var batch []sent
			for i := 0; i < 8; i++ {
				tenant := fenceCostTenant
				if i == 3 {
					tenant = tc.dev3Tenant
				}
				batch = append(batch, sent{tenant, batchEvent(i, true, batchT0)})
			}
			r.run(r.consume(t, batch))
			lastAll, _ := r.counter.Counts()

			if got := r.api.txs.Load(); got != tc.wantTxs {
				t.Errorf("took %d transactions; want %d", got, tc.wantTxs)
			}
			if got := r.metric(t, metricFallbacks, "", ""); got != tc.wantFallbacks {
				t.Errorf("fallbacks = %v; want %v", got, tc.wantFallbacks)
			}
			if lastAll != 5 {
				t.Errorf("the seven were written again with %d statements; want 5 (grouped)", lastAll)
			}
			if got := r.reported(); len(got) != 1 || got[0].device != "dev-3" ||
				got[0].reason != uint(dmproto.FailureReason_Invalid) {
				t.Errorf("reported %+v; want dev-3 as invalid", got)
			}
			if e, m := eventsFor(t, r.db, "dev-3"); e != 0 || m != 0 {
				t.Errorf("the refused message left %d/%d event/measurement rows; want none", e, m)
			}
			if e, _, _ := rowCounts(t, r.db); e != 7 {
				t.Errorf("stored %d events; want 7", e)
			}
			if got := len(r.acks.acked()); got != 8 {
				t.Errorf("%d messages acknowledged; want 8", got)
			}
		})
	}
}

// The anchor statement names only the messages that hold anchors, not every message that
// wrote rows. In "one anchor holder", eight messages of one tenant write rows but only dev-3
// carries anchors, so its refused anchor insert carried one message's anchors and names
// dev-3 at once: the batch, dev-3 alone, the other seven — 3 transactions, 1 fallback.
// Blaming the eight row writers instead would cost a pass one message at a time to find it
// (4, 2). In "one row writer", dev-0 is the only message with rows and holds no anchors,
// while dev-3 and dev-5 send empty payloads with anchors: the anchor insert carried two
// messages, so the batch is written one message at a time to find dev-3 — 4 transactions,
// 2 fallbacks. Blaming the one row writer would set dev-0 aside first, wrongly, and take
// two more transactions (6, 3).
func TestTheAnchorStatementBlamesOnlyTheMessagesHoldingAnchors(t *testing.T) {
	at := func(i int) time.Time { return batchT0.Add(time.Duration(i) * time.Millisecond) }
	oneHolder := func() []sent {
		var batch []sent
		for i := 0; i < 8; i++ {
			ev := batchEvent(i, true, batchT0)
			if i != 3 {
				ev.Anchors = nil
			}
			batch = append(batch, sent{fenceCostTenant, ev})
		}
		return batch
	}
	oneRowWriter := func() []sent {
		return []sent{
			{fenceCostTenant, measurementOf("dev-0", at(0), "", 0, "1.5")},
			{fenceCostTenant, measurementOf("dev-3", at(3), "", 2)},
			{fenceCostTenant, measurementOf("dev-5", at(5), "", 2)},
		}
	}
	for _, tc := range []struct {
		name          string
		batch         func() []sent
		wantTxs       int64
		wantFallbacks float64
		wantEvents    int64
	}{
		{"one anchor holder", oneHolder, 3, 1, 7},
		{"one row writer", oneRowWriter, 4, 2, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			batch := tc.batch()
			r := newBatchRig(t, len(batch))
			r.api.failAnchorsFor = "dev-3"
			r.run(r.consume(t, batch))

			if got := r.api.txs.Load(); got != tc.wantTxs {
				t.Errorf("took %d transactions; want %d", got, tc.wantTxs)
			}
			if got := r.metric(t, metricFallbacks, "", ""); got != tc.wantFallbacks {
				t.Errorf("fallbacks = %v; want %v", got, tc.wantFallbacks)
			}
			if got := r.reported(); len(got) != 1 || got[0].device != "dev-3" ||
				got[0].reason != uint(dmproto.FailureReason_Invalid) {
				t.Errorf("reported %+v; want dev-3 as invalid", got)
			}
			if e, _, _ := rowCounts(t, r.db); e != tc.wantEvents {
				t.Errorf("stored %d events; want %d", e, tc.wantEvents)
			}
			if got := len(r.acks.acked()); got != len(batch) {
				t.Errorf("%d messages acknowledged; want %d", got, len(batch))
			}
		})
	}
}

// oracleBatch is one deterministic batch over three tenants that holds every case the
// grouped write treats specially, and seed what is stored before it.
func oracleBatch() (seed, batch []sent) {
	at := func(ms int) time.Time { return batchT0.Add(time.Duration(ms) * time.Millisecond) }
	tenants := []string{"t-a", "t-b", "t-c"}
	seed = []sent{
		{"t-a", measurementOf("seed-1", at(500), "pre", 1, "1.5")},
		{"t-b", stateChangeOf("seed-sc", at(501), "", "CONNECTED", 3, "area-old")},
	}
	for i := 0; i < 24; i++ {
		tn := tenants[i%3]
		dev := fmt.Sprintf("o-%d", i)
		alt := ""
		if i%2 == 0 {
			alt = fmt.Sprintf("alt-%d", i)
		}
		var ev dmodel.ResolvedEvent
		switch i % 4 {
		case 0:
			ev = measurementOf(dev, at(i), alt, i%4, "1.25", "2.5", "3.75")
		case 1:
			ev = locationOf(dev, at(i), alt, 1+i%3, fmt.Sprintf("%d.5", 40+i))
		case 2:
			ev = alertOf(dev, at(i), alt, 2, fmt.Sprintf("alert %d", i))
		case 3:
			ev = stateChangeOf(dev, at(i), "", "CONNECTED", uint64(i), "area-sc")
		}
		batch = append(batch, sent{tn, ev})
	}
	batch = append(batch,
		// An empty payload with anchors, then a full one with its key: both stored.
		sent{"t-a", measurementOf("empty-1", at(100), "e", 2)},
		sent{"t-b", threeReadings("filler", at(101), "", 1)},
		sent{"t-a", measurementOf("full-1", at(100), "e", 1, "8.5")},
		// The same message twice, with and without an alternate id.
		sent{"t-c", measurementOf("dup-1", at(110), "d", 1, "4.5")},
		sent{"t-c", measurementOf("dup-1", at(110), "d", 1, "4.5")},
		sent{"t-b", locationOf("dup-2", at(111), "", 2, "10.5")},
		sent{"t-b", locationOf("dup-2", at(111), "", 2, "10.5")},
		// One alternate id and instant, two contents: the first is stored.
		sent{"t-a", alertOf("same-1", at(120), "s", 1, "first")},
		sent{"t-a", alertOf("same-1", at(120), "s", 1, "second")},
		// The same alternate id under another tenant is another key.
		sent{"t-b", alertOf("same-3", at(120), "s", 1, "other tenant")},
		// A key already stored.
		sent{"t-a", measurementOf("seed-1", at(500), "pre", 3, "77")},
		// A state change redelivered with a new anchor set.
		sent{"t-b", stateChangeOf("seed-sc", at(501), "", "CONNECTED", 3, "area-new")},
		// A measurement whose readings span instants, with no alternate id, in each tenant.
		sent{"t-a", measurementOf("span-a", at(130), "", 3, "1", "2", "3", "4")},
		sent{"t-b", measurementOf("span-b", at(131), "", 0, "5", "6")},
		sent{"t-c", measurementOf("span-c", at(132), "", 1, "7")},
	)
	return seed, batch
}

// tableDump is every row of the six event tables, in an order fixed by their key columns.
type tableDump struct {
	Events       []model.Event
	Measurements []model.MeasurementEvent
	Locations    []model.LocationEvent
	Alerts       []model.AlertEvent
	StateChanges []model.StateChangeEvent
	Anchors      []model.EventAnchor
}

func dumpTables(t *testing.T, db *gorm.DB) tableDump {
	t.Helper()
	var d tableDump
	for _, q := range []struct {
		dest  any
		order string
	}{
		{&d.Events, "tenant_id, event_id, occurred_time"},
		{&d.Measurements, "tenant_id, payload_id, occurred_time"},
		{&d.Locations, "tenant_id, payload_id, occurred_time"},
		{&d.Alerts, "tenant_id, payload_id, occurred_time"},
		{&d.StateChanges, "tenant_id, device_token, occurred_time, state, session_id"},
		{&d.Anchors, "tenant_id, event_id, occurred_time, anchor_type, anchor_token"},
	} {
		if err := sysDB(db).Order(q.order).Find(q.dest).Error; err != nil {
			t.Fatalf("dump: %v", err)
		}
	}
	return d
}

// The differential oracle: one batch holding every case the grouped write handles
// specially, stored once grouped (a batch of 64) and once one message at a time (a batch of
// 1), over two databases seeded alike. Every column of every row of the six tables must
// agree. The sanity control is the statement count: the grouped run must make FEWER
// statements than the per-message run, or both ran the same path and the equality says
// nothing. It must also commit in ONE transaction with no fallback: a grouped write that
// gave up and wrote the batch one message at a time would agree for the same reason.
func TestAGroupedBatchStoresWhatOneEventAtATimeStores(t *testing.T) {
	seed, batch := oracleBatch()
	// Each rig in a subtest of its own: the fixture names its database after the test, and
	// closes it when the subtest ends, so everything is read inside.
	run := func(maxBatch int) (res oracleResult) {
		t.Run(fmt.Sprintf("batch of %d", maxBatch), func(t *testing.T) {
			res = runOracle(t, maxBatch, seed, batch)
		})
		if !res.ran {
			t.FailNow()
		}
		return res
	}
	grouped, oneByOne := run(64), run(1)

	if grouped.txs != 1 || grouped.fallbacks != 0 {
		t.Fatalf("the grouped run took %d transactions with %v fallbacks; want 1 and 0",
			grouped.txs, grouped.fallbacks)
	}
	if grouped.statements >= oneByOne.statements {
		t.Fatalf("the grouped run made %d statements and the per-message run %d; want fewer grouped",
			grouped.statements, oneByOne.statements)
	}
	assert.Equal(t, oneByOne.dump, grouped.dump, "the grouped batch stored something other than the per-message path")

	// And what the cases above are for, checked on the grouped run directly.
	d := grouped.dump
	for _, c := range []struct {
		what      string
		got, want int
	}{
		{"readings of the full event after an empty one with its key", countRows(d.Measurements, "full-1"), 1},
		{"anchors of the empty event", countRows(d.Anchors, "empty-1"), 2},
		{"alerts of one device's key, first and second content", countRows(d.Alerts, "same-1"), 1},
		{"alerts of the key under another tenant", countRows(d.Alerts, "same-3"), 1},
		{"events of a key already stored", countRows(d.Events, "seed-1"), 1},
		{"anchors of the redelivered state change", countRows(d.Anchors, "seed-sc"), 1},
		{"events of the message sent twice with an alternate id", countRows(d.Events, "dup-1"), 1},
		{"locations of the message sent twice without one", countRows(d.Locations, "dup-2"), 1},
	} {
		if c.got != c.want {
			t.Errorf("%s: %d; want %d", c.what, c.got, c.want)
		}
	}
}

// countRows counts the rows of one device in a dumped table.
func countRows[T any](rows []T, dev string) int {
	n := 0
	for i := range rows {
		if reflect.ValueOf(rows[i]).FieldByName("DeviceToken").String() == dev {
			n++
		}
	}
	return n
}

// oracleResult is one oracle run: what it stored, what the batch cost, and whether it ran.
type oracleResult struct {
	dump       tableDump
	statements int64
	txs        int64
	fallbacks  float64
	ran        bool
}

// runOracle seeds a rig, persists batch through it at maxBatch, and returns what it stored
// and what the batch cost.
func runOracle(t *testing.T, maxBatch int, seed, batch []sent) oracleResult {
	t.Helper()
	r := newBatchRig(t, maxBatch)
	for _, s := range seed {
		if _, err := r.ep.PersistEvent(core.WithTenant(context.Background(), s.tenant), s.ev); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	r.api.txs.Store(0)
	r.counter.Reset()
	r.run(r.consume(t, batch))
	all, _ := r.counter.Counts()
	if got := len(r.acks.acked()); got != len(batch) {
		t.Fatalf("batch of %d: %d of %d messages acknowledged", maxBatch, got, len(batch))
	}
	if got := r.reported(); len(got) != 0 {
		t.Fatalf("batch of %d: reported %+v; want nothing", maxBatch, got)
	}
	return oracleResult{dump: dumpTables(t, r.db), statements: all, txs: r.api.txs.Load(),
		fallbacks: r.metric(t, metricFallbacks, "", ""), ran: true}
}
