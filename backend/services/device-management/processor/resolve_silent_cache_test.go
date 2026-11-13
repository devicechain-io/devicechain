// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/test/msgtest"
)

// Resolution carries on, at database speed, when every cache stops answering.
//
// A cache read that is never answered, the way one routed to a NATS server that has
// dropped off the network is not, used to hold its resolver for the full 5 s JetStream
// request timeout, and a warm event makes four of them. Now each read gives up at the
// cache's budget, the cache that failed is skipped for a while, and the event is resolved
// from the database, which holds the same data.
func TestResolveCarriesOnWhenEveryCacheGoesSilent(t *testing.T) {
	var silent []*msgtest.SilentKV
	rig := newCountingResolveRig(t, false, func(field string, kv *msgtest.MemoryKV) *messaging.Cache {
		s := &msgtest.SilentKV{MemoryKV: kv}
		silent = append(silent, s)
		// In-process tier off: with it on, the warm-up would leave every value in memory
		// and no read would reach the silenced store.
		return messaging.NewCacheOver(s, messaging.WithoutLocalCache())
	})
	rig.resolve(tempEvent("21"))
	rig.resetCounters()
	for _, s := range silent {
		s.Arm()
	}

	const events = 20
	var firstSilenced int64
	var remainingStart time.Time
	firstStart := time.Now()
	for i := 0; i < events; i++ {
		start := time.Now()
		resolved := rig.resolve(tempEvent("21"))
		took := time.Since(start)
		if got := stampedUnit(t, resolved); got != "Cel" || resolved.ProfileVersionToken != "p@1" ||
			resolved.SourceDeviceToken != "dev" || resolved.DeviceTypeToken != "dt" {
			t.Fatalf("event %d resolved unit %q at %q for device %q of type %q; want Cel at p@1 for dev of dt",
				i+1, got, resolved.ProfileVersionToken, resolved.SourceDeviceToken, resolved.DeviceTypeToken)
		}
		if i == 0 {
			// Four reads that each wait out the budget is the most the first event can pay.
			if took > 4*time.Second {
				t.Fatalf("the first event with every cache silent took %s; each read must give up at the "+
					"cache's budget instead of waiting out a JetStream request timeout", took)
			}
			for _, s := range silent {
				firstSilenced += s.Silenced.Load()
			}
			if firstSilenced == 0 {
				t.Fatal("no cache read reached a silent store, so the test proves nothing about one")
			}
			remainingStart = time.Now()
			continue
		}
		var swallowed int64
		for _, s := range silent {
			swallowed += s.Silenced.Load()
		}
		// NewCacheOver uses a 500 ms read budget and cacheBypassFor = 5 s.
		// admit skips an open cache until that window expires; settle renews it
		// after a failed probe. The 30 s hang guard can outlast the window, so
		// allow at most one re-probe per store per 5 s, and none before then.
		// Measuring from before event 1 conservatively includes the staggered
		// initial failures. SilentKV counts each swallowed Get or Put on entry.
		windows := int64(time.Since(firstStart) / (5 * time.Second))
		if added, allowed := swallowed-firstSilenced, int64(len(silent))*windows; added > allowed {
			t.Fatalf("event %d added %d silenced cache calls after the first event; want at most %d across %d bypass windows; "+
				"once a cache has failed it must be skipped, not waited on again", i+1, added, allowed, windows)
		}
	}
	if took := time.Since(remainingStart); took > 30*time.Second {
		t.Errorf("events 2..%d took %s with every cache silent, want under 30 s", events, took)
	}
	if len(rig.statements()) == 0 {
		t.Fatal("no database reads while every cache was silent; the events cannot have been resolved from the database")
	}
}
