// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package partition

import (
	"errors"
	"hash/fnv"
	"math/rand/v2"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/devicechain-io/dc-microservice/core"
)

// randomTenants is a fixed corpus: the same n tokens on every run, so a failure reproduces.
func randomTenants(t *testing.T, n int) []string {
	t.Helper()
	const alpha = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_-"
	rng := rand.New(rand.NewPCG(0xdec1de, 0xc4a1))
	out := make([]string, 0, n)
	seen := map[string]bool{}
	for len(out) < n {
		l := 1 + rng.IntN(40)
		b := make([]byte, l)
		for i := range b {
			b[i] = alpha[rng.IntN(len(alpha))]
		}
		b[0] = alpha[rng.IntN(62)] // a token starts with a letter or digit
		s := string(b)
		if seen[s] {
			continue
		}
		seen[s] = true
		require.NoError(t, core.ValidateToken(s))
		out = append(out, s)
	}
	return out
}

// referenceVPartition is the contract restated from the standard library, deliberately not
// sharing a line with Hash: it is what "every caller computes the same lane" is measured
// against.
func referenceVPartition(instance, tenant string, p int) int {
	h := fnv.New64a()
	_, _ = h.Write([]byte(instance + "/" + tenant))
	return int(h.Sum64() % uint64(p))
}

// TestHashGoldenVectors pins the hash. These values were computed by an independent FNV-1a
// implementation. A change to the hash, its seed, its byte order or its separator moves a
// tenant's lane and must fail here before it can orphan anyone's data.
func TestHashGoldenVectors(t *testing.T) {
	cases := []struct {
		instance, tenant string
		hash             uint64
		vp               int
	}{
		{"", "", 12638123428881205758, 62},
		{"a", "", 620363185194866937, 57},
		{"dc", "acme", 8487109039535901095, 39},
		{"dc", "Acme", 11553498586897169799, 7},
		{"prod-1", "tenant_001", 15976334630788448151, 23},
		{"prod-1", "tenant_002", 15976335730300076362, 10},
		{"inst", "x", 17406508797402157294, 46},
		{"dc-test", "zzzzzzzzzzzzzzzz", 12007662379157566164, 20},
	}
	for _, c := range cases {
		require.Equal(t, c.hash, Hash(c.instance, c.tenant), "%q/%q", c.instance, c.tenant)
		require.Equal(t, c.vp, HashedVPartition(c.instance, c.tenant, DefaultP), "%q/%q", c.instance, c.tenant)
	}
}

// TestHashIsPublishedFnv1a ties Hash to the FNV-1a specification itself: the published vectors
// for "a" (0xaf63dc4c8601ec8c) and "foobar" (0x85944171f73967e8), and the standard library on the
// exact bytes Hash is documented to hash.
func TestHashIsPublishedFnv1a(t *testing.T) {
	h := fnv.New64a()
	_, _ = h.Write([]byte("a"))
	require.Equal(t, uint64(0xaf63dc4c8601ec8c), h.Sum64())
	h = fnv.New64a()
	_, _ = h.Write([]byte("foobar"))
	require.Equal(t, uint64(0x85944171f73967e8), h.Sum64())

	for _, tc := range []struct{ i, tn string }{{"foo", "bar"}, {"dc", "acme"}, {"", ""}} {
		h := fnv.New64a()
		_, _ = h.Write([]byte(tc.i + "/" + tc.tn))
		require.Equal(t, h.Sum64(), Hash(tc.i, tc.tn))
	}
}

// 🔑 THE LOCKSTEP TEST. Every way the repo has of asking "which lane is this tenant in" must give
// one answer for every tenant: the bare function, the table, the lane, a table that has been
// through the mirror's encoding, and the stdlib restatement. Today they are all in this
// package; the day a second caller appears it joins this list.
func TestEveryCallerComputesTheSameLane(t *testing.T) {
	const instance = "dc-lockstep"
	table := NewTable(DefaultP)
	mirror := newMapBucket()
	require.NoError(t, Publish(mirror, table))
	mirrored, err := Load(mirror)
	require.NoError(t, err)

	for _, tenant := range randomTenants(t, 10_000) {
		want := referenceVPartition(instance, tenant, DefaultP)
		require.Equal(t, want, HashedVPartition(instance, tenant, DefaultP), tenant)
		require.Equal(t, want, table.VPartition(instance, tenant), tenant)
		require.Equal(t, want, table.Lane(instance, tenant).VPartition, tenant)
		require.Equal(t, want, mirrored.VPartition(instance, tenant), tenant)
		require.Equal(t, want, mirrored.Lane(instance, tenant).VPartition, tenant)
		require.GreaterOrEqual(t, want, 0)
		require.Less(t, want, DefaultP)
	}
}

// The modulo is a modulo for EVERY P, not only for powers of two, where a mask would agree with
// it. Nothing but 64 is in use, but P is recorded rather than assumed, so the function must
// stay right for the value that is recorded.
func TestTheModuloIsAModuloForAnyP(t *testing.T) {
	for _, p := range []int{1, 2, 7, 48, 64, 100, 999, MaxP} {
		for _, tenant := range randomTenants(t, 500) {
			require.Equal(t, referenceVPartition("dc-p", tenant, p), HashedVPartition("dc-p", tenant, p), "P=%d %s", p, tenant)
		}
	}
}

// The corpus must actually spread, or the lockstep test above could pass on a hash that maps
// everything to one lane.
func TestTheHashSpreadsTenantsAcrossEveryVPartition(t *testing.T) {
	counts := make([]int, DefaultP)
	for _, tenant := range randomTenants(t, 10_000) {
		counts[HashedVPartition("dc-lockstep", tenant, DefaultP)]++
	}
	for vp, n := range counts {
		require.Greater(t, n, 80, "vpartition %d got %d of 10000 (mean 156)", vp, n)
		require.Less(t, n, 260, "vpartition %d got %d of 10000 (mean 156)", vp, n)
	}
}

func TestTheInstanceIdIsPartOfTheKey(t *testing.T) {
	moved := 0
	for _, tenant := range randomTenants(t, 1000) {
		if HashedVPartition("inst-a", tenant, DefaultP) != HashedVPartition("inst-b", tenant, DefaultP) {
			moved++
		}
	}
	require.Greater(t, moved, 900, "two instances must not place tenants alike")
}

// An override pins a tenant to the vpartition it names and nobody else moves.
func TestAnOverrideMovesOnlyItsTenant(t *testing.T) {
	const instance = "dc-override"
	tenants := randomTenants(t, 2000)
	table := NewTable(DefaultP)
	pinned := tenants[17]
	target := (HashedVPartition(instance, pinned, DefaultP) + 1) % DefaultP
	table.Overrides = map[string]int{pinned: target}
	table.Groups[target] = 2
	require.NoError(t, table.Validate())

	for _, tenant := range tenants {
		if tenant == pinned {
			require.Equal(t, target, table.VPartition(instance, tenant))
			require.Equal(t, Lane{VPartition: target, ShardGroup: 2}, table.Lane(instance, tenant))
			continue
		}
		require.Equal(t, HashedVPartition(instance, tenant, DefaultP), table.VPartition(instance, tenant), tenant)
	}
}

// 🔑 S = 1 IS TODAY. With every vpartition on group 0 the answer to "which shard group" is 0
// for every tenant whatever the hash says, and the table spans exactly one group.
func TestOneShardGroupIsTodaysBehaviour(t *testing.T) {
	const instance = "dc-s1"
	table := NewTable(DefaultP)
	require.Equal(t, 1, table.S())
	require.NoError(t, table.Validate())
	require.Equal(t, uint64(0), table.Generation)
	require.Empty(t, table.Overrides)
	for _, tenant := range randomTenants(t, 10_000) {
		require.Equal(t, 0, table.Lane(instance, tenant).ShardGroup, tenant)
	}
	// Even a tenant pinned elsewhere stays on group 0 while the one group owns every vpartition.
	table.Overrides = map[string]int{"big": 63}
	require.Equal(t, 0, table.Lane(instance, "big").ShardGroup)
	require.Equal(t, 63, table.Lane(instance, "big").VPartition)
}

// Growing S moves vpartitions and never re-hashes: no tenant's vpartition changes when a
// vpartition changes group.
func TestMovingAVPartitionDoesNotRehashAnyone(t *testing.T) {
	const instance = "dc-grow"
	tenants := randomTenants(t, 5000)
	before := NewTable(DefaultP)
	after := NewTable(DefaultP)
	for vp := 0; vp < DefaultP; vp += 2 {
		after.Groups[vp] = 1
	}
	require.Equal(t, 2, after.S())
	for _, tenant := range tenants {
		require.Equal(t, before.VPartition(instance, tenant), after.VPartition(instance, tenant))
		require.Equal(t, after.Groups[after.VPartition(instance, tenant)], after.Lane(instance, tenant).ShardGroup)
	}
}

func TestValidateRefusesAnInconsistentTable(t *testing.T) {
	require.NoError(t, NewTable(DefaultP).Validate())

	cases := map[string]func(*Table){
		"zero P":                func(x *Table) { x.P = 0; x.Groups = nil },
		"P over the ceiling":    func(x *Table) { x.P = MaxP + 1; x.Groups = make([]int, MaxP+1) },
		"short groups":          func(x *Table) { x.Groups = x.Groups[:10] },
		"negative group":        func(x *Table) { x.Groups[3] = -1 },
		"override out of range": func(x *Table) { x.Overrides = map[string]int{"a": DefaultP} },
		"negative override":     func(x *Table) { x.Overrides = map[string]int{"a": -1} },
		"empty tenant":          func(x *Table) { x.Overrides = map[string]int{"": 1} },
	}
	for name, mutate := range cases {
		x := NewTable(DefaultP)
		mutate(&x)
		require.Error(t, x.Validate(), name)
	}

	many := NewTable(DefaultP)
	many.Overrides = map[string]int{}
	for i := 0; i <= MaxOverrides; i++ {
		many.Overrides[randomKey(i)] = 0
	}
	require.Greater(t, len(many.Overrides), MaxOverrides)
	require.Error(t, many.Validate())
}

func randomKey(i int) string {
	const digits = "abcdefghij"
	b := []byte("t")
	for n := i; ; n /= 10 {
		b = append(b, digits[n%10])
		if n < 10 {
			break
		}
	}
	return string(b)
}

func TestAStrayVPartitionFailsLoudlyNotAsGroupZero(t *testing.T) {
	table := NewTable(DefaultP)
	require.Panics(t, func() { table.ShardGroup(DefaultP) })
	require.Panics(t, func() { table.ShardGroup(-1) })
	require.Panics(t, func() { HashedVPartition("dc", "acme", 0) })
}

// 🔑 P IMMUTABILITY, at the value level: any other P is the sentinel error, and the same P is nil.
func TestCheckPRefusesAnyOtherP(t *testing.T) {
	require.NoError(t, CheckP(DefaultP, DefaultP))
	for _, other := range []int{1, 32, 63, 65, 128, MaxP} {
		err := CheckP(DefaultP, other)
		require.Error(t, err)
		require.True(t, errors.Is(err, ErrPChanged), "P=%d", other)
	}
}

func TestLabelIsBoundedByP(t *testing.T) {
	seen := map[string]bool{}
	for _, tenant := range randomTenants(t, 10_000) {
		seen[Label(HashedVPartition("dc", tenant, DefaultP))] = true
	}
	require.LessOrEqual(t, len(seen), DefaultP)
}
