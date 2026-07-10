// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"testing"
	"time"

	dmmodel "github.com/devicechain-io/dc-device-management/model"
	dmproto "github.com/devicechain-io/dc-device-management/proto"
	"github.com/devicechain-io/dc-event-processing/model"
	"github.com/devicechain-io/dc-microservice/entity"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

// waitForAck blocks until the consumer acks (a race-free sync point) so the test can cancel the
// loop AFTER the fact is persisted-and-acked rather than mid-persist against a cancelled context.
func waitForAck(t *testing.T, ack *fakeAck) {
	t.Helper()
	select {
	case <-ack.acked:
	case <-time.After(2 * time.Second):
		t.Fatal("consumer did not ack within the timeout")
	}
}

func newProcRosterStore(t *testing.T) *model.DeviceRosterStore {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := rdb.RegisterTenantScoping(db); err != nil {
		t.Fatalf("register tenant scoping: %v", err)
	}
	if err := db.AutoMigrate(&model.DeviceRoster{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return model.NewDeviceRosterStore(&rdb.RdbManager{Database: db})
}

func newProcActiveStore(t *testing.T) *model.ProfileActiveStore {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := rdb.RegisterTenantScoping(db); err != nil {
		t.Fatalf("register tenant scoping: %v", err)
	}
	if err := db.AutoMigrate(&model.ProfileActive{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return model.NewProfileActiveStore(&rdb.RdbManager{Database: db})
}

// rosterMsg builds a consumed device-roster fact on tenant's scoped subject.
func rosterMsg(t *testing.T, tenant, device, profile string, since time.Time, ack *fakeAck) messaging.Message {
	t.Helper()
	b, err := dmproto.MarshalDeviceRosterEvent(&dmmodel.DeviceRosterEvent{
		DeviceToken: device, ProfileToken: profile, ExpectedSince: since,
	})
	if err != nil {
		t.Fatalf("marshal roster: %v", err)
	}
	m := messaging.NewConsumedMessage("dc."+tenant+".device-roster", b, 0, nil, ack)
	m.StreamSeq = 1
	return m
}

// entityDeletedMsg builds a consumed entity-deleted fact on tenant's scoped subject. DeletedTime
// is stamped AFTER testBase (a device is always deleted after it was created), so the tombstone's
// lifecycle clock is newer than a create seeded at testBase and the monotonic guard applies it.
func entityDeletedMsg(t *testing.T, tenant string, etype entity.Type, token string, ack *fakeAck) messaging.Message {
	t.Helper()
	b, err := dmproto.MarshalEntityDeletedEvent(&dmmodel.EntityDeletedEvent{
		EntityType: etype, EntityId: 1, EntityToken: token, DeletedTime: testBase.Add(time.Hour),
	})
	if err != nil {
		t.Fatalf("marshal entity-deleted: %v", err)
	}
	m := messaging.NewConsumedMessage("dc."+tenant+".entity-deleted", b, 0, nil, ack)
	m.StreamSeq = 1
	return m
}

// runRosterConsumer persists a fact to the durable roster projection, then acks — persist-
// before-ack durability (the projection, not the finite-retention stream, is the restart source).
func TestRosterConsumerPersistsThenAcks(t *testing.T) {
	ack := &fakeAck{acked: make(chan struct{}, 1)}
	store := newProcRosterStore(t)
	rp := newTestProcessor(newTestStore(t), nil, 1)
	rp.RosterStore = store
	rp.RosterReader = &fakeReader{results: []readResult{
		{msg: rosterMsg(t, "acme", "dev1", "prof", testBase, ack)},
	}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	rp.procCtx = ctx
	rp.readerWG.Add(1)
	go rp.runRosterConsumer()

	waitForAck(t, ack)
	cancel()
	rp.readerWG.Wait()

	rows, err := store.LoadAll(context.Background())
	if err != nil {
		t.Fatalf("load roster: %v", err)
	}
	if len(rows) != 1 || rows[0].DeviceToken != "dev1" || rows[0].ProfileToken != "prof" || !rows[0].ExpectedSince.Equal(testBase) {
		t.Fatalf("roster fact was not persisted correctly: %+v", rows)
	}
}

// A DEVICE deletion removes its roster entry; a non-device deletion is acked and ignored.
func TestEntityDeletedConsumerRemovesDeviceOnly(t *testing.T) {
	store := newProcRosterStore(t)
	ctx := context.Background()
	// Seed two rostered devices.
	for _, d := range []string{"dev1", "dev2"} {
		if err := store.Upsert(ctx, &model.DeviceRoster{Tenant: "acme", DeviceToken: d, ProfileToken: "p", ExpectedSince: testBase}); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	devAck, grpAck := &fakeAck{acked: make(chan struct{}, 1)}, &fakeAck{}
	rp := newTestProcessor(newTestStore(t), nil, 1)
	rp.RosterStore = store
	rp.EntityDeletedReader = &fakeReader{results: []readResult{
		{msg: entityDeletedMsg(t, "acme", entity.TypeDeviceGroup, "grp1", grpAck)}, // ignored
		{msg: entityDeletedMsg(t, "acme", entity.TypeDevice, "dev1", devAck)},      // removes dev1
	}}
	cctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	rp.procCtx = cctx
	rp.readerWG.Add(1)
	go rp.runEntityDeletedConsumer()

	waitForAck(t, devAck)
	cancel()
	rp.readerWG.Wait()

	if grpAck.acks != 1 {
		t.Fatalf("a non-device deletion should be acked and ignored, acks=%d", grpAck.acks)
	}
	rows, _ := store.LoadAll(ctx)
	if len(rows) != 1 || rows[0].DeviceToken != "dev2" {
		t.Fatalf("only the deleted device's roster entry should be removed, got %+v", rows)
	}
}

// A roster fact with an empty device token (a forged/malformed fact) is dropped-and-acked, never
// persisted as a phantom row.
func TestRosterConsumerDropsEmptyDeviceToken(t *testing.T) {
	ack := &fakeAck{acked: make(chan struct{}, 1)}
	store := newProcRosterStore(t)
	rp := newTestProcessor(newTestStore(t), nil, 1)
	rp.RosterStore = store
	rp.RosterReader = &fakeReader{results: []readResult{
		{msg: rosterMsg(t, "acme", "", "prof", testBase, ack)},
	}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	rp.procCtx = ctx
	rp.readerWG.Add(1)
	go rp.runRosterConsumer()

	waitForAck(t, ack)
	cancel()
	rp.readerWG.Wait()

	if rows, _ := store.LoadAll(context.Background()); len(rows) != 0 {
		t.Fatalf("an empty-device-token fact must not be persisted, got %+v", rows)
	}
}

// The rule consumer maintains the active-version projection off the same fact (carrying the
// publish time — the grace base) alongside the rule projection.
func TestRuleConsumerMaintainsProfileActive(t *testing.T) {
	ack := &fakeAck{acked: make(chan struct{}, 1)}
	ruleStore := newTestRuleStore(t)
	activeStore := newProcActiveStore(t)
	published := testBase.Add(-2 * time.Hour)

	b, err := dmproto.MarshalDetectionRulesPublishedEvent(&dmmodel.DetectionRulesPublishedEvent{
		ProfileVersionToken: "prof@3",
		PublishedAt:         published,
		Rules:               []dmmodel.PublishedDetectionRule{{Token: "r1", Definition: validFactRule}},
	})
	if err != nil {
		t.Fatalf("marshal fact: %v", err)
	}
	msg := messaging.NewConsumedMessage("dc.acme.detection-rules-published", b, 0, nil, ack)
	msg.StreamSeq = 1

	rp := newTestProcessor(newTestStore(t), nil, 1)
	rp.RuleStore = ruleStore
	rp.ProfileActiveStore = activeStore
	rp.ruleUpdates = make(chan ruleUpdate, 1)
	rp.RuleUpdatesReader = &fakeReader{results: []readResult{{msg: msg}}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	rp.procCtx = ctx
	rp.readerWG.Add(1)
	go rp.runRuleConsumer()

	<-rp.ruleUpdates // the compiled rule reached the loop
	waitForAck(t, ack)
	cancel()
	rp.readerWG.Wait()

	actives, err := activeStore.LoadAll(context.Background())
	if err != nil {
		t.Fatalf("load actives: %v", err)
	}
	if len(actives) != 1 {
		t.Fatalf("expected 1 profile-active row, got %d", len(actives))
	}
	a := actives[0]
	if a.Tenant != "acme" || a.ProfileToken != "prof" || a.ActiveVersionToken != "prof@3" || !a.PublishedAt.Equal(published) {
		t.Fatalf("profile-active not maintained from the fact: %+v", a)
	}
}

// decodeRosterFact / decodeEntityDeletedFact drop a fact with no parseable tenant or an
// unparseable payload (not fatal), and decode a well-formed one.
func TestDecodeRosterAndEntityDeletedDropUnusable(t *testing.T) {
	rp := &ResolvedEventsProcessor{procCtx: context.Background()}

	if _, _, ok := decodeRosterFact(rp, messaging.NewConsumedMessage("badsubject", nil, 0, nil, nil)); ok {
		t.Fatal("a roster fact with no parseable tenant must be dropped")
	}
	if _, _, ok := decodeRosterFact(rp, messaging.NewConsumedMessage("dc.acme.device-roster", []byte("not-proto\xff"), 0, nil, nil)); ok {
		t.Fatal("a roster fact with an unparseable payload must be dropped")
	}
	tenant, ev, ok := decodeRosterFact(rp, rosterMsg(t, "acme", "dev1", "prof", testBase, nil))
	if !ok || tenant != "acme" || ev.DeviceToken != "dev1" {
		t.Fatalf("a well-formed roster fact should decode: ok=%v tenant=%q ev=%+v", ok, tenant, ev)
	}

	if _, _, ok := decodeEntityDeletedFact(rp, messaging.NewConsumedMessage("badsubject", nil, 0, nil, nil)); ok {
		t.Fatal("an entity-deleted fact with no parseable tenant must be dropped")
	}
	dtenant, dev, dok := decodeEntityDeletedFact(rp, entityDeletedMsg(t, "acme", entity.TypeDevice, "dev1", nil))
	if !dok || dtenant != "acme" || dev.EntityToken != "dev1" || dev.EntityType != entity.TypeDevice {
		t.Fatalf("a well-formed entity-deleted fact should decode: ok=%v tenant=%q ev=%+v", dok, dtenant, dev)
	}
}

// profileActiveFromFact splits the stable profile token off the version token and skips a
// token that does not split into a non-empty profile + version.
func TestProfileActiveFromFact(t *testing.T) {
	a, ok := profileActiveFromFact("acme", &dmmodel.DetectionRulesPublishedEvent{ProfileVersionToken: "prof@3", PublishedAt: testBase})
	if !ok || a.ProfileToken != "prof" || a.ActiveVersionToken != "prof@3" || a.Tenant != "acme" || !a.PublishedAt.Equal(testBase) {
		t.Fatalf("well-formed token should split: ok=%v a=%+v", ok, a)
	}
	for _, bad := range []string{"", "prof", "@3", "prof@"} {
		if _, ok := profileActiveFromFact("acme", &dmmodel.DetectionRulesPublishedEvent{ProfileVersionToken: bad}); ok {
			t.Fatalf("malformed version token %q must be skipped", bad)
		}
	}
}
