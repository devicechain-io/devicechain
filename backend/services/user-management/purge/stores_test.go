// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package purge

import (
	"context"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/tenantpurge"
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
	for _, s := range DefaultStores(nil, nil, nil, "inst", nil, nil) {
		names = append(names, s.Name())
	}
	require.Equal(t, []string{
		StoreRelational, StoreTelemetry, StoreBroker, StoreKeyValue, StoreDetect, StoreObject,
	}, names, "changing the set of stores a purge asks about is a deliberate act; "+
		"dropping one releases the token over whatever it held")
}

// TestExternalTablesNameARegisteredStore closes the ClassExternal contract from the side
// that owns the store set.
//
// 🔴 THIS IS THE ONLY CHECK THERE IS, and without it "external" is strictly worse than
// "deferred". A deferred table blocks completion, so forgetting it is loud. An external
// table does NOT: the sweep skips it on the strength of a store name in a registry that
// core cannot cross-check, because core cannot import a service. So a store renamed,
// dropped from DefaultStores, or never written leaves the table classified as handled and
// every purge completing — the registry saying one thing and the coordinator doing
// another, with no error anywhere in between.
//
// It is deliberately asserted over DefaultStores rather than over the const block: a name
// that exists as a constant but is not in the set is the exact failure being guarded
// against.
func TestExternalTablesNameARegisteredStore(t *testing.T) {
	registered := map[string]bool{}
	for _, s := range DefaultStores(nil, nil, nil, "inst", nil, nil) {
		registered[s.Name()] = true
	}
	claimed := tenantpurge.ExternalStores()
	require.NotEmpty(t, claimed, "no table claims to be erased by a store, so this test "+
		"asserted nothing — remove it with the last external entry rather than leaving it green")
	for _, name := range claimed {
		require.Truef(t, registered[name], "the purge classifier skips a table because store %q "+
			"erases it, but no such store is registered — the sweep passes over the table and "+
			"nothing else touches it", name)
	}
}

// TestEveryPendingStoreSaysWhatItHolds pins the part that goes into the deletion record.
//
// A Pending store's whole value is the sentence it contributes. One with an empty or
// perfunctory reason still blocks completion, so nothing would fail — but the ledger
// would say a purge is incomplete without saying what is retained, which is the report an
// operator or an auditor actually reads.
func TestEveryPendingStoreSaysWhatItHolds(t *testing.T) {
	for _, s := range DefaultStores(nil, nil, nil, "inst", nil, nil) {
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

// TestANoteDoesNotBlockCompletion is the invariant the whole note mechanism rests on, and it
// is named in Outcome's own doc comment as the thing to read before adding a note anywhere.
//
// Notes and deferrals are both free text on a store's outcome, which makes them very easy to
// confuse — and confusing them is not a cosmetic error in either direction. A deferral
// written as a note lets a purge complete over data that is still there, which is a false
// erasure claim. A note written as a deferral blocks every purge on the instance forever.
func TestANoteDoesNotBlockCompletion(t *testing.T) {
	noted := Outcome{Rows: 3, Notes: []string{"the exempted buckets were not scanned"}}
	require.True(t, noted.Clean(), "a note qualifies a clean pass; it must never hold one open")
	require.Equal(t, "the exempted buckets were not scanned", noted.Note())
	require.Empty(t, noted.Reason(), "a note must not leak into the deferral text")

	deferred := Outcome{Deferred: []string{"the checkpoint still holds this tenant's windows"}}
	require.False(t, deferred.Clean(), "a deferral must still block — this is the control")
	require.Empty(t, deferred.Note(), "a deferral must not leak into the note text")

	both := Outcome{
		Notes:    []string{"skipped the exempted buckets"},
		Deferred: []string{"the checkpoint still holds this tenant's windows"},
	}
	require.False(t, both.Clean(), "a note alongside a deferral must not soften it")
	require.NotEqual(t, both.Note(), both.Reason())
}

// TestNoteJoinsMultipleEntriesTheSameWayReasonDoes pins the separator.
//
// It is not fussiness: the key-value store is the only producer of MULTIPLE notes, and its
// three entries are multi-sentence paragraphs. Joined with an empty string they run together
// into one garbled block in the durable record, on every pass, and nothing else in the suite
// would notice — Reason() has this assertion and Note() did not.
func TestNoteJoinsMultipleEntriesTheSameWayReasonDoes(t *testing.T) {
	out := Outcome{Notes: []string{"the first thing", "the second thing"}}
	require.Equal(t, "the first thing; the second thing", out.Note())

	same := Outcome{Deferred: []string{"the first thing", "the second thing"}}
	require.Equal(t, same.Reason(), out.Note(),
		"a reader moves between these two fields; they must not be punctuated differently")
}
