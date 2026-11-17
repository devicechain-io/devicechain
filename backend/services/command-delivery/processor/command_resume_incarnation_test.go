// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/devicechain-io/dc-command-delivery/model"
	"github.com/devicechain-io/dc-command-delivery/verify"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type resumeIdentitySource struct {
	fakeSource
	lookup func() (map[string]verify.DeviceIdentity, error)
}

func (s *resumeIdentitySource) DeviceIncarnations(context.Context, string, []string) (map[string]verify.DeviceIdentity, error) {
	return s.lookup()
}

type resumePageAPI struct {
	*model.Api
	rows       []*model.Command
	beforeRead func(uint)
	released   []uint
	cancelled  []uint
	queuedRead bool
}

func (a *resumePageAPI) HeldCommandsForDevice(_ context.Context, _ string, cursor uint, limit int) ([]*model.Command, error) {
	if a.beforeRead != nil {
		a.beforeRead(cursor)
	}
	var page []*model.Command
	for _, cmd := range a.rows {
		if cmd.ID > cursor && len(page) < limit {
			page = append(page, cmd)
		}
	}
	return page, nil
}

func (a *resumePageAPI) ReleaseHold(_ context.Context, id uint) (bool, error) {
	a.released = append(a.released, id)
	return false, nil
}

func (a *resumePageAPI) CancelHeldCommand(_ context.Context, id uint) (bool, error) {
	a.cancelled = append(a.cancelled, id)
	return true, nil
}

func (a *resumePageAPI) QueuedCommandsForDevice(context.Context, string, int) ([]*model.Command, error) {
	a.queuedRead = true
	return nil, nil
}

func resumeHeldCommand(id uint, incarnation int64) *model.Command {
	cmd := &model.Command{DeviceToken: "dev", DeviceIncarnation: sql.NullInt64{Int64: incarnation, Valid: true}}
	cmd.ID = id
	return cmd
}

func TestResumeDeviceDoesNotJudgeCommandsInsertedDuringIdentityLookup(t *testing.T) {
	api := &resumePageAPI{}
	src := &resumeIdentitySource{lookup: func() (map[string]verify.DeviceIdentity, error) {
		// The identity read sees the old device. Before it returns, the device is
		// re-created and a command for its new incarnation is held at the same token.
		api.rows = append(api.rows, resumeHeldCommand(1, 23))
		return map[string]verify.DeviceIdentity{"dev": {ID: 17}}, nil
	}}
	proc := &CommandDeliveryProcessor{Api: api, DisabledSource: src}
	require.NoError(t, proc.ResumeDevice(context.Background(), "acme", "dev"))
	require.Len(t, api.rows, 1, "the concurrent insertion must have happened")
	assert.Empty(t, api.cancelled, "a command inserted after the page snapshot must stay held")
	assert.Empty(t, api.released, "a command outside the verified page must not be released")
}

func TestResumeDeviceRefreshesIdentityAfterLaterPages(t *testing.T) {
	api := &resumePageAPI{}
	for id := uint(1); id <= reconcilePageSize; id++ {
		api.rows = append(api.rows, resumeHeldCommand(id, 17))
	}
	recreated := false
	api.beforeRead = func(cursor uint) {
		if cursor == reconcilePageSize {
			recreated = true
			api.rows = append(api.rows, resumeHeldCommand(reconcilePageSize+1, 23))
		}
	}
	lookups := 0
	src := &resumeIdentitySource{lookup: func() (map[string]verify.DeviceIdentity, error) {
		lookups++
		if recreated {
			// The new device cannot yet be verified. Neither the cached old identity
			// nor an unsuccessful fresh lookup authorizes cancelling or releasing it.
			return nil, errors.New("device-management unavailable")
		}
		return map[string]verify.DeviceIdentity{"dev": {ID: 17}}, nil
	}}
	proc := &CommandDeliveryProcessor{Api: api, DisabledSource: src}
	require.NoError(t, proc.ResumeDevice(context.Background(), "acme", "dev"))
	require.True(t, recreated, "the walk must reach the second page")
	assert.Equal(t, 2, lookups)
	assert.Empty(t, api.cancelled, "the new incarnation must not be cancelled using the first page's identity")
	assert.Len(t, api.released, reconcilePageSize)
	assert.NotContains(t, api.released, uint(reconcilePageSize+1), "an unverified command must stay held")
	assert.False(t, api.queuedRead, "identity failure must return before reading the dispatch backlog")
}
