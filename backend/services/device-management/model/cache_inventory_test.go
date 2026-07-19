// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"reflect"
	"testing"

	"github.com/devicechain-io/dc-microservice/kv"
)

// Every cache in this bundle is a JetStream KV bucket that reserves its ceiling
// up front, so the platform disk budget (config.KvReservation) has to know about
// all of them. It derives that from kv.All, which lives in core and cannot see
// this package — so a cache added here without a matching inventory entry would
// reserve disk nothing budgets for, and would silently take the State ceiling
// (kv.TierFor's fallback) rather than the cache one it belongs in.
//
// That is the same class of drift core/streams was created to end for message
// streams, where a hand-maintained mirror of the set had already gone wrong. This
// is the equivalent tripwire for buckets: adding a cache field without declaring
// the bucket fails here.
func TestEveryCacheIsDeclaredInTheKvInventory(t *testing.T) {
	fields := reflect.TypeOf(Caches{}).NumField()
	if got := kv.Count(kv.Cache); got != fields {
		t.Errorf("Caches has %d caches but kv.All declares %d cache-tier buckets: "+
			"an undeclared cache is missing from the disk budget and falls to the "+
			"State ceiling instead of the cache one", fields, got)
	}
}
