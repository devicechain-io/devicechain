// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build integration

package model

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/entityevent"
	"github.com/stretchr/testify/require"
)

type lockedDeletionPublisher struct {
	tenant  string
	entered chan struct{}
	release chan struct{}
	once    sync.Once
	mu      sync.Mutex
	count   int
}

func (p *lockedDeletionPublisher) PublishEntityDeleted(ctx context.Context, _ *entityevent.EntityDeletedEvent) error {
	tenant, _ := core.TenantFromContext(ctx)
	if tenant != p.tenant {
		return nil
	}
	p.mu.Lock()
	p.count++
	p.mu.Unlock()
	p.once.Do(func() { close(p.entered) })
	<-p.release
	return nil
}

// Pins duplicate publication by concurrent replica drains on Postgres, which SQLite cannot
// exercise: one advisory-lock holder publishes the fact, and the other skips. The fact carries
// the time this process stamped at the delete.
func TestTheDeletionOutboxSerializesDrainsOnTheRealEngine(t *testing.T) {
	api, ctx, _ := ownershipPgRig(t, 1)
	stamp := time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC)
	api.ownershipNow = func() time.Time { return stamp }
	deleted, err := api.DeleteDevice(ctx, "pump-00000")
	require.NoError(t, err)
	require.True(t, deleted)
	rows := deletionRows(t, api, ctx)
	require.Len(t, rows, 1)
	require.True(t, rows[0].DeletedTime.Equal(stamp), "the fact is not dated by the delete")
	pub := &lockedDeletionPublisher{tenant: tenantOf(ctx), entered: make(chan struct{}), release: make(chan struct{})}
	api.EntityDeletedPublisher = pub
	var release sync.Once
	unblock := func() { release.Do(func() { close(pub.release) }) }
	defer unblock()
	first := make(chan error, 1)
	go func() { first <- api.DrainEntityDeletionsLocked(context.Background()) }()
	select {
	case <-pub.entered:
	case err := <-first:
		t.Fatalf("first drain ended before publishing: %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("drain never entered publisher")
	}
	second := make(chan error, 1)
	go func() { second <- api.DrainEntityDeletionsLocked(context.Background()) }()
	select {
	case err := <-second:
		require.ErrorIs(t, err, core.ErrPassSkipped)
	case <-time.After(10 * time.Second):
		t.Fatal("second drain waited instead of skipping")
	}
	unblock()
	select {
	case err := <-first:
		require.NoError(t, err)
	case <-time.After(10 * time.Second):
		t.Fatal("first drain failed to finish")
	}
	pub.mu.Lock()
	count := pub.count
	pub.mu.Unlock()
	require.Equal(t, 1, count)
	require.Empty(t, deletionRows(t, api, ctx))
}
