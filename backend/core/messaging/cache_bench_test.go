// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging

import (
	"context"
	"strings"
	"testing"
	"time"
)

// BenchmarkCacheGet compares a Get answered by the bucket (tier=kv, the in-process tier
// off) with one answered from process memory (tier=local), on an embedded single-server
// JetStream over loopback.
//
// 🔴 THE kv ROWS UNDERSTATE A PRODUCTION HIT. Loopback to one in-process server has no
// network hop and no replica; a replicated bucket across nodes costs more per read. The
// local rows are the same wherever they run.
//
// size=64KiB is the decode-per-hit cost: memory holds encoded bytes and decodes them for
// every Get, so a large value costs a decode even on a hit. parallel stands for a resolver
// pool: two goroutines per GOMAXPROCS reading one key at once.
func BenchmarkCacheGet(b *testing.B) {
	nmgr, cleanup := newTestManager(b)
	defer cleanup()

	type payload struct {
		Token string `json:"token"`
		Blob  string `json:"blob"`
	}
	sizes := []struct {
		name  string
		value payload
	}{
		{"small", payload{Token: "dev-1"}},
		{"64KiB", payload{Token: "dev-1", Blob: strings.Repeat("x", 64<<10)}},
	}
	tiers := []struct {
		name string
		opts []CacheOption
	}{
		{"kv", []CacheOption{WithoutLocalCache()}},
		{"local", nil},
	}
	ctx := context.Background()
	for _, tier := range tiers {
		c, err := nmgr.NewCache("bench-"+tier.name, time.Minute, tier.opts...)
		if err != nil {
			b.Fatalf("NewCache: %v", err)
		}
		for _, size := range sizes {
			key := "acme|" + size.name
			if err := c.Set(ctx, key, size.value); err != nil {
				b.Fatalf("Set: %v", err)
			}
			get := func(b *testing.B) bool {
				var got payload
				if found, err := c.Get(ctx, key, &got); err != nil || !found {
					b.Errorf("Get = (%v, %v)", found, err)
					return false
				}
				return true
			}
			b.Run("tier="+tier.name+"/size="+size.name+"/serial", func(b *testing.B) {
				b.ReportAllocs()
				for i := 0; i < b.N; i++ {
					if !get(b) {
						return
					}
				}
			})
			b.Run("tier="+tier.name+"/size="+size.name+"/parallel", func(b *testing.B) {
				b.ReportAllocs()
				b.SetParallelism(2)
				b.RunParallel(func(pb *testing.PB) {
					for pb.Next() {
						get(b)
					}
				})
			})
		}
	}
}
