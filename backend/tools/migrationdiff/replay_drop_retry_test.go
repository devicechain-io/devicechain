// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The error text a TimescaleDB background worker racing a DROP SCHEMA ... CASCADE produced in CI.
var errCatalogRace = errors.New(`ERROR: tuple concurrently deleted (SQLSTATE XX000)`)

func TestIsCatalogRaceIsNarrow(t *testing.T) {
	assert.True(t, isCatalogRace(errCatalogRace))
	assert.True(t, isCatalogRace(fmt.Errorf("wrapped: %w", errors.New(`ERROR: tuple concurrently updated (SQLSTATE XX000)`))))
	assert.False(t, isCatalogRace(nil))
	// Same SQLSTATE, different internal error: not ours to retry.
	assert.False(t, isCatalogRace(errors.New(`ERROR: cache lookup failed for relation 1 (SQLSTATE XX000)`)))
	// The phrase without the SQLSTATE is not the server's catalog error.
	assert.False(t, isCatalogRace(errors.New(`tuple concurrently deleted`)))
	assert.False(t, isCatalogRace(errors.New(`ERROR: cannot drop schema x because other objects depend on it (SQLSTATE 2BP01)`)))
}

// A drop that loses the race and then wins is retried, and each retry is logged. Before this,
// the first race error ended the run as "dropping schema ...".
func TestDropSchemaRetriesACatalogRace(t *testing.T) {
	calls, sleeps, logs := 0, 0, 0
	err := dropSchemaWithRetry(func(q string) error {
		calls++
		assert.Equal(t, `DROP SCHEMA IF EXISTS "event-management" CASCADE`, q)
		if calls <= 2 {
			return errCatalogRace
		}
		return nil
	}, "event-management", func(time.Duration) { sleeps++ }, func(string, ...any) { logs++ })
	require.NoError(t, err)
	assert.Equal(t, 3, calls)
	assert.Equal(t, 2, sleeps)
	assert.Equal(t, 2, logs, "every retry is logged")
}

// A real failure is never retried or masked.
func TestDropSchemaDoesNotRetryOtherErrors(t *testing.T) {
	calls := 0
	boom := errors.New("ERROR: permission denied for schema x (SQLSTATE 42501)")
	err := dropSchemaWithRetry(func(string) error { calls++; return boom }, "x",
		func(time.Duration) {}, func(string, ...any) {})
	assert.ErrorIs(t, err, boom)
	assert.Equal(t, 1, calls)
}

// A race that never clears is bounded and still reported, so the harness cannot spin or go green.
func TestDropSchemaGivesUpAfterBoundedRetries(t *testing.T) {
	calls := 0
	err := dropSchemaWithRetry(func(string) error { calls++; return errCatalogRace }, "x",
		func(time.Duration) {}, func(string, ...any) {})
	assert.ErrorIs(t, err, errCatalogRace)
	assert.Equal(t, dropAttempts, calls)
}
