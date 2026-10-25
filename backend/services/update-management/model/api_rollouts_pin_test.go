// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

// publishingGroup is a dynamic group that is republished mid-walk: it serves version 1 until the
// first page has been handed out, after which the group's ACTIVE version is 2. A page asked for
// a named version is served at that version (device-management serves any existing frozen
// version); an unpinned page is served at the active one.
type publishingGroup struct {
	mu       sync.Mutex
	active   int32
	requests []*int32 // the version each call asked for, nil when unpinned
	// wrongPin makes a pinned request answer at the active version anyway (a backend that
	// ignores the pin), to prove the caller refuses the splice.
	wrongPin bool
}

func (g *publishingGroup) ResolveGroupTargets(_ context.Context, _ string, version *int32, after string, _ int) (*GroupTargetPage, error) {
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
		first := g.active
		g.active = 2 // a publish lands between the first and the second page
		return &GroupTargetPage{DeviceTokens: []string{"v1-first"}, NextCursor: "1000", ResolvedVersion: &first}, nil
	}
	tok := "v1-last"
	if served == 2 {
		tok = "v2-unintended"
	}
	return &GroupTargetPage{DeviceTokens: []string{tok}, ResolvedVersion: &served}, nil
}

func TestResolveRolloutGroupPinsLaterPagesToTheFirstVersion(t *testing.T) {
	g := &publishingGroup{active: 1}
	api := &Api{Groups: g}
	devices, version, err := api.resolveRolloutGroup(context.Background(), "fleet")
	require.NoError(t, err)
	require.Equal(t, []string{"v1-first", "v1-last"}, devices)
	require.NotNil(t, version)
	require.Equal(t, int32(1), *version)
	require.Len(t, g.requests, 2)
	require.Nil(t, g.requests[0], "the first page is unpinned: it is what chooses the version")
	require.NotNil(t, g.requests[1], "a later page must be pinned")
	require.Equal(t, int32(1), *g.requests[1])
}

func TestResolveRolloutGroupRefusesAPageAtAnotherVersion(t *testing.T) {
	g := &publishingGroup{active: 1, wrongPin: true}
	api := &Api{Groups: g}
	_, _, err := api.resolveRolloutGroup(context.Background(), "fleet")
	require.Error(t, err)
	require.Contains(t, err.Error(), "changed version")
}

// A static group has no version: nothing is pinned, because device-management refuses a version
// on a static group.
func TestResolveRolloutGroupDoesNotPinAStaticGroup(t *testing.T) {
	e := newRolloutEnv(t, 5)
	e.groups.pageSize = 2
	var seen []*int32
	api := &Api{Groups: recordingGroups{inner: e.groups, seen: &seen}}
	devices, version, err := api.resolveRolloutGroup(tenantCtx("acme"), "fleet")
	require.NoError(t, err)
	require.Len(t, devices, 5)
	require.Nil(t, version)
	require.Greater(t, len(seen), 1)
	for _, v := range seen {
		require.Nil(t, v)
	}
}

type recordingGroups struct {
	inner GroupResolver
	seen  *[]*int32
}

func (r recordingGroups) ResolveGroupTargets(ctx context.Context, g string, v *int32, after string, limit int) (*GroupTargetPage, error) {
	*r.seen = append(*r.seen, v)
	return r.inner.ResolveGroupTargets(ctx, g, v, after, limit)
}

// Through the front door: starting a rollout over a group that is republished mid-walk freezes
// the first version's members and records that version on the rollout.
func TestStartRolloutRecordsTheVersionTheTargetsWereResolvedAt(t *testing.T) {
	e := newRolloutEnv(t, 3)
	e.api.Groups = &publishingGroup{active: 1}
	e.create(t, "r1", 10, nil)
	v, err := e.api.StartRollout(tenantCtx("acme"), "r1")
	require.NoError(t, err)
	require.True(t, v.Rollout.GroupVersion.Valid)
	require.Equal(t, int32(1), v.Rollout.GroupVersion.Int32)
	got := e.targets(t, "r1", nil)
	var tokens []string
	for _, tv := range got {
		tokens = append(tokens, tv.Target.DeviceToken)
	}
	require.ElementsMatch(t, []string{"v1-first", "v1-last"}, tokens)
}
