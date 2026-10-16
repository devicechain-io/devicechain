// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package verify

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/devicechain-io/dc-command-delivery/model"
	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/stretchr/testify/require"
)

// scopedMember is a request from a member limited to Acme (ADR-081).
func scopedMember() context.Context {
	ctx := core.WithTenant(context.Background(), "tenant-a")
	return auth.WithClaims(ctx, &auth.Claims{TokenType: auth.TokenTypeAccess,
		Authorities: []string{string(auth.CommandWrite)}, ScopeAnchors: []string{"customer:acme"}})
}

func answering(t *testing.T, data map[string]any) string {
	t.Helper()
	dm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
	}))
	t.Cleanup(dm.Close)
	return dm.URL
}

// The validator returns the device's anchors beside its verdict — with a rejection too,
// since the caller decides a scoped member's right to the device before it relays any
// verdict — nil for a device the answer does not name, and fails closed on an answer that
// omits the list rather than reading it as "no such device".
func TestValidateEnqueueReturnsTheDeviceAnchors(t *testing.T) {
	ctx := core.WithTenant(context.Background(), "tenant-a")
	listing := []map[string]any{{"deviceToken": "dev-1", "anchors": []string{"customer:acme", "area:n"}}}

	v := NewEnqueueValidator(newSvcClient(t), answering(t, map[string]any{
		"validateCommandEnqueue": map[string]any{"allowed": true}, "deviceScopeAnchors": listing}))
	anchors, err := v.ValidateEnqueue(ctx, "dev-1", "reboot", nil)
	require.NoError(t, err)
	require.Equal(t, []string{"customer:acme", "area:n"}, anchors)

	v = NewEnqueueValidator(newSvcClient(t), answering(t, map[string]any{
		"validateCommandEnqueue": map[string]any{"allowed": false, "code": "COMMAND_NOT_IN_VOCABULARY", "reason": "no"},
		"deviceScopeAnchors":     listing}))
	anchors, err = v.ValidateEnqueue(ctx, "dev-1", "reboot", nil)
	var rejection *model.EnqueueRejected
	require.True(t, errors.As(err, &rejection))
	require.Equal(t, []string{"customer:acme", "area:n"}, anchors, "the anchors must travel with a rejection")

	v = NewEnqueueValidator(newSvcClient(t), answering(t, map[string]any{
		"validateCommandEnqueue": map[string]any{"allowed": true}, "deviceScopeAnchors": []any{}}))
	anchors, err = v.ValidateEnqueue(ctx, "dev-1", "reboot", nil)
	require.NoError(t, err)
	require.Nil(t, anchors, "a device the answer does not name has no anchors, not an empty set")

	v = NewEnqueueValidator(newSvcClient(t), answering(t, map[string]any{
		"validateCommandEnqueue": map[string]any{"allowed": true}}))
	_, err = v.ValidateEnqueue(ctx, "dev-1", "reboot", nil)
	require.Error(t, err, "an answer without the anchor list was read as a decision")
	require.False(t, errors.As(err, &rejection), "a broken answer is an availability failure, not a rejection")
}

// 🔴 Each of the three device-management calls an enqueue can make on a scoped member's
// behalf must say so to svcclient, which otherwise refuses — by design — to act for a
// scoped caller under the service's unscoped identity. Each marks THIS call only.
func TestServiceCallsServeAScopedMember(t *testing.T) {
	ctx := scopedMember()

	v := NewEnqueueValidator(newSvcClient(t), answering(t, map[string]any{
		"validateCommandEnqueue": map[string]any{"allowed": true},
		"deviceScopeAnchors":     []map[string]any{{"deviceToken": "dev-1", "anchors": []string{"customer:acme"}}}}))
	anchors, err := v.ValidateEnqueue(ctx, "dev-1", "reboot", nil)
	require.NoError(t, err, "the enqueue gate refused to serve a scoped member")
	require.Equal(t, []string{"customer:acme"}, anchors)

	l := NewDeviceAnchorLookup(newSvcClient(t), answering(t, map[string]any{
		"deviceScopeAnchors": []map[string]any{{"deviceToken": "dev-1", "anchors": []string{"customer:acme"}}}}))
	found, err := l.DeviceScopeAnchors(ctx, []string{"dev-1", "dev-2"})
	require.NoError(t, err, "the anchor lookup refused to serve a scoped member")
	require.Equal(t, map[string][]string{"dev-1": {"customer:acme"}}, found)

	s := NewWindowScheduler(newSvcClient(t), answering(t, map[string]any{
		"commandDispatchSchedule": map[string]any{"holds": []any{}, "refusals": []any{}}}))
	_, _, err = s.ScheduleDispatch(ctx, []string{"dev-1"}, "reboot", sql.NullTime{})
	require.NoError(t, err, "the window gate refused to serve a scoped member")
}

// The marker is a property of each call, not of the member's context: the other
// device-management reads this service makes are refused for a scoped caller, as svcclient
// intends. The batch validator is the one a scoped member could reach if its resolver ever
// opted in, and it must not inherit permission from its neighbours.
func TestUnmarkedServiceCallsStillRefuseAScopedMember(t *testing.T) {
	b := NewBatchValidator(newSvcClient(t), answering(t, map[string]any{"validateCommandEnqueueBatch": []any{}}))
	_, err := b.ValidateEnqueueBatch(scopedMember(), []string{"dev-1"}, "reboot", nil)
	require.Error(t, err)
	require.True(t, errors.Is(err, auth.ErrScopeUnsupported), "got %v", err)
}

// An anchor lookup that loses its list fails closed.
func TestDeviceAnchorLookupFailsClosedOnAMissingList(t *testing.T) {
	l := NewDeviceAnchorLookup(newSvcClient(t), answering(t, map[string]any{}))
	_, err := l.DeviceScopeAnchors(core.WithTenant(context.Background(), "tenant-a"), []string{"dev-1"})
	require.Error(t, err)
}
