// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package placement

import (
	"context"
	"errors"
	"fmt"
	"math"
	"testing"

	"github.com/devicechain-io/dc-microservice/config"
	"github.com/devicechain-io/dc-microservice/partition"
	"github.com/stretchr/testify/require"
)

type bucket struct {
	m    map[string][]byte
	rev  map[string]uint64
	next uint64
}

func newBucket() *bucket { return &bucket{m: map[string][]byte{}, rev: map[string]uint64{}} }

func (b *bucket) put(k string, v []byte) {
	b.next++
	b.m[k] = append([]byte(nil), v...)
	b.rev[k] = b.next
}

func (b *bucket) Get(k string) ([]byte, uint64, bool, error) {
	v, ok := b.m[k]
	return v, b.rev[k], ok, nil
}

func (b *bucket) Create(k string, v []byte) error {
	if _, ok := b.m[k]; ok {
		return partition.ErrConflict
	}
	b.put(k, v)
	return nil
}

func (b *bucket) Update(k string, v []byte, rev uint64) error {
	if b.rev[k] != rev {
		return partition.ErrConflict
	}
	b.put(k, v)
	return nil
}

type source struct {
	table partition.Table
	err   error
	reads int
	// after is called after each read, to change what the next read returns.
	after func(*source)
}

func (s *source) Placement(context.Context) (partition.Table, error) {
	s.reads++
	t, err := s.table, s.err
	if s.after != nil {
		s.after(s)
	}
	return t, err
}

func newTable() partition.Table {
	t := partition.NewTable(partition.DefaultP)
	t.Groups[1] = 1
	t.Overrides = map[string]int{"acme": 1}
	t.Generation = 3
	return t
}

func TestSyncMakesTheMirrorEqualTheTable(t *testing.T) {
	b := newBucket()
	src := &source{table: newTable()}
	require.NoError(t, Sync(context.Background(), src, b, nil))
	require.NoError(t, partition.Verify(b, src.table))

	// A change in the database reaches the mirror on the next pass, and only then.
	src.table.Groups[2] = 2
	src.table.Generation++
	require.Error(t, partition.Verify(b, src.table))
	require.NoError(t, Sync(context.Background(), src, b, nil))
	require.NoError(t, partition.Verify(b, src.table))
}

// 🔑 THE MIRROR'S CONSISTENCY WITH THE TABLE: a Sync that left the mirror differing from the
// table must say so, not return nil.
func TestSyncReportsAMirrorThatDoesNotHoldTheTable(t *testing.T) {
	lying := &lyingBucket{bucket: *newBucket()}
	require.Error(t, Sync(context.Background(), &source{table: newTable()}, lying, nil))
}

// lyingBucket accepts every write and stores something else.
type lyingBucket struct{ bucket }

func (b *lyingBucket) Create(k string, _ []byte) error {
	other := newTable()
	other.Generation = 99
	raw, err := partition.Encode(other)
	if err != nil {
		return err
	}
	b.put(k, raw)
	return nil
}

func TestSyncRefusesAMirrorWithAnotherPAndItIsFatal(t *testing.T) {
	b := newBucket()
	require.NoError(t, partition.Publish(b, partition.NewTable(partition.DefaultP*2)))

	err := Sync(context.Background(), &source{table: newTable()}, b, nil)
	require.Error(t, err)
	require.True(t, errors.Is(err, partition.ErrPChanged))
	require.True(t, IsFatal(err))
}

func TestOtherFailuresAreNotFatal(t *testing.T) {
	b := newBucket()
	err := Sync(context.Background(), &source{err: errors.New("db away")}, b, nil)
	require.Error(t, err)
	require.False(t, IsFatal(err))
	require.Empty(t, b.m, "an unreadable table must not blank or seed the mirror")
}

// A replica whose read of the database went stale is refused by the mirror and, on re-reading,
// publishes the newer table instead of waiting for the next interval.
func TestSyncRereadsOnceWhenItsReadWentStale(t *testing.T) {
	b := newBucket()
	newer := newTable()
	newer.Generation = 5
	require.NoError(t, partition.Publish(b, newer))

	stale := newTable()
	stale.Generation = 4
	src := &source{table: stale, after: func(s *source) { s.table = newer }}
	require.NoError(t, Sync(context.Background(), src, b, nil))
	require.Equal(t, 2, src.reads)
	require.NoError(t, partition.Verify(b, newer), "the mirror must never have gone backwards")
}

func TestSyncGivesUpAfterOneReread(t *testing.T) {
	b := newBucket()
	newer := newTable()
	newer.Generation = 5
	require.NoError(t, partition.Publish(b, newer))

	stale := newTable()
	stale.Generation = 4
	src := &source{table: stale}
	err := Sync(context.Background(), src, b, nil)
	require.True(t, errors.Is(err, partition.ErrStale), "%v", err)
	require.Equal(t, 2, src.reads)
	require.False(t, IsFatal(err))
}

// A bucket that cannot be opened fails the pass, not the service, and is retried.
func TestALazyBucketRetriesItsOpenUntilItWorks(t *testing.T) {
	opens := 0
	real := newBucket()
	lazy := NewLazyBucket(func() (partition.Bucket, error) {
		opens++
		if opens < 3 {
			return nil, errors.New("broker away")
		}
		return real, nil
	})
	src := &source{table: newTable()}

	for i := 0; i < 2; i++ {
		err := Sync(context.Background(), src, lazy, nil)
		require.ErrorContains(t, err, "broker away")
		require.False(t, IsFatal(err))
	}
	require.Empty(t, real.m)

	require.NoError(t, Sync(context.Background(), src, lazy, nil))
	require.NoError(t, partition.Verify(real, src.table))
	opens = 0
	require.NoError(t, Sync(context.Background(), src, lazy, nil))
	require.Equal(t, 0, opens, "an opened bucket is kept")
}

// A changed P found on a PERIODIC pass fails the pass (so the pass metric and its alert see it),
// keeps the sentinel, and is reported through the fatal hook. It does not stop the process: see
// IsFatal for why only the start does. A transient failure is not reported that way.
func TestPeriodicPassReportsAChangedPLoudly(t *testing.T) {
	var reported []error
	onFatal := func(err error) { reported = append(reported, err) }

	b := newBucket()
	require.NoError(t, partition.Publish(b, partition.NewTable(partition.DefaultP*2)))
	err := mirrorPass(&source{table: newTable()}, b, nil, onFatal)(context.Background())
	require.Error(t, err)
	require.True(t, IsFatal(err))
	require.Len(t, reported, 1)
	require.True(t, IsFatal(reported[0]))

	reported = nil
	err = mirrorPass(&source{err: errors.New("db away")}, newBucket(), nil, onFatal)(context.Background())
	require.Error(t, err)
	require.Empty(t, reported)
}

func TestSyncNatsShardGroupLimit(t *testing.T) {
	for _, configured := range []bool{true, false} {
		for _, group := range []int{1, 2, 3} {
			t.Run(fmt.Sprintf("configured=%t/group=%d", configured, group), func(t *testing.T) {
				var cfg *config.NatsShardGroupsConfiguration
				if configured {
					cfg = &config.NatsShardGroupsConfiguration{Count: 2}
				}
				table := newTable()
				table.Groups[3] = group
				b := newBucket()
				err := Sync(context.Background(), &source{table: table}, b, cfg)
				if configured && group >= 2 {
					require.ErrorContains(t, err, fmt.Sprintf("group %d, count 2", group))
					require.ErrorContains(t, err, "nats.shardGroups.count must be raised first")
					require.Empty(t, b.m)
					require.Zero(t, b.next, "invalid placement must never write the bucket")
					require.Error(t, mirrorPass(&source{table: table}, b, cfg, func(error) {})(context.Background()))
					require.Zero(t, b.next)
				} else {
					require.NoError(t, err)
					require.NoError(t, partition.Verify(b, table))
				}
			})
		}
	}
}

// Table.S() is the highest group plus one, so a group of math.MaxInt would overflow a
// comparison made through it; the limit compares each group directly.
func TestSyncNatsShardGroupLimitMaxInt(t *testing.T) {
	table := newTable()
	table.Groups[3] = math.MaxInt
	b := newBucket()
	err := Sync(context.Background(), &source{table: table}, b, &config.NatsShardGroupsConfiguration{Count: 2})
	require.ErrorContains(t, err, "nats.shardGroups.count must be raised first")
	require.Zero(t, b.next, "invalid placement must never write the bucket")
}
