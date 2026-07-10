// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"math"
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

func newProcAttributeStore(t *testing.T) *model.DeviceAttributeStore {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := rdb.RegisterTenantScoping(db); err != nil {
		t.Fatalf("register tenant scoping: %v", err)
	}
	if err := db.AutoMigrate(&model.DeviceAttribute{}, &model.DeviceAttributeDeletion{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return model.NewDeviceAttributeStore(&rdb.RdbManager{Database: db})
}

// attrMsg builds a consumed device-attribute fact on tenant's scoped subject.
func attrMsg(t *testing.T, tenant string, ev *dmmodel.DeviceAttributeEvent, ack *fakeAck) messaging.Message {
	t.Helper()
	b, err := dmproto.MarshalDeviceAttributeEvent(ev)
	if err != nil {
		t.Fatalf("marshal attribute: %v", err)
	}
	m := messaging.NewConsumedMessage("dc."+tenant+".device-attribute", b, 0, nil, ack)
	m.StreamSeq = 1
	return m
}

func loadAttr(t *testing.T, s *model.DeviceAttributeStore, tenant, device, scope, key string) (float64, bool) {
	t.Helper()
	all, err := s.LoadAll(context.Background())
	if err != nil {
		t.Fatalf("load all: %v", err)
	}
	for _, a := range all {
		if a.Tenant == tenant && a.DeviceToken == device && a.Scope == scope && a.AttrKey == key {
			return a.Value, true
		}
	}
	return 0, false
}

// A numeric set fact is persisted to the projection, then acked (persist-before-ack).
func TestAttributeConsumerPersistsSetThenAcks(t *testing.T) {
	ack := &fakeAck{acked: make(chan struct{}, 1)}
	store := newProcAttributeStore(t)
	rp := newTestProcessor(newTestStore(t), nil, 1)
	rp.AttributeStore = store
	rp.AttributeReader = &fakeReader{results: []readResult{
		{msg: attrMsg(t, "acme", &dmmodel.DeviceAttributeEvent{
			DeviceToken: "d1", AttrKey: "maxTemp", Scope: "SHARED", Value: 100, UpdatedAt: testBase}, ack)},
	}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	rp.procCtx = ctx
	rp.readerWG.Add(1)
	go rp.runAttributeConsumer()

	waitForAck(t, ack)
	cancel()
	rp.readerWG.Wait()

	if v, ok := loadAttr(t, store, "acme", "d1", "SHARED", "maxTemp"); !ok || v != 100 {
		t.Fatalf("set fact not persisted: v=%v ok=%v", v, ok)
	}
}

// A removal fact tombstones the row (a non-numeric overwrite or delete emits Removed).
func TestAttributeConsumerPersistsRemoval(t *testing.T) {
	store := newProcAttributeStore(t)
	if err := store.Upsert(context.Background(), &model.DeviceAttribute{
		Tenant: "acme", DeviceToken: "d1", Scope: "SHARED", AttrKey: "k", Value: 5, LastEventAt: testBase}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	ack := &fakeAck{acked: make(chan struct{}, 1)}
	rp := newTestProcessor(newTestStore(t), nil, 1)
	rp.AttributeStore = store
	rp.AttributeReader = &fakeReader{results: []readResult{
		{msg: attrMsg(t, "acme", &dmmodel.DeviceAttributeEvent{
			DeviceToken: "d1", AttrKey: "k", Scope: "SHARED", Removed: true, UpdatedAt: testBase.Add(time.Hour)}, ack)},
	}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	rp.procCtx = ctx
	rp.readerWG.Add(1)
	go rp.runAttributeConsumer()

	waitForAck(t, ack)
	cancel()
	rp.readerWG.Wait()

	if _, ok := loadAttr(t, store, "acme", "d1", "SHARED", "k"); ok {
		t.Fatalf("removal fact should tombstone the row")
	}
}

// A malformed fact (empty scope) is dropped-and-acked, never persisted as a phantom row.
func TestAttributeConsumerDropsMalformed(t *testing.T) {
	ack := &fakeAck{acked: make(chan struct{}, 1)}
	store := newProcAttributeStore(t)
	rp := newTestProcessor(newTestStore(t), nil, 1)
	rp.AttributeStore = store
	rp.AttributeReader = &fakeReader{results: []readResult{
		{msg: attrMsg(t, "acme", &dmmodel.DeviceAttributeEvent{
			DeviceToken: "d1", AttrKey: "k", Scope: "", Value: 1, UpdatedAt: testBase}, ack)},
	}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	rp.procCtx = ctx
	rp.readerWG.Add(1)
	go rp.runAttributeConsumer()

	waitForAck(t, ack)
	cancel()
	rp.readerWG.Wait()

	if rows, _ := store.LoadAll(context.Background()); len(rows) != 0 {
		t.Fatalf("a malformed (empty-scope) fact must not be persisted, got %+v", rows)
	}
}

// A DEVICE deletion purges the device's attribute projection (the entity-deleted consumer's
// attribute teardown, alongside the roster tombstone).
func TestEntityDeletedConsumerPurgesDeviceAttributes(t *testing.T) {
	attrStore := newProcAttributeStore(t)
	ctx := context.Background()
	for _, d := range []string{"d1", "d2"} {
		if err := attrStore.Upsert(ctx, &model.DeviceAttribute{
			Tenant: "acme", DeviceToken: d, Scope: "SHARED", AttrKey: "k", Value: 100, LastEventAt: testBase}); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	ack := &fakeAck{acked: make(chan struct{}, 1)}
	rp := newTestProcessor(newTestStore(t), nil, 1)
	rp.AttributeStore = attrStore // no RosterStore: exercises the attribute-only teardown path
	rp.EntityDeletedReader = &fakeReader{results: []readResult{
		{msg: entityDeletedMsg(t, "acme", entity.TypeDevice, "d1", ack)},
	}}
	cctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	rp.procCtx = cctx
	rp.readerWG.Add(1)
	go rp.runEntityDeletedConsumer()

	waitForAck(t, ack)
	cancel()
	rp.readerWG.Wait()

	if _, ok := loadAttr(t, attrStore, "acme", "d1", "SHARED", "k"); ok {
		t.Fatalf("the deleted device's attributes must be purged")
	}
	if v, ok := loadAttr(t, attrStore, "acme", "d2", "SHARED", "k"); !ok || v != 100 {
		t.Fatalf("a surviving device's attributes must be untouched, got v=%v ok=%v", v, ok)
	}
}

// The PRODUCTION entity-deleted configuration wires BOTH stores: a device deletion must tombstone
// the roster AND purge the attributes, and ack the fact EXACTLY ONCE.
func TestEntityDeletedConsumerBothStores(t *testing.T) {
	roster := newProcRosterStore(t)
	attrs := newProcAttributeStore(t)
	ctx := context.Background()
	if err := roster.Upsert(ctx, &model.DeviceRoster{Tenant: "acme", DeviceToken: "d1", ProfileToken: "p", ExpectedSince: testBase}); err != nil {
		t.Fatalf("seed roster: %v", err)
	}
	if err := attrs.Upsert(ctx, &model.DeviceAttribute{Tenant: "acme", DeviceToken: "d1", Scope: "SHARED", AttrKey: "k", Value: 100, LastEventAt: testBase}); err != nil {
		t.Fatalf("seed attr: %v", err)
	}
	ack := &fakeAck{acked: make(chan struct{}, 1)}
	rp := newTestProcessor(newTestStore(t), nil, 1)
	rp.RosterStore = roster
	rp.AttributeStore = attrs // both wired: the shipped production path
	rp.EntityDeletedReader = &fakeReader{results: []readResult{
		{msg: entityDeletedMsg(t, "acme", entity.TypeDevice, "d1", ack)},
	}}
	cctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	rp.procCtx = cctx
	rp.readerWG.Add(1)
	go rp.runEntityDeletedConsumer()

	waitForAck(t, ack)
	cancel()
	rp.readerWG.Wait()

	if rows, _ := roster.LoadAll(ctx); len(rows) != 0 {
		t.Fatalf("roster entry must be tombstoned, got %+v", rows)
	}
	if _, ok := loadAttr(t, attrs, "acme", "d1", "SHARED", "k"); ok {
		t.Fatalf("attributes must be purged")
	}
	if ack.acks != 1 {
		t.Fatalf("the device deletion must be acked exactly once, acks=%d", ack.acks)
	}
}

// attributeFactPoison drops every malformed/forged shape (each defends a real hazard) and admits a
// well-formed fact. This covers the arms the consumer-level drop test does not exercise directly.
func TestAttributeFactPoison(t *testing.T) {
	long := string(make([]byte, 300)) // over core.MaxTokenLen (128) and over the scope bound (64)
	ok := &dmmodel.DeviceAttributeEvent{DeviceToken: "d1", AttrKey: "k", Scope: "SHARED", Value: 1, UpdatedAt: testBase}
	cases := []struct {
		name string
		ev   *dmmodel.DeviceAttributeEvent
		bad  bool
	}{
		{"well-formed set", ok, false},
		{"well-formed server removal", &dmmodel.DeviceAttributeEvent{DeviceToken: "d1", AttrKey: "k", Scope: "SERVER", Removed: true, UpdatedAt: testBase}, false},
		{"empty device", &dmmodel.DeviceAttributeEvent{DeviceToken: "", AttrKey: "k", Scope: "SHARED", UpdatedAt: testBase}, true},
		{"over-long device", &dmmodel.DeviceAttributeEvent{DeviceToken: long, AttrKey: "k", Scope: "SHARED", UpdatedAt: testBase}, true},
		{"empty key", &dmmodel.DeviceAttributeEvent{DeviceToken: "d1", AttrKey: "", Scope: "SHARED", UpdatedAt: testBase}, true},
		{"over-long key", &dmmodel.DeviceAttributeEvent{DeviceToken: "d1", AttrKey: long, Scope: "SHARED", UpdatedAt: testBase}, true},
		{"empty scope", &dmmodel.DeviceAttributeEvent{DeviceToken: "d1", AttrKey: "k", Scope: "", UpdatedAt: testBase}, true},
		{"over-long scope", &dmmodel.DeviceAttributeEvent{DeviceToken: "d1", AttrKey: "k", Scope: long, UpdatedAt: testBase}, true},
		{"client scope (device-set)", &dmmodel.DeviceAttributeEvent{DeviceToken: "d1", AttrKey: "k", Scope: "CLIENT", UpdatedAt: testBase}, true},
		{"junk scope", &dmmodel.DeviceAttributeEvent{DeviceToken: "d1", AttrKey: "k", Scope: "SHART", UpdatedAt: testBase}, true},
		{"NaN value on set", &dmmodel.DeviceAttributeEvent{DeviceToken: "d1", AttrKey: "k", Scope: "SHARED", Value: math.NaN(), UpdatedAt: testBase}, true},
		{"Inf value on set", &dmmodel.DeviceAttributeEvent{DeviceToken: "d1", AttrKey: "k", Scope: "SHARED", Value: math.Inf(1), UpdatedAt: testBase}, true},
		{"NaN tolerated on removal", &dmmodel.DeviceAttributeEvent{DeviceToken: "d1", AttrKey: "k", Scope: "SHARED", Value: math.NaN(), Removed: true, UpdatedAt: testBase}, false},
		{"zero updated-at", &dmmodel.DeviceAttributeEvent{DeviceToken: "d1", AttrKey: "k", Scope: "SHARED", UpdatedAt: time.Time{}}, true},
	}
	for _, c := range cases {
		if _, bad := attributeFactPoison(c.ev); bad != c.bad {
			t.Errorf("%s: attributeFactPoison bad=%v, want %v", c.name, bad, c.bad)
		}
	}
}

// decodeAttributeFact drops a fact with no parseable tenant or an unparseable payload, and decodes
// a well-formed one.
func TestDecodeAttributeFactDropsUnusable(t *testing.T) {
	rp := &ResolvedEventsProcessor{procCtx: context.Background()}
	if _, _, ok := decodeAttributeFact(rp, messaging.NewConsumedMessage("badsubject", nil, 0, nil, nil)); ok {
		t.Fatal("an attribute fact with no parseable tenant must be dropped")
	}
	if _, _, ok := decodeAttributeFact(rp, messaging.NewConsumedMessage("dc.acme.device-attribute", []byte("not-proto\xff"), 0, nil, nil)); ok {
		t.Fatal("an attribute fact with an unparseable payload must be dropped")
	}
	tenant, ev, ok := decodeAttributeFact(rp, attrMsg(t, "acme", &dmmodel.DeviceAttributeEvent{
		DeviceToken: "d1", AttrKey: "k", Scope: "SERVER", Value: 3, UpdatedAt: testBase}, nil))
	if !ok || tenant != "acme" || ev.DeviceToken != "d1" || ev.Scope != "SERVER" || ev.Value != 3 {
		t.Fatalf("a well-formed attribute fact should decode: ok=%v tenant=%q ev=%+v", ok, tenant, ev)
	}
}
