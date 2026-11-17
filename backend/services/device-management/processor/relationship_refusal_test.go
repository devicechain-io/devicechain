// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"errors"
	"github.com/devicechain-io/dc-device-management/config"
	"github.com/devicechain-io/dc-device-management/model"
	esmodel "github.com/devicechain-io/dc-event-sources/model"
	"github.com/devicechain-io/dc-microservice/eventtype"
	"github.com/devicechain-io/dc-microservice/resolvedevent"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"
	"testing"
	"time"
)

func relationshipEvent(kind, targetType, target string) *esmodel.UnresolvedEvent {
	now := time.Now().UTC()
	return &esmodel.UnresolvedEvent{Device: "dev", EventType: eventtype.NewRelationship,
		OccurredTime: now, ProcessedTime: now,
		Payload: &esmodel.UnresolvedNewRelationshipPayload{RelationshipType: kind, TargetType: targetType, Target: target}}
}
func relationshipRig(t *testing.T) *resolveRig {
	rig := newCountingResolveRig(t, false, nil)
	require.NoError(t, rig.db.AutoMigrate(&model.Customer{}, &model.Area{}, &model.Asset{}, &model.EntityGroup{}, &model.PendingOwnershipFact{}, &model.PendingEntityDeletion{}))
	for _, token := range []string{"alpha", "bravo"} {
		c := &model.Customer{}
		c.Token = token
		require.NoError(t, rig.db.WithContext(rig.ctx).Create(c).Error)
	}
	area := &model.Area{}
	area.Token = "bravo"
	asset := &model.Asset{}
	asset.Token = "bravo"
	group := &model.EntityGroup{}
	group.Token = "bravo"
	for _, target := range []any{area, asset, group} {
		require.NoError(t, rig.db.WithContext(rig.ctx).Create(target).Error)
	}
	return rig
}
func edgeCount(t *testing.T, rig *resolveRig) int64 {
	t.Helper()
	var n int64
	require.NoError(t, rig.db.WithContext(rig.ctx).Model(&model.EntityRelationship{}).Count(&n).Error)
	return n
}
func TestADeviceCannotAnchorItselfToACustomer(t *testing.T) {
	rig := relationshipRig(t)
	before := edgeCount(t, rig)
	counter := prometheus.NewCounter(prometheus.CounterOpts{Name: "test_relationship_refused_total"})
	rig.rez.relationshipRefused = counter
	event := relationshipEvent("assigned", "customer", "bravo")
	resolved, failed, acked := processOne(rig, event, 1)
	require.Zero(t, resolved)
	require.Len(t, failed, 1, "first delivery must reach failed-events")
	require.Equal(t, 1, acked)
	require.Equal(t, "acme", failed[0].tenant)
	require.Equal(t, uint(resolvedevent.FailureReason_Invalid), failed[0].reason)
	require.ErrorIs(t, failed[0].err, model.ErrDeviceAuthoredOwnership)
	require.Equal(t, event.Payload, failed[0].event.Payload)
	require.Equal(t, before, edgeCount(t, rig))
	require.Equal(t, 1.0, testutil.ToFloat64(counter))
}
func TestDeviceAuthoredOwnershipAndContainmentAreRefused(t *testing.T) {
	for _, row := range []struct{ kind, target string }{
		{"tracks", "customer"}, {"assigned", "area"}, {"assigned", "asset"}, {"assigned", "group"}, {"contains", "asset"},
	} {
		t.Run(row.kind+"-"+row.target, func(t *testing.T) {
			rig := relationshipRig(t)
			before := edgeCount(t, rig)
			results, reason, err := rig.rez.ResolveEvent(rig.ctx, relationshipEvent(row.kind, row.target, "bravo"))
			require.ErrorIs(t, err, model.ErrDeviceAuthoredOwnership)
			require.Empty(t, results)
			require.Equal(t, uint(resolvedevent.FailureReason_Invalid), reason)
			require.Equal(t, before, edgeCount(t, rig))
		})
	}
}
func TestAnOldOwnershipEdgeCannotBypassDeviceRefusal(t *testing.T) {
	rig := relationshipRig(t)
	token := model.DeviceAuthoredRelationshipToken("acme", "dev", "customer", "bravo", "assigned")
	_, err := rig.api.CreateEntityRelationship(rig.ctx, &model.EntityRelationshipCreateRequest{
		Token: token, SourceType: "device", Source: "dev", TargetType: "customer", Target: "bravo", RelationshipType: "assigned"})
	require.NoError(t, err)
	before := edgeCount(t, rig)
	result, reason, err := rig.rez.CreateNewEntityRelationship(rig.ctx, deviceWithToken("dev"), *relationshipEvent("assigned", "customer", "bravo").Payload.(*esmodel.UnresolvedNewRelationshipPayload))
	require.ErrorIs(t, err, model.ErrDeviceAuthoredOwnership)
	require.Nil(t, result)
	require.Equal(t, uint(resolvedevent.FailureReason_Invalid), reason)
	require.Equal(t, before, edgeCount(t, rig), "existing edges are preserved")
}
func TestDeviceUntrackedRelationshipsRemainIdempotent(t *testing.T) {
	for _, kind := range []string{"observes", "member"} {
		t.Run(kind, func(t *testing.T) {
			rig := relationshipRig(t)
			if kind == "observes" {
				_, err := rig.api.CreateEntityRelationshipType(rig.ctx, &model.EntityRelationshipTypeCreateRequest{Token: kind})
				require.NoError(t, err)
			}
			before := edgeCount(t, rig)
			event := relationshipEvent(kind, "customer", "bravo")
			first, _, err := rig.rez.ResolveEvent(rig.ctx, event)
			require.NoError(t, err)
			second, _, err := rig.rez.ResolveEvent(rig.ctx, event)
			require.NoError(t, err)
			require.Len(t, first, 1)
			require.Len(t, second, 1)
			require.Equal(t, first[0].Resolved.Payload, second[0].Resolved.Payload)
			require.Equal(t, before+1, edgeCount(t, rig))
			// An untracked relationship is not ownership, so its target must not be stamped on
			// the event as an anchor: that would put this event in bravo's scope.
			for _, a := range append(first[0].Resolved.Anchors, second[0].Resolved.Anchors...) {
				require.False(t, a.AnchorType == "customer" && a.AnchorToken == "bravo",
					"the untracked target was stamped as an anchor: %+v", a)
			}
		})
	}
}

// The stored type decides, reserved tokens included: a tenant that marks "member" tracked
// has made it ownership, and a device may no longer assert it.
func TestAReservedTypeMarkedTrackedIsRefusedToo(t *testing.T) {
	rig := relationshipRig(t)
	_, err := rig.api.CreateEntityRelationshipType(rig.ctx, &model.EntityRelationshipTypeCreateRequest{Token: "member", Tracked: true})
	require.NoError(t, err)
	// An edge the device created before the type was marked tracked, under the derived token:
	// the redelivery must be refused, not answered "found" by the idempotency lookup.
	token := model.DeviceAuthoredRelationshipToken("acme", "dev", "customer", "bravo", "member")
	_, err = rig.api.CreateEntityRelationship(rig.ctx, &model.EntityRelationshipCreateRequest{
		Token: token, SourceType: "device", Source: "dev", TargetType: "customer", Target: "bravo", RelationshipType: "member"})
	require.NoError(t, err)
	before := edgeCount(t, rig)
	_, reason, err := rig.rez.ResolveEvent(rig.ctx, relationshipEvent("member", "customer", "bravo"))
	require.ErrorIs(t, err, model.ErrDeviceAuthoredOwnership)
	require.Equal(t, uint(resolvedevent.FailureReason_Invalid), reason)
	require.Equal(t, before, edgeCount(t, rig))
}
func TestAGatewayCannotAssignItsChildThroughAnEvent(t *testing.T) {
	rig := gatewayRig(t, config.AuthModeDisabled)
	event := relationshipEvent("assigned", "customer", "bravo")
	event.Gateway, event.Device = "gw", "c"
	_, failed, acked := processOne(rig, event, 1)
	require.Len(t, failed, 1)
	require.ErrorIs(t, failed[0].err, model.ErrDeviceAuthoredOwnership)
	require.Equal(t, 1, acked)
}

type relationshipLookupFailure struct {
	*model.CachedApi
	err error
}

func (a relationshipLookupFailure) EntityRelationshipTypesByToken(context.Context, []string) ([]*model.EntityRelationshipType, error) {
	return nil, a.err
}
func TestRelationshipTypeLookupFailureRemainsRetryable(t *testing.T) {
	rig := relationshipRig(t)
	failure := errors.New("type lookup unavailable")
	rig.rez.Api = relationshipLookupFailure{rig.capi, failure}
	before := edgeCount(t, rig)
	edge, reason, err := rig.rez.CreateNewEntityRelationship(rig.ctx, deviceWithToken("dev"), esmodel.UnresolvedNewRelationshipPayload{RelationshipType: "custom", TargetType: "customer", Target: "bravo"})
	require.ErrorIs(t, err, failure)
	require.Nil(t, edge)
	require.Equal(t, uint(resolvedevent.FailureReason_ApiCallFailed), reason)
	require.Equal(t, before, edgeCount(t, rig))
}
