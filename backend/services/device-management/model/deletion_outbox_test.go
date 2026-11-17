// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/entity"
	"github.com/devicechain-io/dc-microservice/entityevent"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type deletionPublisher struct {
	events        []entityevent.EntityDeletedEvent
	tenants       []string
	calls, failAt int
	fail          bool
}

func (p *deletionPublisher) PublishEntityDeleted(ctx context.Context, e *entityevent.EntityDeletedEvent) error {
	p.calls++
	if p.fail || p.calls == p.failAt {
		return errors.New("broker unavailable")
	}
	tenant, _ := core.TenantFromContext(ctx)
	p.tenants = append(p.tenants, tenant)
	p.events = append(p.events, *e)
	return nil
}
func deletionDevice(t *testing.T, api *Api, ctx context.Context, token string) *Device {
	t.Helper()
	d := &Device{}
	d.Token = token
	require.NoError(t, api.RDB.DB(ctx).Create(d).Error)
	return d
}
func deleteForOutbox(t *testing.T, api *Api, ctx context.Context, token string) {
	t.Helper()
	deleted, err := api.DeleteDevice(ctx, token)
	require.NoError(t, err)
	require.True(t, deleted)
}
func deletionRows(t *testing.T, api *Api, ctx context.Context) []PendingEntityDeletion {
	t.Helper()
	var rows []PendingEntityDeletion
	require.NoError(t, api.RDB.DB(ctx).Order("id").Find(&rows).Error)
	return rows
}
func drainDeletions(t *testing.T, api *Api, ctx context.Context) EntityDeletionDrainResult {
	t.Helper()
	res, err := api.DrainEntityDeletions(ctx, EntityDeletionDrainPageSize)
	require.NoError(t, err)
	return res
}

// Pins lost deletion facts during broker outages: retries retain the committed fact.
func TestADeletionWhosePublishFailedIsPublishedLater(t *testing.T) {
	api := newDeleteEmitTestApi(t)
	ctx := core.WithTenant(context.Background(), "acme")
	stamp := time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)
	api.ownershipNow = func() time.Time { return stamp }
	pub := &deletionPublisher{fail: true}
	api.EntityDeletedPublisher = pub
	d := deletionDevice(t, api, ctx, "device")
	deleteForOutbox(t, api, ctx, d.Token)
	require.Empty(t, pub.events)
	for range 3 {
		_, err := api.DrainEntityDeletions(ctx, EntityDeletionDrainPageSize)
		require.Error(t, err)
		require.Len(t, deletionRows(t, api, ctx), 1)
	}
	pub.fail = false
	require.Equal(t, 1, drainDeletions(t, api, ctx).Published)
	require.Equal(t, []entityevent.EntityDeletedEvent{{EntityType: entity.TypeDevice, EntityId: d.ID, EntityToken: d.Token, DeletedTime: stamp}}, pub.events)
	require.Empty(t, deletionRows(t, api, ctx))
	require.Zero(t, drainDeletions(t, api, ctx).Published)
	require.Len(t, pub.events, 1)
}

// Pins phantom announcements from deletes whose cascade rolls back.
func TestADeleteThatRollsBackQueuesNothing(t *testing.T) {
	api := newDeleteEmitTestApi(t)
	ctx := core.WithTenant(context.Background(), "acme")
	pub := &deletionPublisher{}
	api.EntityDeletedPublisher = pub
	d := deletionDevice(t, api, ctx, "device")
	failure := errors.New("cascade failed")
	_, err := api.deleteEdgeEntity(ctx, entity.TypeDevice, &Device{}, d.Token, func(*gorm.DB, uint) ([]uint, error) { return nil, failure })
	require.ErrorIs(t, err, failure)
	var count int64
	require.NoError(t, api.RDB.DB(ctx).Model(&Device{}).Where("id = ?", d.ID).Count(&count).Error)
	require.EqualValues(t, 1, count)
	require.Empty(t, deletionRows(t, api, ctx))
	drainDeletions(t, api, ctx)
	require.Empty(t, pub.events)
}

// Pins the fact committing with the delete and nothing else: a failure AFTER the fact was
// queued (the ownership queue, the transaction's last statement) must take the fact back with
// the delete. A publisher called after the commit would pass the test above, since its failure
// comes first; it cannot pass this one, which must also see nothing published before a drain.
func TestAFailureAfterTheFactIsQueuedRollsBackTheFactWithTheDelete(t *testing.T) {
	api := newDeleteEmitTestApi(t)
	ctx := core.WithTenant(context.Background(), "acme")
	pub := &deletionPublisher{}
	api.EntityDeletedPublisher = pub
	d := deletionDevice(t, api, ctx, "device")
	other := deletionDevice(t, api, ctx, "tracker")
	// The cascade names a device whose ownership fact will be queued last; dropping the
	// ownership queue makes that final statement fail after the deletion fact was written.
	require.NoError(t, api.RDB.Database.Migrator().DropTable(&PendingOwnershipFact{}))
	_, err := api.deleteEdgeEntity(ctx, entity.TypeDevice, &Device{}, d.Token,
		func(*gorm.DB, uint) ([]uint, error) { return []uint{other.ID}, nil })
	require.Error(t, err)
	var count int64
	require.NoError(t, api.RDB.DB(ctx).Model(&Device{}).Where("id = ?", d.ID).Count(&count).Error)
	require.EqualValues(t, 1, count, "the delete committed although its transaction failed")
	require.Empty(t, deletionRows(t, api, ctx), "a fact survived the rollback of its delete")
	require.Empty(t, pub.events, "a deletion was announced for a delete that rolled back")
}

// Pins a fact with no tenant. A delete made under a system context gets no tenant from the
// tenant callback; the fact must carry the entity's own tenant, or it can never be published
// and, sorting first, would stop every drain pass before any other tenant's deletions.
func TestADeleteUnderASystemContextQueuesTheEntitysTenant(t *testing.T) {
	api := newDeleteEmitTestApi(t)
	acme := core.WithTenant(context.Background(), "acme")
	pub := &deletionPublisher{}
	api.EntityDeletedPublisher = pub
	d := deletionDevice(t, api, acme, "device")
	_, err := api.deleteEdgeEntity(core.WithSystemContext(context.Background()), entity.TypeDevice, &Device{}, d.Token, nil)
	require.NoError(t, err)
	rows := deletionRows(t, api, acme)
	require.Len(t, rows, 1, "the fact was not queued under the entity's tenant")
	require.Equal(t, "acme", rows[0].TenantId)
	globex := core.WithTenant(context.Background(), "globex")
	g := deletionDevice(t, api, globex, "other")
	deleteForOutbox(t, api, globex, g.Token)
	require.Equal(t, 2, drainDeletions(t, api, context.Background()).Published)
	require.Equal(t, []string{"acme", "globex"}, pub.tenants)
}

// Pins a misleading successful drain with no publisher that could discard facts.
func TestTheDeletionDrainRefusesToRunWithoutAPublisher(t *testing.T) {
	api := newDeleteEmitTestApi(t)
	ctx := core.WithTenant(context.Background(), "acme")
	d := deletionDevice(t, api, ctx, "device")
	deleteForOutbox(t, api, ctx, d.Token)
	_, err := api.DrainEntityDeletions(ctx, EntityDeletionDrainPageSize)
	require.ErrorIs(t, err, ErrNoEntityDeletedPublisher)
	require.Len(t, deletionRows(t, api, ctx), 1)
}

// Pins cross-tenant publication and reordering within each tenant's deletion stream.
func TestTheDeletionDrainPublishesEachTenantUnderItsOwnTenantInOrder(t *testing.T) {
	api := newDeleteEmitTestApi(t)
	pub := &deletionPublisher{}
	api.EntityDeletedPublisher = pub
	expected := map[string][]uint{}
	for _, tenant := range []string{"b", "a"} {
		ctx := core.WithTenant(context.Background(), tenant)
		for i := range 2 {
			d := deletionDevice(t, api, ctx, fmt.Sprintf("d%d", i))
			deleteForOutbox(t, api, ctx, d.Token)
			expected[tenant] = append(expected[tenant], d.ID)
		}
	}
	res := drainDeletions(t, api, context.Background())
	require.Equal(t, 2, res.Tenants)
	require.Equal(t, 4, res.Published)
	actual := map[string][]uint{}
	for i, e := range pub.events {
		actual[pub.tenants[i]] = append(actual[pub.tenants[i]], e.EntityId)
	}
	require.Equal(t, expected, actual)
}

// Pins overtaking a failed fact and losing an already acknowledged prefix.
func TestAPublishFailureStopsThePassAndKeepsLaterRows(t *testing.T) {
	api := newDeleteEmitTestApi(t)
	ctx := core.WithTenant(context.Background(), "a")
	pub := &deletionPublisher{failAt: 2}
	api.EntityDeletedPublisher = pub
	var ids []uint
	for i := range 3 {
		d := deletionDevice(t, api, ctx, fmt.Sprintf("d%d", i))
		deleteForOutbox(t, api, ctx, d.Token)
		ids = append(ids, d.ID)
	}
	other := core.WithTenant(ctx, "b")
	d := deletionDevice(t, api, other, "other")
	deleteForOutbox(t, api, other, d.Token)
	res, err := api.DrainEntityDeletions(ctx, EntityDeletionDrainPageSize)
	require.Error(t, err)
	require.Equal(t, 1, res.Published)
	require.Equal(t, 2, pub.calls)
	rows := deletionRows(t, api, ctx)
	require.Len(t, rows, 2)
	require.Equal(t, ids[1], rows[0].EntityId)
	require.Equal(t, ids[2], rows[1].EntityId)
	require.Len(t, deletionRows(t, api, other), 1)
	pub.failAt = 0
	drainDeletions(t, api, ctx)
	require.Equal(t, ids[1], pub.events[1].EntityId)
	require.Equal(t, ids[2], pub.events[2].EntityId)
}

// Pins stamping at drain time: a fact published after an outage must carry the time of the
// delete, not of the publish, or a late fact would outrank a re-creation made meanwhile.
func TestTheDeletedTimeIsTheTimeOfTheDeleteNotOfTheDrain(t *testing.T) {
	api := newDeleteEmitTestApi(t)
	ctx := core.WithTenant(context.Background(), "acme")
	stamp := time.Date(2001, 2, 3, 4, 5, 6, 0, time.UTC)
	now := stamp
	api.ownershipNow = func() time.Time { return now }
	pub := &deletionPublisher{}
	api.EntityDeletedPublisher = pub
	d := deletionDevice(t, api, ctx, "device")
	deleteForOutbox(t, api, ctx, d.Token)
	now = stamp.Add(24 * time.Hour)
	drainDeletions(t, api, ctx)
	require.Len(t, pub.events, 1)
	require.Equal(t, stamp, pub.events[0].DeletedTime)
}
