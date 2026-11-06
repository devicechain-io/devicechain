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

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/datatypes"
	"gorm.io/gorm"

	"github.com/devicechain-io/dc-device-management/config"
	"github.com/devicechain-io/dc-microservice/payloaddecode"
	"github.com/devicechain-io/dc-microservice/rdb"
)

// heapPerDecoded is the average heap, in bytes, that one decoded value of raw's JSON holds:
// n copies are decoded and kept live, and the heap is read before and after.
func heapPerDecoded[T any](t *testing.T, raw []byte, n int) float64 {
	t.Helper()
	return heapPer(t, raw, n, func(raw []byte) (*T, error) {
		v := new(T)
		return v, json.Unmarshal(raw, v)
	})
}

// heapPer is heapPerDecoded for a value the cache builds with decode rather than
// json.Unmarshal: what it holds is what decode built, an index included.
func heapPer[T any](t *testing.T, raw []byte, n int, decode func([]byte) (*T, error)) float64 {
	t.Helper()
	var sink = make([]*T, 0, n)
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	for i := 0; i < n; i++ {
		v, err := decode(raw)
		if err != nil {
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

// measuredMetrics is a resolution declaring n metrics, keyed metric_<i> or, with key set,
// all key (the worst ratio of heap to encoded length is keys of one character).
func measuredMetrics(n int, rich bool, key string) ProfileResolution {
	scope := ProfileScope{DeviceTypeToken: "thermostat", ProfileVersionToken: "thermostat-profile@12", FenceSetVersion: 3}
	var defs []*MetricDefinition
	for i := 0; i < n; i++ {
		d := &MetricDefinition{MetricKey: fmt.Sprintf("metric_%d", i), DataType: "DOUBLE"}
		d.ID = uint(i + 1)
		if key != "" {
			d.MetricKey, d.ID = key, uint(i%10)
		}
		if rich {
			enum := datatypes.JSON(`["a","b","c"]`)
			d.Unit = sql.NullString{String: "Cel", Valid: true}
			d.MinValue = sql.NullFloat64{Float64: -40, Valid: true}
			d.MaxValue = sql.NullFloat64{Float64: 125, Valid: true}
			d.Enum = &enum
		}
		defs = append(defs, d)
	}
	return NewProfileResolution(scope, defs)
}

// shortKeyMetrics is a resolution declaring n INT metrics under n DISTINCT keys of one or two
// characters, with one-digit ids: the least JSON per key-index entry, so the worst ratio of
// heap to encoded length a resolution reaches. (measuredMetrics's one-character fixture
// repeats ONE key, so its index holds one entry and prices none of the others.)
func shortKeyMetrics(n int) ProfileResolution {
	const chars = "abcdefghijklmnopqrstuvwxyz0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZ"
	var defs []*MetricDefinition
	for i := 0; i < n; i++ {
		key := string(chars[i%len(chars)])
		if i >= len(chars) {
			key += string(chars[(i/len(chars)-1)%len(chars)])
		}
		d := &MetricDefinition{MetricKey: key, DataType: "INT"}
		d.ID = uint(i % 10)
		defs = append(defs, d)
	}
	return NewProfileResolution(ProfileScope{}, defs)
}

// TestDecodedChargeCoversTheHeap measures what decoded values of representative sizes hold
// on the heap and requires the charge the caches make for them (see decodedCharge) to be at least that. The charge is what the byte bound counts for the
// decoded copy; if it fell below the heap the bound would promise less memory than the
// cache uses. Measured on Go 1.26, windows/amd64, the heap of a decoded value was up to
// 5.0 times its encoded length for a resolution (its key index included, with many
// distinct short keys) and about 1.3 times or less for the rest. The counterweight: the
// resolution's factor must stay within 1.5x of the worst ratio measured, so the charge
// cannot drift arbitrarily high either.
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
		// override is the charge the cache makes when it is not the factor's: the programs of a
		// payload decoder (resolutionChargeOf).
		override float64
	}
	var cases []measured
	var current decodedCharge
	add := func(name string, heap float64, v any) {
		raw, _ := json.Marshal(v)
		cases = append(cases, measured{name: name, heap: heap, enc: len(raw), charge: current})
	}
	marshal := func(v any) []byte { raw, _ := json.Marshal(v); return raw }

	r10, r100, r20 := measuredMetrics(10, false, ""), measuredMetrics(100, false, ""), measuredMetrics(20, true, "")
	r1 := measuredMetrics(1, false, "")
	// What the cache keeps for a resolution is what decodeProfileResolution builds: the
	// metrics and their key index.
	resolution := func(raw []byte, n int) float64 { return heapPer(t, raw, n, decodeProfileResolution) }
	current = deviceCharge
	add("device", heapPerDecoded[Device](t, marshal(device), 2000), device)
	current = resolutionCharge
	add("resolution 1 metric", resolution(marshal(r1), 2000), r1)
	add("resolution 10 metrics", resolution(marshal(r10), 2000), r10)
	add("resolution 20 rich metrics", resolution(marshal(r20), 2000), r20)
	short := measuredMetrics(100, false, "k")
	add("resolution 100 short-key metrics", resolution(marshal(short), 500), short)
	add("resolution 100 metrics", resolution(marshal(r100), 500), r100)
	// Many distinct short keys: the index's share is largest here. 62 and 484 sit just past
	// growths of the index's table, where (Go 1.26) the heap measured 4.8x and 5.0x the
	// encoding, above the 4x this cache was once charged.
	s62, s484 := shortKeyMetrics(62), shortKeyMetrics(484)
	add("resolution 62 distinct short keys", resolution(marshal(s62), 1000), s62)
	add("resolution 484 distinct short keys", resolution(marshal(s484), 100), s484)
	// A version with a payload decoder: the encoding holds the document, and the entry holds the
	// compiled programs of every expression in it. The document is a maximal one (the size
	// ceiling, every let and section the limits allow), because that is what the factor must
	// cover; it is what a tenant at the ceiling can put in every device type's entry.
	scope := ProfileScope{DeviceTypeToken: "thermostat", ProfileVersionToken: "thermostat-profile@12", FenceSetVersion: 3}
	_, metricDefs := maximalDecoder(t)
	for _, f := range []struct {
		name string
		doc  json.RawMessage
	}{
		{"resolution + maximal decoder", mustDoc(maximalDecoder(t))},
		{"resolution + 520 one-token expressions", tinyExpressionDecoder(t)},
		{"resolution + 17 dense expressions", denseDecoder(t)},
	} {
		built := NewProfileResolutionWithDecoder(scope, metricDefs, f.doc)
		require.NotNil(t, built.Decoder().Plan(), "%s does not compile: %v", f.name, built.Decoder().Unusable())
		add(f.name, resolution(marshal(built), 40), built)
		decoded, err := decodeProfileResolution(marshal(built))
		require.NoError(t, err)
		charged, ok := resolutionChargeOf(decoded, len(marshal(built)))
		require.True(t, ok)
		cases[len(cases)-1].override = float64(charged)
		// The counterweight: a decoder is charged for memory it holds, within 2x of it.
		if heap := cases[len(cases)-1].heap; float64(charged) > 2*heap {
			t.Errorf("%s: charged %d B for %.0f B held", f.name, charged, heap)
		}
	}
	current = relationshipsCharge
	add("relationships 5", heapPerDecoded[EntityRelationshipSearchResults](t, marshal(rels), 2000), rels)
	current = membershipsCharge
	add("memberships 50", heapPerDecoded[[]GroupMembership](t, marshal(memberships), 2000), memberships)
	add("memberships 0", heapPerDecoded[[]GroupMembership](t, []byte("[]"), 2000), []GroupMembership{})
	current = scopedGroupsCharge
	add("bool", heapPerDecoded[bool](t, []byte("true"), 2000), true)

	worstResolution := 0.0
	for _, c := range cases {
		if c.charge == resolutionCharge && c.override == 0 {
			worstResolution = max(worstResolution, (c.heap-float64(c.charge.base))/float64(c.enc))
		}
	}
	if limit := 1.5 * worstResolution; float64(resolutionCharge.factor) > limit {
		t.Errorf("the resolution factor %d is above 1.5x the worst ratio measured (%.2f): it charges for memory no resolution holds",
			resolutionCharge.factor, worstResolution)
	}
	for _, c := range cases {
		charge := float64(c.charge.bytes(c.enc))
		if c.override > 0 {
			charge = c.override
		}
		t.Logf("%-28s encoded %5d B  heap %7.0f B  charge %6.0f B  (%.2fx encoded)", c.name, c.enc, c.heap, charge, c.heap/float64(c.enc))
		// Tolerance: the heap read includes allocator size-class rounding and runs
		// concurrently with the test runtime, so allow 5% under.
		if charge < c.heap*0.95 {
			t.Errorf("%s: charge %.0f B is below the measured heap %.0f B", c.name, charge, c.heap)
		}
	}
}

// maximalDecoder is a decoder near the document ceiling with the declared metrics it writes:
// MaxLets bindings and MaxSections sections of 33 rows each, over 64 DOUBLE metrics.
func maximalDecoder(t *testing.T) (json.RawMessage, []*MetricDefinition) {
	t.Helper()
	var defs []*MetricDefinition
	for i := 0; i < 64; i++ {
		d := &MetricDefinition{MetricKey: fmt.Sprintf("measurement_%02d", i), DataType: "DOUBLE"}
		d.ID = uint(i + 1)
		defs = append(defs, d)
	}
	doc := payloaddecode.Document{Kind: payloaddecode.KindCEL, EnvVersion: payloaddecode.EnvVersion, Input: payloaddecode.InputFrame}
	for i := 0; i < payloaddecode.MaxLets; i++ {
		doc.Let = append(doc.Let, payloaddecode.Let{Name: fmt.Sprintf("header_%02d", i), Expr: fmt.Sprintf("u8(frame, %d) + u8(frame, %d) * 256", i, i+1)})
	}
	for s := 0; s < payloaddecode.MaxSections; s++ {
		sec := payloaddecode.Section{Name: fmt.Sprintf("section_%d", s), When: fmt.Sprintf("fPort == %d && header_00 >= 0", s+1)}
		for r := 0; r < 33; r++ {
			sec.Outputs = append(sec.Outputs, payloaddecode.Output{Key: fmt.Sprintf("measurement_%02d", r),
				Expr: fmt.Sprintf("double(i16be(frame, %d)) / 10.0 + double(header_%02d)", 2+r, r%payloaddecode.MaxLets),
				When: fmt.Sprintf("header_%02d != %d", r%payloaddecode.MaxLets, r)})
		}
		doc.Sections = append(doc.Sections, sec)
	}
	raw, err := json.Marshal(doc)
	require.NoError(t, err)
	size, err := payloaddecode.CanonicalSize(raw)
	require.NoError(t, err)
	require.LessOrEqual(t, size, payloaddecode.MaxDocumentBytes, "measured as the store renders it")
	require.Greater(t, len(raw), payloaddecode.MaxDocumentBytes*3/4, "the fixture is no longer near the ceiling")
	return raw, defs
}

func mustDoc(raw json.RawMessage, _ []*MetricDefinition) json.RawMessage { return raw }

// tinyExpressionDecoder is the other shape that matters: as many expressions as the limits allow,
// each as short as one can be (8 sections of 64 rows of `1.0`).
func tinyExpressionDecoder(t *testing.T) json.RawMessage {
	t.Helper()
	doc := payloaddecode.Document{Kind: payloaddecode.KindCEL, EnvVersion: payloaddecode.EnvVersion, Input: payloaddecode.InputFrame}
	for s := 0; s < payloaddecode.MaxSections; s++ {
		sec := payloaddecode.Section{Name: fmt.Sprintf("s%d", s)}
		if s > 0 {
			sec.When = fmt.Sprintf("fPort == %d", s)
		}
		for r := 0; r < 64; r++ {
			sec.Outputs = append(sec.Outputs, payloaddecode.Output{Key: fmt.Sprintf("measurement_%02d", r), Expr: "1.0"})
		}
		doc.Sections = append(doc.Sections, sec)
	}
	raw, err := json.Marshal(doc)
	require.NoError(t, err)
	return raw
}

// denseDecoder is few, long expressions: 14 lets of a hundred u8 reads summed in a balanced tree
// (the parser's recursion limit forbids a chain), which is the shape with the most program per
// expression.
func denseDecoder(t *testing.T) json.RawMessage {
	t.Helper()
	var tree func(lo, hi int) string
	tree = func(lo, hi int) string {
		if hi-lo == 1 {
			return fmt.Sprintf("u8(frame, %d)", lo%100)
		}
		mid := (lo + hi) / 2
		return "(" + tree(lo, mid) + " + " + tree(mid, hi) + ")"
	}
	doc := payloaddecode.Document{Kind: payloaddecode.KindCEL, EnvVersion: payloaddecode.EnvVersion, Input: payloaddecode.InputFrame}
	for i := 0; i < 14; i++ {
		doc.Let = append(doc.Let, payloaddecode.Let{Name: fmt.Sprintf("v%02d", i), Expr: tree(0, 100)})
	}
	doc.Sections = []payloaddecode.Section{{Name: "s", Outputs: []payloaddecode.Output{{Key: "measurement_00", Expr: "double(v00)"}}}}
	raw, err := json.Marshal(doc)
	require.NoError(t, err)
	return raw
}

// The configuration refuses a per-tenant share of the resolution cache that cannot hold one
// maximal decoder (config.MaxDecoderResolutionChargeBytes). That number is only worth something
// while it is at least what the largest decoder shapes are really charged, so it is measured here
// against the three the limits allow.
func TestTheConfiguredMaximalChargeCoversEveryDecoderShape(t *testing.T) {
	scope := ProfileScope{DeviceTypeToken: "thermostat", ProfileVersionToken: "thermostat-profile@12", FenceSetVersion: 3}
	_, metricDefs := maximalDecoder(t)
	for name, doc := range map[string]json.RawMessage{
		"maximal": mustDoc(maximalDecoder(t)),
		"tiny":    tinyExpressionDecoder(t),
		"dense":   denseDecoder(t),
	} {
		built := NewProfileResolutionWithDecoder(scope, metricDefs, doc)
		require.NotNil(t, built.Decoder().Plan(), "%s does not compile", name)
		enc, err := json.Marshal(built)
		require.NoError(t, err)
		charged, ok := resolutionChargeOf(&built, len(enc))
		require.True(t, ok)
		t.Logf("%s: charged %d B (limit %d B)", name, charged, config.MaxDecoderResolutionChargeBytes)
		assert.LessOrEqual(t, charged+len(enc)+localEntryOverheadAllowance, config.MaxDecoderResolutionChargeBytes,
			"%s decoder is charged more than the configuration assumes a maximal one is", name)
	}
}

// localEntryOverheadAllowance covers the entry's key and the per-entry bookkeeping the cache
// charges beside its encoded bytes and the decoded value.
const localEntryOverheadAllowance = 1 << 10
