// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package partition

import (
	"errors"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRouterRefusesBeforeAPlacement(t *testing.T) {
	r := NewRouter("inst")
	_, err := r.ShardGroup("a")
	require.ErrorIs(t, err, ErrNoPlacement)
}

func TestRouterAgreesWithTheTable(t *testing.T) {
	tbl := NewTable(DefaultP)
	for i := range tbl.Groups {
		tbl.Groups[i] = i % 3
	}
	tbl.Overrides = map[string]int{"big": 5}
	tbl.Generation = 2
	r := NewRouter("inst")
	require.NoError(t, r.Update(tbl))
	for _, tenant := range []string{"a", "b", "big", "t-1", "t_2"} {
		want := tbl.Lane("inst", tenant)
		got, err := r.Lane(tenant)
		require.NoError(t, err)
		require.Equal(t, want, got)
		g, err := r.ShardGroup(tenant)
		require.NoError(t, err)
		require.Equal(t, want.ShardGroup, g)
	}
	_, err := r.ShardGroup("")
	require.ErrorIs(t, err, ErrNoTenant)
}

func TestRouterUpdateGuards(t *testing.T) {
	r := NewRouter("inst")
	t1 := NewTable(DefaultP)
	t1.Generation = 3
	require.NoError(t, r.Update(t1))

	older := NewTable(DefaultP)
	older.Generation = 2
	require.ErrorIs(t, r.Update(older), ErrStale)

	div := NewTable(DefaultP)
	div.Generation = 3
	div.Groups[0] = 1
	require.ErrorIs(t, r.Update(div), ErrDivergent)

	other := NewTable(16)
	other.Generation = 9
	require.ErrorIs(t, r.Update(other), ErrPChanged)

	bad := NewTable(DefaultP)
	bad.Generation = 9
	bad.Groups = bad.Groups[:3]
	require.Error(t, r.Update(bad))

	// The refused tables left the placement alone.
	require.NoError(t, r.Update(t1))
}

func TestRouterDoesNotAliasTheCallersTable(t *testing.T) {
	tbl := NewTable(DefaultP)
	tbl.Overrides = map[string]int{"x": 1}
	r := NewRouter("inst")
	require.NoError(t, r.Update(tbl))
	tbl.Groups[HashedVPartition("inst", "y", DefaultP)] = 7
	tbl.Overrides["x"] = 2
	g, err := r.ShardGroup("y")
	require.NoError(t, err)
	require.Equal(t, 0, g)
	l, _ := r.Lane("x")
	require.Equal(t, 1, l.VPartition)
}

func TestRouterRefreshFromMirror(t *testing.T) {
	b := newMapBucket()
	r := NewRouter("inst")
	require.True(t, errors.Is(r.Refresh(b), ErrNoTable))
	tbl := NewTable(DefaultP)
	tbl.Generation = 1
	require.NoError(t, Publish(b, tbl))
	require.NoError(t, r.Refresh(b))
	_, err := r.ShardGroup("a")
	require.NoError(t, err)
}

func TestRouterRefusesAfterAnIncompatibleMirror(t *testing.T) {
	b := newMapBucket()
	r := NewRouter("inst")
	tbl := NewTable(DefaultP)
	tbl.Generation = 1
	require.NoError(t, Publish(b, tbl))
	require.NoError(t, r.Refresh(b))
	_, err := r.ShardGroup("a")
	require.NoError(t, err)

	// A table from a newer release (unknown field) must stop routing, not be ignored.
	b.m[TableKey] = []byte(`{"p":64,"groups":[],"future":1}`)
	require.Error(t, r.Refresh(b))
	_, err = r.ShardGroup("a")
	require.Error(t, err)

	// A good, newer table recovers.
	tbl.Generation = 2
	require.NoError(t, r.Update(tbl))
	_, err = r.ShardGroup("a")
	require.NoError(t, err)
}

func TestRouterKeepsPlacementOnTransientAndStale(t *testing.T) {
	b := newMapBucket()
	r := NewRouter("inst")
	tbl := NewTable(DefaultP)
	tbl.Generation = 5
	require.NoError(t, r.Update(tbl))
	require.ErrorIs(t, r.Refresh(b), ErrNoTable) // empty mirror
	_, err := r.ShardGroup("a")
	require.NoError(t, err)
	old := NewTable(DefaultP)
	old.Generation = 4
	enc, _ := Encode(old)
	b.m[TableKey] = enc
	require.ErrorIs(t, r.Refresh(b), ErrStale)
	_, err = r.ShardGroup("a")
	require.NoError(t, err)
	b.getErr = errors.New("nats down")
	require.Error(t, r.Refresh(b))
	_, err = r.ShardGroup("a")
	require.NoError(t, err, "a transient read failure keeps the placement")
}

func TestRouterConcurrentUpdatesNeverGoBackwards(t *testing.T) {
	r := NewRouter("inst")
	var wg sync.WaitGroup
	for g := uint64(1); g <= 200; g++ {
		wg.Add(1)
		go func(g uint64) {
			defer wg.Done()
			tbl := NewTable(DefaultP)
			tbl.Generation = g
			_ = r.Update(tbl)
		}(g)
	}
	wg.Wait()
	r.mu.RLock()
	defer r.mu.RUnlock()
	require.Equal(t, uint64(200), r.table.Generation)
}
