// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"runtime"
	"testing"
	"time"

	"gorm.io/datatypes"
	"gorm.io/gorm"

	"github.com/devicechain-io/dc-microservice/rdb"
)

// heapPerDecoded is the average heap, in bytes, that one decoded value of raw's JSON holds:
// n copies are decoded and kept live, and the heap is read before and after.
func heapPerDecoded[T any](t *testing.T, raw []byte, n int) float64 {
	t.Helper()
	var sink = make([]*T, 0, n)
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	for i := 0; i < n; i++ {
		v := new(T)
		if err := json.Unmarshal(raw, v); err != nil {
			t.Fatal(err)
		}
		sink = append(sink, v)
	}
	runtime.GC()
	runtime.ReadMemStats(&after)
	runtime.KeepAlive(sink)
	grew := int64(after.HeapAlloc) - int64(before.HeapAlloc)
	if grew < 0 {
		grew = 0 // tiny values share allocator blocks and can read as noise below zero
	}
	return float64(grew) / float64(n)
}

func measuredMetrics(n int, rich bool) ProfileResolution {
	res := ProfileResolution{Scope: ProfileScope{DeviceTypeToken: "thermostat", ProfileVersionToken: "thermostat-profile@12", FenceSetVersion: 3}}
	for i := 0; i < n; i++ {
		m := ResolvedMetric{ID: uint(i + 1), MetricKey: fmt.Sprintf("metric_%d", i), DataType: "DOUBLE"}
		if rich {
			unit, lo, hi := "Cel", -40.0, 125.0
			m.Unit, m.MinValue, m.MaxValue = &unit, &lo, &hi
			m.Enum = []string{"a", "b", "c"}
		}
		res.Metrics = append(res.Metrics, m)
	}
	return res
}

// TestDecodedChargeCoversTheHeap measures what decoded values of representative sizes hold
// on the heap and requires the charge the caches make for them (see decodedCharge) to be at least that. The charge is what the byte bound counts for the
// decoded copy; if it fell below the heap the bound would promise less memory than the
// cache uses. Measured on Go 1.26, windows/amd64, the heap of a decoded value was up to
// 3.4 times its encoded length for a resolution and about 1.3 times or less for the rest.
func TestDecodedChargeCoversTheHeap(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	meta := datatypes.JSON(`{"site":"north-yard","floor":3,"owner":"facilities"}`)
	device := Device{
		Model:          gorm.Model{ID: 42, CreatedAt: now, UpdatedAt: now},
		TenantScoped:   rdb.TenantScoped{TenantId: "acme"},
		TokenReference: rdb.TokenReference{Token: "thermostat-0042"},
		NamedEntity:    rdb.NamedEntity{Name: sql.NullString{String: "Thermostat 42", Valid: true}},
		MetadataEntity: rdb.MetadataEntity{Metadata: &meta},
		DeviceTypeId:   7,
		DeviceType: &DeviceType{
			Model:          gorm.Model{ID: 7, CreatedAt: now, UpdatedAt: now},
			TenantScoped:   rdb.TenantScoped{TenantId: "acme"},
			TokenReference: rdb.TokenReference{Token: "thermostat"},
		},
	}
	rels := EntityRelationshipSearchResults{}
	for i := 0; i < 5; i++ {
		rels.Results = append(rels.Results, EntityRelationship{
			Model: gorm.Model{ID: uint(i + 1), CreatedAt: now, UpdatedAt: now}, TenantScoped: rdb.TenantScoped{TenantId: "acme"},
			TokenReference: rdb.TokenReference{Token: fmt.Sprintf("rel-%d", i)},
			SourceType:     "device", SourceId: 42, TargetType: "area", TargetId: uint(i + 100), TargetToken: fmt.Sprintf("area-%d", i),
			RelationshipTypeId: 1,
			RelationshipType:   EntityRelationshipType{TokenReference: rdb.TokenReference{Token: "located-in"}, Tracked: true},
		})
	}
	var memberships []GroupMembership
	for i := 0; i < 50; i++ {
		memberships = append(memberships, GroupMembership{GroupId: uint(i), GroupToken: fmt.Sprintf("group-%d", i), SelectorVersion: 3})
	}

	type measured struct {
		name   string
		heap   float64
		enc    int
		charge decodedCharge
	}
	var cases []measured
	var current decodedCharge
	add := func(name string, heap float64, v any) {
		raw, _ := json.Marshal(v)
		cases = append(cases, measured{name, heap, len(raw), current})
	}
	marshal := func(v any) []byte { raw, _ := json.Marshal(v); return raw }

	r10, r100, r20 := measuredMetrics(10, false), measuredMetrics(100, false), measuredMetrics(20, true)
	r1 := measuredMetrics(1, false)
	current = deviceCharge
	add("device", heapPerDecoded[Device](t, marshal(device), 2000), device)
	current = resolutionCharge
	add("resolution 1 metric", heapPerDecoded[ProfileResolution](t, marshal(r1), 2000), r1)
	add("resolution 10 metrics", heapPerDecoded[ProfileResolution](t, marshal(r10), 2000), r10)
	add("resolution 20 rich metrics", heapPerDecoded[ProfileResolution](t, marshal(r20), 2000), r20)
	short := measuredMetrics(100, false) // the worst ratio: keys of one character
	for i := range short.Metrics {
		short.Metrics[i].MetricKey = "k"
		short.Metrics[i].ID = uint(i % 10)
	}
	add("resolution 100 short-key metrics", heapPerDecoded[ProfileResolution](t, marshal(short), 500), short)
	add("resolution 100 metrics", heapPerDecoded[ProfileResolution](t, marshal(r100), 500), r100)
	current = relationshipsCharge
	add("relationships 5", heapPerDecoded[EntityRelationshipSearchResults](t, marshal(rels), 2000), rels)
	current = membershipsCharge
	add("memberships 50", heapPerDecoded[[]GroupMembership](t, marshal(memberships), 2000), memberships)
	add("memberships 0", heapPerDecoded[[]GroupMembership](t, []byte("[]"), 2000), []GroupMembership{})
	current = scopedGroupsCharge
	add("bool", heapPerDecoded[bool](t, []byte("true"), 2000), true)

	for _, c := range cases {
		charge := float64(c.charge.bytes(c.enc))
		t.Logf("%-28s encoded %5d B  heap %7.0f B  charge %6.0f B  (%.2fx encoded)", c.name, c.enc, c.heap, charge, c.heap/float64(c.enc))
		// Tolerance: the heap read includes allocator size-class rounding and runs
		// concurrently with the test runtime, so allow 5% under.
		if charge < c.heap*0.95 {
			t.Errorf("%s: charge %.0f B is below the measured heap %.0f B", c.name, charge, c.heap)
		}
	}
}
