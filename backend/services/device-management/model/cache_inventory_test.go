// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"reflect"
	"testing"

	"github.com/devicechain-io/dc-microservice/kv"
	"github.com/devicechain-io/dc-microservice/messaging"
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
// is the equivalent tripwire for buckets.
//
// It compares the declared NAMES rather than just counting, because counts alone
// let two mistakes cancel: an orphaned kv.All entry plus an undeclared cache
// still sum correctly. Counting only *messaging.Cache fields also keeps the test
// from failing spuriously the day Caches grows a field that is not a cache.
func TestEveryCacheIsDeclaredInTheKvInventory(t *testing.T) {
	cacheType := reflect.TypeOf((*messaging.Cache)(nil))
	tv := reflect.TypeOf(Caches{})

	var fields []string
	for i := 0; i < tv.NumField(); i++ {
		if tv.Field(i).Type == cacheType {
			fields = append(fields, tv.Field(i).Name)
		}
	}
	if len(fields) == 0 {
		t.Fatal("no *messaging.Cache fields found on Caches; this tripwire is not testing anything")
	}

	declared := map[string]bool{}
	for _, b := range kv.All {
		if b.Tier == kv.Cache {
			declared[b.Name] = true
		}
	}

	// The bucket name each cache is created under — the value passed to NewCache,
	// which is what kv.TierFor keys on.
	used := map[string]string{
		"DeviceByToken":         CACHE_NAME_DEVICE_BY_TOKEN,
		"RelationshipsBySource": CACHE_NAME_RELATIONSHIPS_BY_SOURCE,
		"MetricDefsByType":      CACHE_NAME_METRIC_DEFS_BY_TYPE,
		"ProfileScopeByType":    CACHE_NAME_PROFILE_SCOPE_BY_TYPE,
		"MembershipsByEntity":   CACHE_NAME_MEMBERSHIPS_BY_ENTITY,
		"ScopedGroupsExist":     CACHE_NAME_SCOPED_GROUPS_EXIST,
	}

	for _, f := range fields {
		name, ok := used[f]
		if !ok {
			t.Errorf("cache field %q has no bucket name here: add it, and declare the bucket "+
				"in kv.All, or it reserves disk the budget does not know about", f)
			continue
		}
		if !declared[name] {
			t.Errorf("cache %q (field %s) is not declared as a Cache-tier bucket in kv.All: it is "+
				"missing from the disk budget and falls to the larger State ceiling", name, f)
		}
		delete(declared, name)
	}
	for orphan := range declared {
		t.Errorf("kv.All declares Cache-tier bucket %q that no cache in this bundle creates: "+
			"the budget reserves disk for a bucket that does not exist", orphan)
	}
}
