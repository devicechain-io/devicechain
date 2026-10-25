// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package sourcelimit

import (
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type clock struct{ t time.Time }

func (c *clock) now() time.Time { return c.t }

func newTest(t *testing.T, l Limits) (*Limiter, *clock) {
	t.Helper()
	c := &clock{t: time.Unix(1_000_000, 0)}
	lim, err := New(l, WithClock(c.now))
	require.NoError(t, err)
	return lim, c
}

func TestBurstThenRefill(t *testing.T) {
	l, c := newTest(t, Limits{Burst: 3, Every: time.Second, MaxKeys: 10})
	for i := 0; i < 3; i++ {
		require.True(t, l.Allow("a"), "request %d is inside the burst", i)
	}
	require.False(t, l.Allow("a"))
	c.t = c.t.Add(time.Second)
	require.True(t, l.Allow("a"), "one request is earned per interval")
	require.False(t, l.Allow("a"))
}

func TestKeysAreIndependent(t *testing.T) {
	l, _ := newTest(t, Limits{Burst: 1, Every: time.Hour, MaxKeys: 10})
	require.True(t, l.Allow("a"))
	require.False(t, l.Allow("a"))
	require.True(t, l.Allow("b"), "one sender exhausting its allowance must not touch another's")
}

func TestKeyMapIsBounded(t *testing.T) {
	l, _ := newTest(t, Limits{Burst: 1, Every: time.Hour, MaxKeys: 5})
	for i := 0; i < 1000; i++ {
		l.Allow(fmt.Sprintf("k%d", i))
	}
	require.LessOrEqual(t, len(l.buckets), 5, "a spray of distinct keys grew the map")
}

func TestASingleKeyLimiterIsAGlobalBucket(t *testing.T) {
	// How a caller that wants a global bucket builds one: the primitive carries none.
	g, _ := newTest(t, Limits{Burst: 4, Every: time.Hour, MaxKeys: 1})
	ok := 0
	for i := 0; i < 20; i++ {
		if g.Allow("") {
			ok++
		}
	}
	require.Equal(t, 4, ok)
}

func TestZeroValueRefuses(t *testing.T) {
	var l Limiter
	require.False(t, l.Allow("a"), "a limiter nobody configured must not allow everything")
	var np *Limiter
	require.False(t, np.Allow("a"))
}

func TestInvalidLimitsAreRefused(t *testing.T) {
	for name, l := range map[string]Limits{
		"no burst": {Burst: 0, Every: time.Second, MaxKeys: 1},
		"no every": {Burst: 1, Every: 0, MaxKeys: 1},
		"no keys":  {Burst: 1, Every: time.Second, MaxKeys: 0},
		"negative": {Burst: 1, Every: -time.Second, MaxKeys: 1},
	} {
		_, err := New(l)
		require.Error(t, err, name)
	}
	require.Panics(t, func() { Must(Limits{}) })
}

// The primitive has no trusted-source exemption and no global bucket: nothing on its
// surface can bypass the per-key bucket, or share one across keys.
func TestThePrimitiveCarriesNeitherAnExemptionNorAGlobalBucket(t *testing.T) {
	typ := reflect.TypeOf(&Limiter{})
	var methods []string
	for i := 0; i < typ.NumMethod(); i++ {
		methods = append(methods, typ.Method(i).Name)
	}
	require.ElementsMatch(t, []string{"Allow", "Every"}, methods)
	for _, f := range []string{"global", "trusted", "exempt"} {
		_, found := reflect.TypeOf(Limiter{}).FieldByName(f)
		require.False(t, found, f)
	}
	// Behaviourally: many keys never share an allowance.
	l, _ := newTest(t, Limits{Burst: 1, Every: time.Hour, MaxKeys: 1000})
	for i := 0; i < 500; i++ {
		require.True(t, l.Allow(fmt.Sprintf("src-%d", i)), "key %d was refused by a bucket it does not own", i)
	}
}

func TestSourceKeyGroupsAnIPv6Subnet(t *testing.T) {
	require.Equal(t, SourceKey("2001:db8:1:2::1"), SourceKey("2001:db8:1:2:ffff:ffff:ffff:fffe"))
	require.NotEqual(t, SourceKey("2001:db8:1:2::1"), SourceKey("2001:db8:1:3::1"))
	require.Equal(t, "192.0.2.1", SourceKey("192.0.2.1"))
	require.Equal(t, "192.0.2.1", SourceKey("::ffff:192.0.2.1"))
	require.NotEqual(t, SourceKey("192.0.2.1"), SourceKey("192.0.2.2"))
	require.Equal(t, "not-an-address", SourceKey("not-an-address"))
}
