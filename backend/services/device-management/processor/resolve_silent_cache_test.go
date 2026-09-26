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
		return messaging.NewCacheOver(s)
	})
	rig.resolve(tempEvent("21"))
	rig.resetCounters()
	for _, s := range silent {
		s.Arm()
	}

	const events = 20
	var total time.Duration
	for i := 0; i < events; i++ {
		start := time.Now()
		resolved := rig.resolve(tempEvent("21"))
		took := time.Since(start)
		total += took
		if got := stampedUnit(t, resolved); got != "Cel" || resolved.ProfileVersionToken != "p@1" ||
			resolved.SourceDeviceToken != "dev" || resolved.DeviceTypeToken != "dt" {
			t.Fatalf("event %d resolved unit %q at %q for device %q of type %q; want Cel at p@1 for dev of dt",
				i+1, got, resolved.ProfileVersionToken, resolved.SourceDeviceToken, resolved.DeviceTypeToken)
		}
		switch {
		case i == 0 && took > 4*time.Second:
			// Four reads that each wait out the budget is the most the first event can pay.
			t.Fatalf("the first event with every cache silent took %s; each read must give up at the "+
				"cache's budget instead of waiting out a JetStream request timeout", took)
		case i > 0 && took > 200*time.Millisecond:
			t.Fatalf("event %d took %s; once a cache has failed it must be skipped, not waited on again",
				i+1, took)
		}
	}
	if total > 4*time.Second {
		t.Errorf("%d events took %s with every cache silent, want under 4 s", events, total)
	}
	var swallowed int64
	for _, s := range silent {
		swallowed += s.Silenced.Load()
	}
	if swallowed == 0 {
		t.Fatal("no cache read reached a silent store, so the test proves nothing about one")
	}
	if len(rig.statements()) == 0 {
		t.Fatal("no database reads while every cache was silent; the events cannot have been resolved from the database")
	}
}
