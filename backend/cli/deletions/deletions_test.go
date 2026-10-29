// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package deletions

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// routedClient answers the three admin queries dcctl makes from fixed data.
type routedClient struct {
	tenants []tenantRow
	records []Deletion
}

func (r *routedClient) Query(_ context.Context, _ string, q string, vars map[string]any, out any) error {
	var resp map[string]any
	switch {
	case strings.Contains(q, "tenantDeletions("):
		var done []Deletion
		for _, d := range r.records {
			if d.CompletedAt != nil {
				done = append(done, d)
			}
		}
		resp = map[string]any{"tenantDeletions": map[string]any{
			"results": done, "pagination": map[string]any{"totalRecords": len(done)}}}
	case strings.Contains(q, "tenantDeletion("):
		var found any
		for _, d := range r.records {
			if d.Token == vars["token"] && d.Epoch == vars["epoch"] {
				d := d
				found = d
				break
			}
		}
		resp = map[string]any{"tenantDeletion": found}
	case strings.Contains(q, "tenants"):
		resp = map[string]any{"tenants": r.tenants}
	default:
		panic("unexpected query: " + q)
	}
	b, _ := json.Marshal(resp)
	return json.Unmarshal(b, out)
}

// A completed record for acme (the PREVIOUS tenant at that token) plus a NEW purging acme
// whose own record does not exist yet must never read as erased or complete.
func TestAPredecessorsCompletedRecordIsNotTheNewTenantsErasure(t *testing.T) {
	newEpoch := "2026-10-29T12:00:00.5Z"
	c := &routedClient{
		tenants: []tenantRow{{Token: "acme", PurgeState: "purging", PurgeEpoch: &newEpoch}},
		records: []Deletion{{Token: "acme", Epoch: "2026-09-01T00:00:00Z", Erased: true,
			ErasedAt: ptr("2026-09-01T00:06:00Z"), CompletedAt: ptr("2026-09-01T12:07:00Z")}},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Millisecond)
	defer cancel()
	for _, target := range []Target{TargetErased, TargetComplete} {
		d, err := Wait(ctx, c, "u", "acme", "", target, 5*time.Millisecond, nil)
		require.Error(t, err, "target %s must not be reached by a predecessor's record", target)
		require.False(t, Reached(d, target))
	}
}

func ptr(s string) *string { return &s }

func TestReachedIsFailClosed(t *testing.T) {
	require.False(t, Reached(nil, TargetErased))
	partial := &Deletion{Token: "a", Awaiting: "STORES", BlockedBy: []string{"tsdb: still holds rows"}}
	require.False(t, Reached(partial, TargetErased), "blocked store: not erased")
	require.False(t, Reached(&Deletion{Token: "a", Awaiting: "SETTLE"}, TargetErased), "nothing blocking is not the claim")
	erased := &Deletion{Token: "a", Erased: true, ErasedAt: ptr("2026-10-29T00:00:00Z"), Awaiting: "TOKEN_HOLD"}
	require.True(t, Reached(erased, TargetErased))
	require.False(t, Reached(erased, TargetComplete), "erased but the token is still held: not complete")
	require.True(t, Reached(&Deletion{Erased: true, CompletedAt: ptr("x")}, TargetComplete))
	require.False(t, Reached(&Deletion{Erased: true}, TargetErased), "an erased flag without its timestamp is not trusted")
}

const e1 = "2026-10-29T12:00:00.5Z"

func purgingAcme() []tenantRow {
	e := e1
	return []tenantRow{{Token: "acme", PurgeState: "purging", PurgeEpoch: &e}}
}

// The new tenant's own record is what gets read, not the predecessor's.
func TestAPurgingTenantIsBoundToItsOwnEpoch(t *testing.T) {
	c := &routedClient{
		tenants: purgingAcme(),
		records: []Deletion{
			{Token: "acme", Epoch: "2026-09-01T00:00:00Z", Erased: true, ErasedAt: ptr("x"), CompletedAt: ptr("y")},
			{Token: "acme", Epoch: e1, Awaiting: "STORES", BlockedBy: []string{"tsdb: holds rows"}},
		},
	}
	d, err := Lookup(context.Background(), c, "u", "acme", "")
	require.NoError(t, err)
	require.NotNil(t, d)
	require.Equal(t, e1, d.Epoch)
	require.False(t, Reached(d, TargetErased))

	c.records[1] = Deletion{Token: "acme", Epoch: e1, Erased: true, ErasedAt: ptr("z"), Awaiting: "TOKEN_HOLD"}
	d, err = Lookup(context.Background(), c, "u", "acme", "")
	require.NoError(t, err)
	require.True(t, Reached(d, TargetErased))
	require.False(t, Reached(d, TargetComplete))
}

func TestAnActiveTenantIsNotBeingDeletedWhateverItsPredecessorDid(t *testing.T) {
	c := &routedClient{
		tenants: []tenantRow{{Token: "acme", PurgeState: "active"}},
		records: []Deletion{{Token: "acme", Epoch: "2026-09-01T00:00:00Z", Erased: true, ErasedAt: ptr("x"), CompletedAt: ptr("y")}},
	}
	_, err := Lookup(context.Background(), c, "u", "acme", "")
	require.ErrorIs(t, err, ErrNotDeleting)
}

func TestAGoneTenantReportsItsNewestCompletedDeletionWithItsEpoch(t *testing.T) {
	c := &routedClient{records: []Deletion{
		{Token: "acme", Epoch: "2026-09-01T00:00:00Z", Erased: true, ErasedAt: ptr("x"), CompletedAt: ptr("y")},
	}}
	d, err := Lookup(context.Background(), c, "u", "acme", "")
	require.NoError(t, err)
	require.Equal(t, "2026-09-01T00:00:00Z", d.Epoch)
	require.True(t, Reached(d, TargetComplete))

	d, err = Lookup(context.Background(), c, "u", "nobody", "")
	require.NoError(t, err)
	require.Nil(t, d)
}

func TestAnExplicitEpochBindsToExactlyThatDeletion(t *testing.T) {
	c := &routedClient{
		tenants: purgingAcme(),
		records: []Deletion{
			{Token: "acme", Epoch: "2026-09-01T00:00:00Z", Erased: true, ErasedAt: ptr("x"), CompletedAt: ptr("y")},
			{Token: "acme", Epoch: e1},
		},
	}
	d, err := Lookup(context.Background(), c, "u", "acme", "2026-09-01T00:00:00Z")
	require.NoError(t, err)
	require.Equal(t, "2026-09-01T00:00:00Z", d.Epoch)
	d, err = Lookup(context.Background(), c, "u", "acme", "2030-01-01T00:00:00Z")
	require.NoError(t, err)
	require.Nil(t, d)
}

func TestWaitNeverSucceedsOnAPartialOneAndRefusesAnAbsentRecord(t *testing.T) {
	// Purging tenant whose record is missing: an error, never an older record.
	c := &routedClient{tenants: purgingAcme()}
	_, err := Wait(context.Background(), c, "u", "acme", "", TargetErased, time.Millisecond, nil)
	require.ErrorContains(t, err, "no deletion record")

	held := Deletion{Token: "acme", Epoch: e1, Awaiting: "STORES", BlockedBy: []string{"tsdb: holds rows"}}
	c = &routedClient{tenants: purgingAcme(), records: []Deletion{held}}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	d, err := Wait(ctx, c, "u", "acme", "", TargetErased, 5*time.Millisecond, nil)
	require.Error(t, err)
	require.False(t, Reached(d, TargetErased))
}

func TestPrintShowsNotesVerificationAndTheFinalityCaveat(t *testing.T) {
	var b bytes.Buffer
	Print(&b, &Deletion{Token: "acme", Epoch: e1, Erased: true, ErasedAt: ptr("t0"), VerifiedAt: ptr("t1"),
		Awaiting: "TOKEN_HOLD", Stores: []Store{{Store: "tsdb", Complete: true, Note: ptr("telemetry database does not exist")}}})
	out := b.String()
	require.Contains(t, out, "ERASED (token still held")
	require.Contains(t, out, "last verified: t1")
	require.Contains(t, out, "note: telemetry database does not exist")
	b.Reset()
	Print(&b, &Deletion{Token: "acme", Epoch: e1, Awaiting: "STORES", BlockedBy: []string{"tsdb: holds"},
		Stores: []Store{{Store: "rdb", Complete: true}, {Store: "tsdb"}}})
	require.Contains(t, b.String(), "NOT ERASED")
	require.Contains(t, b.String(), "NOT clean")
}
