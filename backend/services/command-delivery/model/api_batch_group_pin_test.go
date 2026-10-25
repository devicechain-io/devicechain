// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/stretchr/testify/require"
)

// publishingGroup is a dynamic group republished mid-walk: it serves version 1 until the first
// page has been handed out, after which the group's ACTIVE version is 2. A page asked for a
// named version is served at that version (device-management serves any existing frozen
// version); an unpinned page is served at the active one.
type publishingGroup struct {
	mu       sync.Mutex
	active   int32
	requests []*int32 // the version each call asked for, nil when unpinned
	// wrongPin makes a pinned request answer at the active version anyway (a backend that
	// ignores the pin), to prove the caller refuses the splice.
	wrongPin bool
}

func (g *publishingGroup) ResolveGroupTargets(_ context.Context, _ string, version *int32,
	after string, _ int) (*GroupTargetPage, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if version == nil {
		g.requests = append(g.requests, nil)
	} else {
		v := *version
		g.requests = append(g.requests, &v)
	}
	served := g.active
	if version != nil && !g.wrongPin {
		served = *version
	}
	if after == "" {
		first := served
		g.active = 2 // a publish lands between the first and the second page
		return &GroupTargetPage{DeviceTokens: []string{"v1-first"}, NextCursor: "1000", ResolvedVersion: &first}, nil
	}
	tok := "v1-last"
	if served == 2 {
		tok = "v2-unintended"
	}
	return &GroupTargetPage{DeviceTokens: []string{tok}, ResolvedVersion: &served}, nil
}

func TestGroupWalkPinsLaterPagesToTheFirstVersion(t *testing.T) {
	api := newBatchTestApi(t)
	api.BatchValidator = &countingValidator{}
	g := &publishingGroup{active: 1}
	api.GroupTargetResolver = g

	targets, err := api.resolveGroupTargets(core.WithTenant(context.Background(), "acme"), groupRequest("b", "fleet"))
	require.NoError(t, err)
	require.Equal(t, []string{"v1-first", "v1-last"}, targets.deviceTokens)
	require.True(t, targets.groupVersion.Valid)
	require.Equal(t, int32(1), targets.groupVersion.Int32)
	require.Len(t, g.requests, 2)
	require.Nil(t, g.requests[0], "the first page is unpinned: it is what chooses the version")
	require.NotNil(t, g.requests[1], "a later page must be pinned")
	require.Equal(t, int32(1), *g.requests[1])
}

func TestGroupWalkRefusesAPageAtAnotherVersion(t *testing.T) {
	api := newBatchTestApi(t)
	api.BatchValidator = &countingValidator{}
	api.GroupTargetResolver = &publishingGroup{active: 1, wrongPin: true}

	_, err := api.resolveGroupTargets(core.WithTenant(context.Background(), "acme"), groupRequest("b", "fleet"))
	var refusal *EnqueueRejected
	require.True(t, errors.As(err, &refusal), "want a decided rejection, got %v", err)
	require.Equal(t, RejectGroupUnusable, refusal.Code)
	require.True(t, strings.Contains(refusal.Reason, "changed version"), refusal.Reason)
}

// recordingResolver wraps a resolver and records the version each call asked for.
type recordingResolver struct {
	inner GroupTargetResolver
	seen  []*int32
}

func (r *recordingResolver) ResolveGroupTargets(ctx context.Context, g string, v *int32,
	after string, limit int) (*GroupTargetPage, error) {
	r.seen = append(r.seen, v)
	return r.inner.ResolveGroupTargets(ctx, g, v, after, limit)
}

// A static group has no version: nothing is pinned, because the owner refuses a version on a
// static group.
func TestGroupWalkDoesNotPinAStaticGroup(t *testing.T) {
	api := newBatchTestApi(t)
	api.BatchValidator = &countingValidator{}
	rec := &recordingResolver{inner: &pagingResolver{members: []string{"a", "b", "c", "d", "e"}, shortPages: 2}}
	api.GroupTargetResolver = rec

	targets, err := api.resolveGroupTargets(core.WithTenant(context.Background(), "acme"), groupRequest("b", "fleet"))
	require.NoError(t, err)
	require.Len(t, targets.deviceTokens, 5)
	require.False(t, targets.groupVersion.Valid)
	require.Greater(t, len(rec.seen), 1)
	for _, v := range rec.seen {
		require.Nil(t, v)
	}
}

// A caller-supplied version is honoured on every page.
func TestGroupWalkPassesACallerVersionOnEveryPage(t *testing.T) {
	api := newBatchTestApi(t)
	api.BatchValidator = &countingValidator{}
	version := int32(7)
	rec := &recordingResolver{inner: &pagingResolver{members: []string{"a", "b", "c", "d", "e"}, shortPages: 2, version: &version}}
	api.GroupTargetResolver = rec

	request := groupRequest("b", "fleet")
	request.GroupVersion = &version
	targets, err := api.resolveGroupTargets(core.WithTenant(context.Background(), "acme"), request)
	require.NoError(t, err)
	require.Len(t, targets.deviceTokens, 5)
	require.Equal(t, int32(7), targets.groupVersion.Int32)
	require.Greater(t, len(rec.seen), 1)
	for _, v := range rec.seen {
		require.NotNil(t, v)
		require.Equal(t, int32(7), *v)
	}
}

// Through the front door: a batch fired at a group that is republished mid-walk targets the
// first version's members and records that version.
func TestBatchAtARepublishedGroupRecordsTheVersionItWasResolvedAt(t *testing.T) {
	api := newBatchTestApi(t)
	ctx := core.WithTenant(context.Background(), "acme")
	api.BatchValidator = &countingValidator{}
	api.GroupTargetResolver = &publishingGroup{active: 1}

	batch, err := api.CreateCommandBatch(ctx, groupRequest("nightly", "fleet"))
	require.NoError(t, err)
	require.True(t, batch.GroupVersion.Valid)
	require.Equal(t, int32(1), batch.GroupVersion.Int32)
	var tokens []string
	for _, c := range commandsOf(t, api, ctx, "nightly") {
		tokens = append(tokens, c.DeviceToken)
	}
	require.ElementsMatch(t, []string{"v1-first", "v1-last"}, tokens)
}
