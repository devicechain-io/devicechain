// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build integration

// Group access scopes (ADR-081) on the real engine: the device rule's group branch joins the
// membership read-model to the group row under both tables' tenant clauses, and the alarm
// stamp is a text[] compared with `&&`. The schema comes from the real migration chain, so
// the appended access_scope_at column and the group-token ledger are exercised here too.
//
//	DC_IT_PGPORT=$PORT go test -tags integration -count=1 ./model/... -run Scope -v
package model

import (
	"context"
	"errors"
	"sort"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func (r scopeAlarmRig) region(t *testing.T, device, region string) {
	t.Helper()
	_, err := r.api.SetEntityAttribute(r.operator, &EntityAttributeSetRequest{
		EntityType: "device", Entity: device, Scope: string(AttributeScopeShared),
		AttrKey: "region", ValueType: string(AttributeValueString), Value: &region})
	require.NoError(t, err)
}

func (r scopeAlarmRig) deviceTokens(t *testing.T, ctx context.Context) []string {
	t.Helper()
	found, err := r.api.Devices(ctx, DeviceSearchCriteria{})
	require.NoError(t, err)
	out := []string{}
	for _, d := range found.Results {
		out = append(out, d.Token)
	}
	sort.Strings(out)
	return out
}

// TestAGroupMemberOnTheRealEngine: a member limited to an access-scope group sees the
// devices in it — and a device moved in by its attributes alone — and the alarms raised
// while a device was in it; another customer's device and its alarms stay out.
func TestAGroupMemberOnTheRealEngine(t *testing.T) {
	r := newScopeAlarmRig(t)
	r.region(t, "acme-pump", "north")
	r.region(t, "globex-pump", "south")
	dynamic, selector := "dynamic", `attr["region"] == "north"`
	_, err := r.api.CreateEntityGroup(r.operator, &EntityGroupCreateRequest{Token: "north",
		MemberType: "device", MembershipMode: &dynamic, Selector: &selector})
	require.NoError(t, err)
	_, err = r.api.PublishEntityGroup(r.operator, "north", nil, nil, "tester")
	require.NoError(t, err)
	member := r.member("group:north")

	require.Empty(t, r.deviceTokens(t, member), "a group that is not an access scope reached a device")
	_, err = r.api.SetEntityGroupAccessScope(r.operator, "north", true)
	require.NoError(t, err)
	require.Equal(t, []string{"acme-pump"}, r.deviceTokens(t, member))
	// Both branches at once, and the negative control.
	require.Equal(t, []string{"acme-pump", "globex-pump"}, r.deviceTokens(t, r.member("group:north", "customer:globex")))
	require.Equal(t, []string{"acme-pump", "globex-pump"}, r.deviceTokens(t, r.operator))

	t0 := time.Now().UTC().Truncate(time.Second)
	r.raise(t, "acme-pump", "acme-hot", t0)
	r.raise(t, "globex-pump", "globex-hot", t0)
	require.Equal(t, []string{"customer:acme", "group:north"}, []string(r.alarm(t, "acme-hot").ScopeAnchors))
	require.Equal(t, []string{"acme-hot"}, r.alarmKeys(t, member))

	// globex-pump moves into the region by its attribute alone: visible now, with no edit to
	// the member — but the alarm it raised before it was in the group stays out.
	r.region(t, "globex-pump", "north")
	require.Equal(t, []string{"acme-pump", "globex-pump"}, r.deviceTokens(t, member))
	require.Equal(t, []string{"acme-hot"}, r.alarmKeys(t, member))

	// A deleted group's token is retired.
	_, err = r.api.SetEntityGroupAccessScope(r.operator, "north", false)
	require.NoError(t, err)
	require.Empty(t, r.deviceTokens(t, member))
	deleted, err := r.api.DeleteEntityGroup(r.operator, "north")
	require.NoError(t, err)
	require.True(t, deleted)
	_, err = r.api.CreateEntityGroup(r.operator, &EntityGroupCreateRequest{Token: "north", MemberType: "device"})
	require.True(t, errors.Is(err, ErrGroupTokenRetired), "a create at a deleted group's token: %v", err)
}
