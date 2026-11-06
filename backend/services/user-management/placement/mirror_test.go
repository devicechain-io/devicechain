// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package placement

import (
	"context"
	"errors"
	"testing"

	"github.com/devicechain-io/dc-microservice/partition"
	"github.com/stretchr/testify/require"
)

type bucket struct{ m map[string][]byte }

func (b *bucket) Put(k string, v []byte) error {
	b.m[k] = append([]byte(nil), v...)
	return nil
}

func (b *bucket) Get(k string) ([]byte, bool, error) { v, ok := b.m[k]; return v, ok, nil }

type source struct {
	table partition.Table
	err   error
}

func (s *source) Placement(context.Context) (partition.Table, error) { return s.table, s.err }

func newTable() partition.Table {
	t := partition.NewTable(partition.DefaultP)
	t.Groups[1] = 1
	t.Overrides = map[string]int{"acme": 1}
	t.Generation = 3
	return t
}

func TestSyncMakesTheMirrorEqualTheTable(t *testing.T) {
	b := &bucket{m: map[string][]byte{}}
	src := &source{table: newTable()}
	require.NoError(t, Sync(context.Background(), src, b))
	require.NoError(t, partition.Verify(b, src.table))

	// A change in the database reaches the mirror on the next pass, and only then.
	src.table.Groups[2] = 2
	src.table.Generation++
	require.Error(t, partition.Verify(b, src.table))
	require.NoError(t, Sync(context.Background(), src, b))
	require.NoError(t, partition.Verify(b, src.table))
}

// 🔑 THE MIRROR'S CONSISTENCY WITH THE TABLE: a Sync that left the mirror differing from the
// table must say so, not return nil.
func TestSyncReportsAMirrorThatDoesNotHoldTheTable(t *testing.T) {
	lying := &lyingBucket{bucket: bucket{m: map[string][]byte{}}}
	require.Error(t, Sync(context.Background(), &source{table: newTable()}, lying))
}

// lyingBucket accepts every write and stores something else.
type lyingBucket struct{ bucket }

func (b *lyingBucket) Put(k string, _ []byte) error {
	other := newTable()
	other.Generation = 99
	raw, err := partition.Encode(other)
	if err != nil {
		return err
	}
	b.m[k] = raw
	return nil
}

func TestSyncRefusesAMirrorWithAnotherPAndItIsFatal(t *testing.T) {
	b := &bucket{m: map[string][]byte{}}
	require.NoError(t, partition.Publish(b, partition.NewTable(partition.DefaultP*2)))

	err := Sync(context.Background(), &source{table: newTable()}, b)
	require.Error(t, err)
	require.True(t, errors.Is(err, partition.ErrPChanged))
	require.True(t, IsFatal(err))
}

func TestOtherFailuresAreNotFatal(t *testing.T) {
	b := &bucket{m: map[string][]byte{}}
	err := Sync(context.Background(), &source{err: errors.New("db away")}, b)
	require.Error(t, err)
	require.False(t, IsFatal(err))
	require.Empty(t, b.m, "an unreadable table must not blank or seed the mirror")
}
