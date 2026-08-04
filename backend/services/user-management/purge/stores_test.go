// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package purge

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestTheStoreSetIsTheCoverageClaim pins which stores a purge asks about.
//
// 🔴 BE CLEAR ABOUT WHAT THIS TEST CAN AND CANNOT DO. It cannot tell anyone that the
// platform grew a sixth place to keep tenant data — nothing here derives the storage
// systems from the deployment. What it CAN do is make removing a registration a failing
// test rather than a silent change, which is the failure mode DefaultStores describes.
//
// The relational side has a real derivation and it lives elsewhere — the coverage gate in
// hack/migration-diff.sh classifies every table in every schema against the catalog and
// fails on one it cannot explain. There is no equivalent for "is there a storage system
// with no store", and this test is the honest stand-in, not a replacement.
func TestTheStoreSetIsTheCoverageClaim(t *testing.T) {
	var names []string
	for _, s := range DefaultStores(nil, nil, nil, "inst") {
		names = append(names, s.Name())
	}
	require.Equal(t, []string{
		StoreRelational, StoreTelemetry, StoreBroker, StoreKeyValue, StoreObject,
	}, names, "changing the set of stores a purge asks about is a deliberate act; "+
		"dropping one releases the token over whatever it held")
}

// TestEveryPendingStoreSaysWhatItHolds pins the part that goes into the deletion record.
//
// A Pending store's whole value is the sentence it contributes. One with an empty or
// perfunctory reason still blocks completion, so nothing would fail — but the ledger
// would say a purge is incomplete without saying what is retained, which is the report an
// operator or an auditor actually reads.
func TestEveryPendingStoreSaysWhatItHolds(t *testing.T) {
	for _, s := range DefaultStores(nil, nil, nil, "inst") {
		p, ok := s.(Pending)
		if !ok {
			continue
		}
		require.NotEmpty(t, p.Holds, "%s must name the tenant data it retains", p.Name())
		require.Greater(t, len(p.Holds), 40,
			"%s's reason must describe the data, not label it", p.Name())
	}
}

// TestAPendingStoreIsNeverClean is the property the whole design leans on: a store that
// has no erasure yet must never be able to satisfy completion.
func TestAPendingStoreIsNeverClean(t *testing.T) {
	p := Pending{StoreName: "tsdb", Holds: "the event store still holds this tenant's telemetry"}
	out, err := p.Erase(context.Background(), "acme", time.Now())
	require.NoError(t, err, "a pending store is a stated gap, not a transient failure")
	require.False(t, out.Clean())
	require.Equal(t, "the event store still holds this tenant's telemetry", out.Reason())
	require.Zero(t, out.Rows)
}

// TestAnOutcomeIsCleanOnlyWithNothingDeferred covers the predicate completion turns on.
func TestAnOutcomeIsCleanOnlyWithNothingDeferred(t *testing.T) {
	require.True(t, Outcome{Rows: 100}.Clean())
	require.False(t, Outcome{Deferred: []string{"something"}}.Clean())
	require.Equal(t, "a; b", Outcome{Deferred: []string{"a", "b"}}.Reason())
}
