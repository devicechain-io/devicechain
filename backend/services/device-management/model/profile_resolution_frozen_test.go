// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"github.com/devicechain-io/dc-microservice/resolvedevent"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"gorm.io/datatypes"
	"gorm.io/gorm"
)

// copiesWhole reports whether a value of t, copied, shares no memory with the original: it
// holds no pointer, slice, map, channel, function, interface or unsafe pointer, however
// deep. A string is immutable, so sharing its bytes is not sharing anything writable.
func copiesWhole(t reflect.Type) bool {
	switch t.Kind() {
	case reflect.Pointer, reflect.Slice, reflect.Map, reflect.Chan, reflect.Func,
		reflect.Interface, reflect.UnsafePointer:
		return false
	case reflect.Array:
		return copiesWhole(t.Elem())
	case reflect.Struct:
		for i := 0; i < t.NumField(); i++ {
			if !copiesWhole(t.Field(i).Type) {
				return false
			}
		}
	}
	return true
}

// TestCopiesWholeSeesEveryReference is copiesWhole's negative control: the shape test below
// passes trivially if copiesWhole stops recognizing a reference kind, since nothing that
// returns one exists today for it to catch.
func TestCopiesWholeSeesEveryReference(t *testing.T) {
	type nested struct {
		ok  [2]int
		ref struct{ p *int }
	}
	shares := map[string]reflect.Type{
		"pointer":       reflect.TypeOf((*ResolvedMetric)(nil)),
		"slice":         reflect.TypeOf([]string(nil)),
		"map":           reflect.TypeOf(map[string]int(nil)),
		"chan":          reflect.TypeOf((chan int)(nil)),
		"func":          reflect.TypeOf((func())(nil)),
		"interface":     reflect.TypeOf((*error)(nil)).Elem(),
		"array of ptr":  reflect.TypeOf([1]*int{}),
		"nested struct": reflect.TypeOf(nested{}),
	}
	for name, typ := range shares {
		if copiesWhole(typ) {
			t.Errorf("copiesWhole(%s) = true, but a copy of a %s shares memory", typ, name)
		}
	}
	for _, typ := range []reflect.Type{reflect.TypeOf(ProfileScope{}), reflect.TypeOf(""), reflect.TypeOf([3]int{})} {
		if !copiesWhole(typ) {
			t.Errorf("copiesWhole(%s) = false, but it holds no reference", typ)
		}
	}
}

// TestAResolutionHandsOutNothingWritable pins the shape that makes a cached resolution
// safe to share without a copy (see ProfileResolution). The compiler enforces each part
// once it is in place; this fails when a change takes one away:
//   - an exported field on ProfileResolution or ResolvedMetric, which a holder could set,
//     or through which it could reach the shared slice and map;
//   - an accessor that hands out a reference (an Enum() returning the slice, a Metrics()
//     returning the metrics, a *ResolvedMetric into them) rather than a value that copies
//     whole or one of these two read-only types;
//   - a pointer-receiver method on ProfileResolution beyond UnmarshalJSON (which refuses);
//     package code holding the cache's shared pointer could call one;
//   - a reference-holding field added to ProfileScope, which Scope() returns by copy.
func TestAResolutionHandsOutNothingWritable(t *testing.T) {
	resolution := reflect.TypeOf(ProfileResolution{})
	metric := reflect.TypeOf(ResolvedMetric{})
	for _, typ := range []reflect.Type{resolution, metric} {
		for i := 0; i < typ.NumField(); i++ {
			if f := typ.Field(i); f.IsExported() {
				t.Errorf("%s.%s is exported: a holder of a shared resolution could write it", typ.Name(), f.Name)
			}
		}
	}
	if !copiesWhole(reflect.TypeOf(ProfileScope{})) {
		t.Error("ProfileScope holds a reference, so Scope()'s copy shares it with the cached resolution")
	}

	// A Calculation shares the resolution's compiled plan (immutable: compiled formulas
	// and their Env are safe for concurrent use and nothing writes the plan after freeze)
	// and owns a pooled sample nothing else holds. Its fields are unexported, so a holder
	// reaches neither through it.
	calculation := reflect.TypeOf(Calculation{})
	for i := 0; i < calculation.NumField(); i++ {
		if f := calculation.Field(i); f.IsExported() {
			t.Errorf("Calculation.%s is exported: a holder could reach the shared plan", f.Name)
		}
	}
	readOnly := map[reflect.Type]bool{resolution: true, metric: true, calculation: true,
		reflect.TypeOf((*error)(nil)).Elem(): true}
	for _, typ := range []reflect.Type{resolution, reflect.PointerTo(resolution), reflect.PointerTo(metric)} {
		for i := 0; i < typ.NumMethod(); i++ {
			m := typ.Method(i)
			if m.Name == "MarshalJSON" {
				continue // a new []byte each call, encoding the value
			}
			if m.Name == "Recalculate" {
				// A new slice each call, holding the caller's own entries and freshly
				// stamped derived ones (TestRecalculatedEntriesAreTheCallers).
				continue
			}
			if m.Name == "ConstantKeys" || m.Name == "DeviceConstants" {
				// A clone of the plan's names, and constants freshly read from the caller's
				// metadata: neither is the resolution's memory
				// (TestConstantAccessorsHandOutCopies).
				continue
			}
			for o := 0; o < m.Type.NumOut(); o++ {
				if out := m.Type.Out(o); !readOnly[out] && !copiesWhole(out) {
					t.Errorf("%s.%s returns %s, which shares memory with the resolution", typ, m.Name, out)
				}
			}
		}
	}
	valueMethods := map[string]bool{}
	for i := 0; i < resolution.NumMethod(); i++ {
		valueMethods[resolution.Method(i).Name] = true
	}
	ptr := reflect.PointerTo(resolution)
	for i := 0; i < ptr.NumMethod(); i++ {
		if name := ptr.Method(i).Name; !valueMethods[name] && name != "UnmarshalJSON" {
			t.Errorf("(*ProfileResolution).%s has a pointer receiver: it could write to a shared resolution", name)
		}
	}
}

// TestCachedHitsShareOneResolution: once the in-process tier holds a resolution, every hit
// is that one value, metrics and index included, with no copy made per event. And a
// publish replaces it whole: a value read before the publish still reads as it did.
func TestCachedHitsShareOneResolution(t *testing.T) {
	r := newProfileResolutionRig(t)
	r.read(t) // a miss: the database answers and the bucket is filled
	first := r.read(t)
	if first.MetricCount() != 1 || unitOf(t, first) != "Cel" {
		t.Fatalf("first hit = %d metrics, unit %q", first.MetricCount(), unitOf(t, first))
	}
	for i := 0; i < 3; i++ {
		got := r.read(t)
		if &got.metrics[0] != &first.metrics[0] || reflect.ValueOf(got.byKey).Pointer() != reflect.ValueOf(first.byKey).Pointer() {
			t.Fatalf("hit %d built its own metrics or index: the cached resolution was copied", i)
		}
	}

	r.editUnitAndPublish(t, "K")
	after := r.read(t)
	if unitOf(t, after) != "K" || after.Scope().ProfileVersionToken != "p@2" {
		t.Fatalf("after publish = %q unit %q, want p@2 with K", after.Scope().ProfileVersionToken, unitOf(t, after))
	}
	if unitOf(t, first) != "Cel" || first.Scope().ProfileVersionToken != "p@1" {
		t.Fatalf("a resolution read before the publish now reads %q unit %q: it was changed in place",
			first.Scope().ProfileVersionToken, unitOf(t, first))
	}
}

// TestConcurrentHitsReadOneVersionWhilePublishing reads one shared resolution from many
// goroutines while publishes replace it. Under -race it fails if any read path writes to
// the shared value (an index built lazily on first use, say), and each read must name one
// version throughout: version p@n carries the unit published with it.
func TestConcurrentHitsReadOneVersionWhilePublishing(t *testing.T) {
	r := newProfileResolutionRig(t)
	unitOfVersion := func(token string) string {
		n, err := strconv.Atoi(strings.TrimPrefix(token, "p@"))
		if err != nil {
			return "?" + token
		}
		if n == 1 {
			return "Cel"
		}
		return fmt.Sprintf("u%d", n)
	}
	stop := make(chan struct{})
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				res, err := r.capi.ProfileResolutionByDeviceType(r.ctx, r.typeId)
				if err != nil {
					t.Errorf("read: %v", err)
					return
				}
				m, ok := res.Metric("temp")
				unit, _ := m.Unit()
				if want := unitOfVersion(res.Scope().ProfileVersionToken); !ok || unit != want {
					t.Errorf("%s reads unit %q, want %q", res.Scope().ProfileVersionToken, unit, want)
					return
				}
				if err := res.ValidateMeasurement("temp", "21.5"); err != nil {
					t.Errorf("validate: %v", err)
					return
				}
			}
		}()
	}
	for v := 2; v <= 6; v++ {
		time.Sleep(5 * time.Millisecond)
		r.editUnitAndPublish(t, fmt.Sprintf("u%d", v))
	}
	close(stop)
	wg.Wait()
}

// TestProfileResolutionWireIsUnchanged: the encoded form is the one the resolution had when
// its fields were exported, both ways, so a bucket entry written by either reads the same
// during a rolling update.
func TestProfileResolutionWireIsUnchanged(t *testing.T) {
	const wire = `{"Scope":{"DeviceTypeToken":"dt","ProfileVersionToken":"p@3","FenceSetVersion":4},` +
		`"Metrics":[{"id":7,"key":"temp","type":"DOUBLE","unit":"Cel","min":-40,"max":125,"enum":["a","b"]},` +
		`{"id":8,"key":"on","type":"BOOLEAN"}]}`
	enum := datatypes.JSON(`["a","b"]`)
	temp := &MetricDefinition{MetricKey: "temp", DataType: "DOUBLE", Unit: sql.NullString{String: "Cel", Valid: true},
		MinValue: sql.NullFloat64{Float64: -40, Valid: true}, MaxValue: sql.NullFloat64{Float64: 125, Valid: true}, Enum: &enum}
	temp.ID = 7
	on := &MetricDefinition{MetricKey: "on", DataType: "BOOLEAN"}
	on.ID = 8
	built := NewProfileResolution(ProfileScope{DeviceTypeToken: "dt", ProfileVersionToken: "p@3", FenceSetVersion: 4},
		[]*MetricDefinition{temp, on})

	for name, v := range map[string]any{"value": built, "pointer": &built} {
		got, err := json.Marshal(v)
		if err != nil || string(got) != wire {
			t.Fatalf("%s encodes as\n%s (%v)\nwant\n%s", name, got, err, wire)
		}
	}
	// The empty resolution (an untyped or unpublished device type, the common case) as the
	// exported-field form encoded it, checked against that form's own encoder.
	const emptyWire = `{"Scope":{"DeviceTypeToken":"","ProfileVersionToken":"","FenceSetVersion":2},"Metrics":[]}`
	if got, err := json.Marshal(NewProfileResolution(ProfileScope{FenceSetVersion: 2}, nil)); err != nil || string(got) != emptyWire {
		t.Fatalf("the empty resolution encodes as\n%s (%v)\nwant\n%s", got, err, emptyWire)
	}
	if empty, err := decodeProfileResolution([]byte(emptyWire)); err != nil || empty.MetricCount() != 0 || empty.Scope().FenceSetVersion != 2 {
		t.Fatalf("the empty wire decodes as %+v (%v)", empty, err)
	}

	decoded, err := decodeProfileResolution([]byte(wire))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(*decoded, built) {
		t.Fatalf("decoded\n%+v\nwant\n%+v", *decoded, built)
	}
	if m, ok := decoded.Metric("temp"); !ok || m.ID() != 7 || m.DataType() != "DOUBLE" {
		t.Fatalf("decoded temp = %+v, %v", m, ok)
	}
	if err := decoded.ValidateMeasurement("temp", "c"); err == nil {
		t.Fatal("the decoded enum and bounds did not validate")
	}
}

// TestDecodingIntoAResolutionRefuses: json.Unmarshal into a resolution is an error and
// leaves it as it was, never an empty resolution that reads as one declaring nothing.
func TestDecodingIntoAResolutionRefuses(t *testing.T) {
	res := NewProfileResolution(ProfileScope{ProfileVersionToken: "p@1"},
		[]*MetricDefinition{{MetricKey: "temp", DataType: "DOUBLE"}})
	before := res
	if err := json.Unmarshal([]byte(`{"Scope":{},"Metrics":[]}`), &res); err == nil {
		t.Fatal("decoding into a resolution succeeded")
	}
	if !reflect.DeepEqual(res, before) {
		t.Fatal("a refused decode changed the resolution")
	}
}

// TestAKeyDeclaredTwiceResolvesToItsLast keeps the index's answer for a duplicated key
// what the per-event map gave before it.
func TestAKeyDeclaredTwiceResolvesToItsLast(t *testing.T) {
	res := NewProfileResolution(ProfileScope{}, []*MetricDefinition{
		{MetricKey: "temp", DataType: "DOUBLE"}, {MetricKey: "temp", DataType: "INTEGER"}})
	if m, ok := res.Metric("temp"); !ok || m.DataType() != "INTEGER" {
		t.Fatalf("temp = %q, %v; want the last declaration", m.DataType(), ok)
	}
	if res.MetricCount() != 2 {
		t.Fatalf("count = %d, want 2", res.MetricCount())
	}
	var zero ProfileResolution
	if _, ok := zero.Metric("temp"); ok || zero.MetricCount() != 0 || zero.ValidateMeasurement("temp", "x") != nil {
		t.Fatal("the zero resolution declares something")
	}
}

// TestRecalculatedEntriesAreTheCallers: what Recalculate returns shares nothing with the
// resolution. Writing through a derived entry's stamp pointers, or into the slice, changes
// no later recalculation.
func TestRecalculatedEntriesAreTheCallers(t *testing.T) {
	unit := sql.NullString{String: "kW", Valid: true}
	r := NewProfileResolution(ProfileScope{}, []*MetricDefinition{
		{Model: gorm.Model{ID: 1}, MetricKey: "v", DataType: string(MetricDouble)},
		{Model: gorm.Model{ID: 2}, MetricKey: "p", DataType: string(MetricDouble), Unit: unit,
			Expression: sql.NullString{String: "v * 2.0", Valid: true}},
	})
	entries := []resolvedevent.ResolvedMeasurementEntry{{Name: "v", Value: "3"}}
	first := r.Recalculate(entries, nil, nopObserver{})
	if len(first) != 2 || first[0].Name != "p" || first[0].Value != "6" {
		t.Fatalf("recalculated %+v", first)
	}
	*first[0].Unit, *first[0].DataType, *first[0].Classifier = "W", "INT", 99
	first[0].Value = "x"
	again := r.Recalculate(entries, nil, nopObserver{})
	if *again[0].Unit != "kW" || *again[0].DataType != "DOUBLE" || *again[0].Classifier != 2 || again[0].Value != "6" {
		t.Errorf("a recalculated entry pointed into the resolution: %+v", again[0])
	}
}
