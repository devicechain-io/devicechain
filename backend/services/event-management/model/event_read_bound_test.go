// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBoundEventReadDeadlines(t *testing.T) {
	anchor := "area"
	scoped := auth.WithClaims(context.Background(), &auth.Claims{
		TokenType: auth.TokenTypeAccess, ScopeAnchors: []string{"customer:acme"},
	})
	for _, tt := range []struct {
		name   string
		ctx    context.Context
		anchor *string
		want   time.Duration
	}{
		{"unscoped without anchor", context.Background(), nil, eventReadTimeout},
		{"unscoped with anchor", context.Background(), &anchor, eventReadTimeout},
		{"scoped without anchor", scoped, nil, eventReadTimeout},
		{"scoped with anchor", scoped, &anchor, 15 * time.Second},
	} {
		t.Run(tt.name, func(t *testing.T) {
			start := time.Now()
			ctx, cancel, bound := boundEventRead(tt.ctx, tt.anchor)
			defer cancel()
			assert.Equal(t, tt.want, bound)
			deadline, ok := ctx.Deadline()
			require.True(t, ok)
			assert.False(t, deadline.Before(start.Add(bound)))
			assert.False(t, deadline.After(time.Now().Add(bound)))
		})
	}

	parent, cancel := context.WithTimeout(scoped, time.Second)
	defer cancel()
	ctx, cancelRead, _ := boundEventRead(parent, &anchor)
	defer cancelRead()
	want, _ := parent.Deadline()
	got, ok := ctx.Deadline()
	require.True(t, ok)
	assert.Equal(t, want, got, "a caller's earlier deadline must not be lengthened")
}

func TestBoundedReadError(t *testing.T) {
	old := eventReadTimeout
	eventReadTimeout = 0
	t.Cleanup(func() { eventReadTimeout = old })
	ctx, cancel, bound := boundEventRead(context.Background(), nil)
	defer cancel()
	<-ctx.Done()
	driverErr := errors.New("driver cancelled the query")
	err := boundedReadError(ctx, driverErr, bound)
	assert.ErrorIs(t, err, context.DeadlineExceeded)
	assert.Contains(t, err.Error(), "did not finish within "+bound.String())
	assert.Contains(t, err.Error(), "narrow the time range or filter on a device")
	assert.NoError(t, boundedReadError(ctx, nil, bound))
	assert.Same(t, driverErr, boundedReadError(context.Background(), driverErr, bound))

	eventReadTimeout = old
	parent, cancelParent := context.WithCancel(context.Background())
	ctx, cancelRead, bound := boundEventRead(parent, nil)
	defer cancelRead()
	cancelParent()
	assert.Same(t, context.Canceled, boundedReadError(ctx, context.Canceled, bound))
	assert.Same(t, driverErr, boundedReadError(ctx, driverErr, bound))

	parent, cancelDeadline := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancelDeadline()
	ctx, cancelEarlier, bound := boundEventRead(parent, nil)
	defer cancelEarlier()
	assert.True(t, context.DeadlineExceeded == boundedReadError(ctx, context.DeadlineExceeded, bound))
}
