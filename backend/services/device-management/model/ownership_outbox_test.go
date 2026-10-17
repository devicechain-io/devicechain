// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// queuedOwnership returns every queued ownership row, across tenants.
func queuedOwnership(t *testing.T, api *Api) []PendingOwnershipFact {
	t.Helper()
	rows := make([]PendingOwnershipFact, 0)
	require.NoError(t, api.RDB.DB(core.WithSystemContext(context.Background())).
		Order("tenant_id").Order("device_id").Find(&rows).Error)
	return rows
}

// assignDozer creates the dozer-01 → customer edge e-<customer>.
func assignDozer(t *testing.T, api *Api, ctx context.Context, customer string) {
	t.Helper()
	_, err := api.CreateEntityRelationship(ctx, &EntityRelationshipCreateRequest{Token: "e-" + customer,
		SourceType: "device", Source: "dozer-01", TargetType: "customer", Target: customer,
		RelationshipType: AssignmentRelationshipType})
	require.NoError(t, err)
}

// 🔴 THE LEAK THIS FILE EXISTS FOR: a device that LEAVES its owner while the broker refuses
// the fact. The removal commits, the publish fails, and the fact must still be there to
// publish once the broker is back — not dropped with a log line, which left the old owner's
// members on the device's live view for as long as it stayed quiet.
func TestALeaveWhoseFactCouldNotBePublishedIsPublishedLater(t *testing.T) {
	api, ctx, pub := ownershipFixture(t)
	assignDozer(t, api, ctx, "acme-mining")
	require.Equal(t, 1, pub.count())

	pub.fail = errors.New("broker unavailable")
	before := time.Now().UTC()
	removed, err := api.RemoveEntityRelationship(ctx, "e-acme-mining")
	require.NoError(t, err, "the write failed because the broker did: the fact is the drain's, never the write's")
	require.True(t, removed)

	for range 3 {
		_, err = api.DrainOwnershipFacts(context.Background(), OwnershipDrainPageSize)
		require.Error(t, err, "a pass whose publish failed reported success")
		require.Len(t, queuedOwnership(t, api), 1, "a fact the broker refused was dropped")
	}

	pub.fail = nil
	require.Equal(t, 2, pub.count(), "the queued fact was not published once the broker came back")
	require.Equal(t, "dozer-01", pub.last().DeviceToken)
	require.Empty(t, pub.last().Anchors, "the published fact still names the owner the device left")
	// The fact's precedence in device-state is its ChangedTime plus a settle window: a time
	// before the change would let an event received before the removal override it.
	require.False(t, pub.last().ChangedTime.Before(before), "the fact is dated before the change it announces")
	require.Empty(t, queuedOwnership(t, api), "a published fact stayed queued")
}

// A write and the request to announce it commit or roll back TOGETHER: a failure to queue the
// fact fails the write, so no ownership change commits that device-state will never hear of.
func TestAWriteWhoseFactCannotBeQueuedDoesNotCommit(t *testing.T) {
	api, ctx, _ := ownershipFixture(t)
	require.NoError(t, api.RDB.Database.Migrator().DropTable(&PendingOwnershipFact{}))

	_, err := api.CreateEntityRelationship(ctx, &EntityRelationshipCreateRequest{Token: "e-acme",
		SourceType: "device", Source: "dozer-01", TargetType: "customer", Target: "acme-mining",
		RelationshipType: AssignmentRelationshipType})
	require.Error(t, err, "an edge was created although its ownership fact could not be queued")

	found, err := api.EntityRelationshipsByToken(ctx, []string{"e-acme"})
	require.NoError(t, err)
	require.Empty(t, found, "the edge committed without its queued fact")
}

// A rolled-back transaction leaves nothing queued, and a committed one leaves one row per
// device however many times it queued it.
func TestQueuedFactsCommitWithTheirTransaction(t *testing.T) {
	api, ctx, _ := ownershipFixture(t)
	id := deviceIdByToken(t, api, ctx, "dozer-01")
	boom := errors.New("rolled back")
	err := api.RDB.DB(ctx).Transaction(func(tx *gorm.DB) error {
		if _, err := queueOwnershipOnTx(tx, []uint{id}); err != nil {
			return err
		}
		return boom
	})
	require.ErrorIs(t, err, boom)
	require.Empty(t, queuedOwnership(t, api), "a rolled-back write queued a fact")

	require.NoError(t, api.RDB.DB(ctx).Transaction(func(tx *gorm.DB) error {
		for range 3 {
			if _, err := queueOwnershipOnTx(tx, []uint{id, id, 0}); err != nil {
				return err
			}
		}
		return nil
	}))
	rows := queuedOwnership(t, api)
	require.Len(t, rows, 1, "a device queued three times is one row")
	require.Equal(t, "acme", rows[0].TenantId)
	require.Equal(t, id, rows[0].DeviceId)
}

// 🔴 A CHANGE COMMITTED WHILE ITS DEVICE'S FACT IS BEING PUBLISHED IS NOT SWALLOWED. The drain
// read the device's anchors before the change; if it then deleted the row by device alone it
// would delete the request the change just made, and the change would never be announced.
func TestAChangeQueuedDuringAPublishSurvivesTheDelete(t *testing.T) {
	api, ctx, _ := ownershipFixture(t)
	assignDozer(t, api, ctx, "acme-mining")
	pub := &racingOwnership{t: t, api: api, ctx: ctx}
	api.DeviceOwnershipPublisher = pub

	_, err := api.DrainOwnershipFacts(context.Background(), OwnershipDrainPageSize)
	require.NoError(t, err)
	require.Len(t, pub.facts, 1)
	require.Equal(t, []string{"customer:acme-mining"}, pub.facts[0].Anchors)
	require.Len(t, queuedOwnership(t, api), 1, "the change committed during the publish lost its queued row")

	_, err = api.DrainOwnershipFacts(context.Background(), OwnershipDrainPageSize)
	require.NoError(t, err)
	require.Len(t, pub.facts, 2, "the change committed during the publish was never announced")
	require.ElementsMatch(t, []string{"customer:acme-mining", "customer:globex-mining"}, pub.facts[1].Anchors)
	require.Empty(t, queuedOwnership(t, api))
}

// racingOwnership commits a second ownership change from inside its first publish, as a
// writer on another request would between the drain's read and its delete.
type racingOwnership struct {
	t     *testing.T
	api   *Api
	ctx   context.Context
	raced bool
	facts []DeviceOwnershipChangedEvent
}

func (r *racingOwnership) PublishDeviceOwnershipChanged(_ context.Context, e *DeviceOwnershipChangedEvent) error {
	r.facts = append(r.facts, *e)
	if !r.raced {
		r.raced = true
		assignDozer(r.t, r.api, r.ctx, "globex-mining")
	}
	return nil
}

// Nothing to publish through is a refusal that keeps every queued row — never a pass that
// deletes what it could not send, nor one that reports success having sent nothing.
func TestTheDrainRefusesToRunWithoutAPublisher(t *testing.T) {
	api, ctx, _ := ownershipFixture(t)
	assignDozer(t, api, ctx, "acme-mining")
	api.DeviceOwnershipPublisher = nil
	_, err := api.DrainOwnershipFacts(context.Background(), OwnershipDrainPageSize)
	require.ErrorIs(t, err, ErrNoOwnershipPublisher)
	require.Len(t, queuedOwnership(t, api), 1)
}

// A device deleted before its fact was published leaves no row behind and publishes nothing:
// the entity-deleted fact is its last word.
func TestADeletedDevicesQueuedFactIsDroppedUnpublished(t *testing.T) {
	api, ctx, pub := ownershipFixture(t)
	pub.api = nil // drain by hand below
	assignDozer(t, api, ctx, "acme-mining")
	require.Len(t, queuedOwnership(t, api), 1)
	require.NoError(t, api.RDB.DB(ctx).Unscoped().Where("token = ?", "dozer-01").Delete(&Device{}).Error)

	res, err := api.DrainOwnershipFacts(context.Background(), OwnershipDrainPageSize)
	require.NoError(t, err)
	require.Equal(t, 0, res.Published)
	require.Equal(t, 1, res.Removed)
	require.Empty(t, queuedOwnership(t, api))
	require.Zero(t, pub.count())
}

// Each tenant's facts are read, published and deleted under that tenant alone, and a page
// bounds one tenant's pass without starving another.
func TestTheDrainVisitsEachTenantUnderItsOwnTenant(t *testing.T) {
	api, ctx, pub := ownershipFixture(t)
	pub.api = nil
	globex := core.WithTenant(context.Background(), "globex")
	ids := make([]uint, 0)
	for _, tok := range []string{"g-1", "g-2", "g-3"} {
		d := &Device{TokenReference: rdb.TokenReference{Token: tok}}
		require.NoError(t, api.RDB.DB(globex).Create(d).Error)
		ids = append(ids, d.ID)
	}
	require.NoError(t, api.RDB.DB(globex).Transaction(func(tx *gorm.DB) error {
		_, err := queueOwnershipOnTx(tx, ids)
		return err
	}))
	assignDozer(t, api, ctx, "acme-mining")

	res, err := api.DrainOwnershipFacts(context.Background(), 2)
	require.NoError(t, err)
	require.Equal(t, 2, res.Tenants)
	require.Equal(t, 3, res.Published, "one page of two from globex and acme's one")
	require.Len(t, queuedOwnership(t, api), 1, "globex's third fact is the next pass's")

	_, err = api.DrainOwnershipFacts(context.Background(), 2)
	require.NoError(t, err)
	require.Empty(t, queuedOwnership(t, api))
	byTenant := map[string][]string{}
	for i, f := range pub.facts {
		byTenant[pub.tenant[i]] = append(byTenant[pub.tenant[i]], f.DeviceToken)
	}
	require.ElementsMatch(t, []string{"dozer-01"}, byTenant["acme"])
	require.ElementsMatch(t, []string{"g-1", "g-2", "g-3"}, byTenant["globex"])
}

// 🔑 THE REQUEST PATH NO LONGER PUBLISHES. Enabling an access scope queues its members' facts
// and answers; nothing reaches the broker until the drain runs.
func TestAnAccessScopeToggleQueuesItsMembersAndPublishesNothing(t *testing.T) {
	api, ctx, pub := accessFixture(t)
	before := pub.count() // drains what the fixture queued
	pub.api = nil
	_, err := api.SetEntityGroupAccessScope(ctx, "arid", true)
	require.NoError(t, err)
	require.Equal(t, before, pub.count(), "the toggle published inline")
	rows := queuedOwnership(t, api)
	require.Len(t, rows, 1, "the toggle did not queue its member")
	require.Equal(t, deviceIdByToken(t, api, ctx, "d-arid"), rows[0].DeviceId)

	_, err = api.DrainOwnershipFacts(context.Background(), OwnershipDrainPageSize)
	require.NoError(t, err)
	got, ok := lastFactFor(pub, before, "d-arid")
	require.True(t, ok)
	require.Contains(t, got.Anchors, "group:arid")
}

func deviceIdByToken(t *testing.T, api *Api, ctx context.Context, token string) uint {
	t.Helper()
	var d Device
	require.NoError(t, api.RDB.DB(ctx).Where("token = ?", token).First(&d).Error)
	return d.ID
}

// 🔴 A DEVICE THAT KEEPS CHANGING DOES NOT MOVE TO THE BACK OF THE QUEUE. The drain pages
// oldest-first, so a re-queue that refreshed the row's age would let a churning device be
// passed over, pass after pass, for as long as the backlog outran a page: the one device
// whose owners keep moving would be the one whose fact never goes out.
func TestAReQueuedDeviceKeepsItsPlaceInTheBacklog(t *testing.T) {
	api, ctx, _ := ownershipFixture(t)
	_, err := api.DrainOwnershipFacts(context.Background(), OwnershipDrainPageSize)
	require.NoError(t, err)
	require.Empty(t, queuedOwnership(t, api))

	queue := func(id uint) {
		require.NoError(t, api.RDB.DB(ctx).Transaction(func(tx *gorm.DB) error {
			_, err := queueOwnershipOnTx(tx, []uint{id})
			return err
		}))
	}
	const hot, cold1, cold2, cold3 = 9001, 9002, 9003, 9004
	for _, id := range []uint{hot, cold1, cold2, cold3} {
		queue(id)
		time.Sleep(5 * time.Millisecond)
	}
	for range 5 {
		queue(hot)
		time.Sleep(5 * time.Millisecond)
	}

	_, err = api.DrainOwnershipFacts(context.Background(), 2)
	require.NoError(t, err)
	left := queuedOwnership(t, api)
	require.Len(t, left, 2)
	require.Equal(t, []uint{cold2, cold3}, []uint{left[0].DeviceId, left[1].DeviceId},
		"the device queued first was not in the first page: its re-queues moved it behind newer requests")
}

// A fact is dated by the clock the pass was given. On an engine with no clock of its own that
// is the process clock, replaceable so a test can order two passes whose replicas disagree.
func TestAFactIsDatedByTheInjectedClockWhereTheEngineHasNone(t *testing.T) {
	api, ctx, pub := ownershipFixture(t)
	at := time.Date(2031, 5, 6, 7, 8, 9, 0, time.UTC)
	api.ownershipNow = func() time.Time { return at }
	assignDozer(t, api, ctx, "acme-mining")
	require.Equal(t, 1, pub.count())
	require.True(t, pub.last().ChangedTime.Equal(at), "fact dated %v, want the injected %v", pub.last().ChangedTime, at)
}
