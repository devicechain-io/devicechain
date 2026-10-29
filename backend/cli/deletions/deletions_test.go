// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package deletions

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type fakeClient struct {
	pages [][]Deletion // one slice per call, the last repeats
	total int32
	calls int
}

func (f *fakeClient) Query(_ context.Context, _ string, _ string, _ map[string]any, out any) error {
	i := f.calls
	if i >= len(f.pages) {
		i = len(f.pages) - 1
	}
	f.calls++
	resp := map[string]any{"tenantDeletions": map[string]any{
		"results": f.pages[i], "pagination": map[string]any{"totalRecords": f.total},
	}}
	b, _ := json.Marshal(resp)
	return json.Unmarshal(b, out)
}

func ptr(s string) *string { return &s }

func TestReachedIsFailClosed(t *testing.T) {
	require.False(t, Reached(nil, TargetErased))
	partial := &Deletion{Token: "a", Awaiting: "STORES", BlockedBy: []string{"tsdb: still holds rows"}}
	require.False(t, Reached(partial, TargetErased), "blocked store: not erased")
	// "nothing blocking" is not the claim: the server has not said erased.
	require.False(t, Reached(&Deletion{Token: "a", Awaiting: "SETTLE"}, TargetErased))
	erased := &Deletion{Token: "a", Erased: true, ErasedAt: ptr("2026-10-29T00:00:00Z"), Awaiting: "TOKEN_HOLD"}
	require.True(t, Reached(erased, TargetErased))
	require.False(t, Reached(erased, TargetComplete), "erased but the token is still held: not complete")
	require.True(t, Reached(&Deletion{Erased: true, CompletedAt: ptr("x")}, TargetComplete))
	// A contradictory record (erased flag with no timestamp) is not trusted.
	require.False(t, Reached(&Deletion{Erased: true}, TargetErased))
}

func TestFindWalksPagesAndAbsentIsNil(t *testing.T) {
	full := make([]Deletion, pageSize)
	for i := range full {
		full[i] = Deletion{Token: "other"}
	}
	c := &fakeClient{pages: [][]Deletion{full, {{Token: "acme"}}}, total: pageSize + 1}
	d, err := Find(context.Background(), c, "u", "acme")
	require.NoError(t, err)
	require.NotNil(t, d)
	require.Equal(t, 2, c.calls)

	c = &fakeClient{pages: [][]Deletion{{{Token: "other"}}}, total: 1}
	d, err = Find(context.Background(), c, "u", "acme")
	require.NoError(t, err)
	require.Nil(t, d)
}

func TestWaitRefusesAnAbsentRecordAndNeverSucceedsOnAPartialOne(t *testing.T) {
	c := &fakeClient{pages: [][]Deletion{{}}, total: 0}
	_, err := Wait(context.Background(), c, "u", "acme", TargetErased, time.Millisecond, nil)
	require.ErrorContains(t, err, "no deletion record")

	// Negative control: one store still holding rows, so the wait never succeeds.
	held := Deletion{Token: "acme", Awaiting: "STORES", BlockedBy: []string{"tsdb: holds rows"}}
	c = &fakeClient{pages: [][]Deletion{{held}}, total: 1}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	d, err := Wait(ctx, c, "u", "acme", TargetErased, 5*time.Millisecond, nil)
	require.Error(t, err)
	require.False(t, Reached(d, TargetErased))

	done := Deletion{Token: "acme", Erased: true, ErasedAt: ptr("2026-10-29T00:00:00Z")}
	c = &fakeClient{pages: [][]Deletion{{held}, {done}}, total: 1}
	d, err = Wait(context.Background(), c, "u", "acme", TargetErased, time.Millisecond, nil)
	require.NoError(t, err)
	require.True(t, Reached(d, TargetErased))
}

func TestPrintNamesTheState(t *testing.T) {
	var b bytes.Buffer
	Print(&b, &Deletion{Token: "acme", Epoch: "e", Awaiting: "STORES", BlockedBy: []string{"tsdb: holds"},
		Stores: []Store{{"rdb", true}, {"tsdb", false}}})
	require.Contains(t, b.String(), "NOT ERASED")
	require.Contains(t, b.String(), "NOT clean")
	b.Reset()
	Print(&b, &Deletion{Token: "acme", Erased: true, ErasedAt: ptr("t"), Awaiting: "TOKEN_HOLD"})
	require.Contains(t, b.String(), "ERASED (token still held)")
}
