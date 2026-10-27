// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"strings"
	"testing"

	dcgraphql "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The markers the ingest path writes: they authenticate the sender's own token, accumulate, and
// the auth-failure stamp is throttled.

func TestRecordIntegrationUndecodedAddsToTheTallyAndKeepsTheLatestSample(t *testing.T) {
	f := newIntFixture(t)
	created := f.create("chirp-1")

	require.NoError(t, f.api.RecordIntegrationUndecoded(f.ctx, "chirp-1", created.IssuedToken, 3, "aabb"))
	require.NoError(t, f.api.RecordIntegrationUndecoded(f.ctx, "chirp-1", created.IssuedToken, 2, "ccdd"))
	// No sample keeps the previous one.
	require.NoError(t, f.api.RecordIntegrationUndecoded(f.ctx, "chirp-1", created.IssuedToken, 1, ""))

	rows, err := f.api.IntegrationsByToken(f.ctx, []string{"chirp-1"})
	require.NoError(t, err)
	assert.EqualValues(t, 6, rows[0].UndecodedCount)
	assert.True(t, rows[0].LastUndecodedAt.Valid)
	assert.Equal(t, "ccdd", rows[0].LastUndecodedSample.String)
}

func TestRecordIntegrationUndecodedWritesNothingForAnyUnauthenticatedCaller(t *testing.T) {
	f := newIntFixture(t)
	created := f.create("chirp-1")

	assert.ErrorIs(t, f.api.RecordIntegrationUndecoded(f.ctx, "chirp-1", differentToken(created.IssuedToken), 1, "aa"), ErrIntegrationAuthFailed)
	assert.ErrorIs(t, f.api.RecordIntegrationUndecoded(f.ctx, "nope", created.IssuedToken, 1, "aa"), ErrIntegrationAuthFailed)
	_, err := f.api.UpdateIntegration(f.ctx, "chirp-1", &IntegrationUpdateRequest{Enabled: dcgraphql.OptionalBoolOf(false)})
	require.NoError(t, err)
	assert.ErrorIs(t, f.api.RecordIntegrationUndecoded(f.ctx, "chirp-1", created.IssuedToken, 1, "aa"), ErrIntegrationDisabled)

	rows, err := f.api.IntegrationsByToken(f.ctx, []string{"chirp-1"})
	require.NoError(t, err)
	assert.Zero(t, rows[0].UndecodedCount, "a refused caller wrote a marker")
	assert.False(t, rows[0].LastUndecodedAt.Valid)
}

func TestRecordIntegrationUndecodedRefusesBadInput(t *testing.T) {
	f := newIntFixture(t)
	created := f.create("chirp-1")
	assert.Error(t, f.api.RecordIntegrationUndecoded(f.ctx, "chirp-1", created.IssuedToken, 0, "aa"), "count below 1")
	assert.Error(t, f.api.RecordIntegrationUndecoded(f.ctx, "chirp-1", created.IssuedToken, 1, "not hex"))
	// An over-long sample is cut, not refused.
	require.NoError(t, f.api.RecordIntegrationUndecoded(f.ctx, "chirp-1", created.IssuedToken, 1, strings.Repeat("ab", 600)))
	rows, err := f.api.IntegrationsByToken(f.ctx, []string{"chirp-1"})
	require.NoError(t, err)
	assert.Len(t, rows[0].LastUndecodedSample.String, MaxUndecodedSampleHex)
}

func TestRecordIntegrationAuthFailureIsThrottledAndIgnoresUnknownTokens(t *testing.T) {
	f := newIntFixture(t)
	f.create("chirp-1")

	require.NoError(t, f.api.RecordIntegrationAuthFailure(f.ctx, "nope"), "an unknown integration is not an error")
	require.NoError(t, f.api.RecordIntegrationAuthFailure(f.ctx, "chirp-1"))
	rows, err := f.api.IntegrationsByToken(f.ctx, []string{"chirp-1"})
	require.NoError(t, err)
	require.True(t, rows[0].LastAuthFailureAt.Valid)
	first := rows[0].LastAuthFailureAt.Time

	require.NoError(t, f.api.RecordIntegrationAuthFailure(f.ctx, "chirp-1"))
	rows, err = f.api.IntegrationsByToken(f.ctx, []string{"chirp-1"})
	require.NoError(t, err)
	assert.True(t, first.Equal(rows[0].LastAuthFailureAt.Time), "a second failure inside the interval rewrote the stamp")
}
