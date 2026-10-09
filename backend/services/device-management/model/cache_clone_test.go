// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"encoding/json"
	"fmt"
	"reflect"
	"testing"
	"time"

	"gorm.io/datatypes"
)

// The tests below hold the clone functions in cache_clone.go to their one contract: the
// clone equals its argument and shares no memory with it. They do it by reflection over
// the TYPE, so a field added to one of these structs is filled in, compared and walked
// without anyone remembering to extend the test.

var (
	timeType = reflect.TypeOf(time.Time{})
	jsonType = reflect.TypeOf(datatypes.JSON{})
)

// fillAll sets every settable field reachable from v to a distinct non-zero value: strings,
// numbers, booleans, times, and pointers, slices (two elements) and maps (one entry)
// allocated. A field named in skip (by "Type.Field") is left alone. depth bounds recursion
// through types that refer to themselves.
func fillAll(v reflect.Value, seq *int, skip map[string]bool, depth int) {
	*seq++
	n := *seq
	switch v.Kind() {
	case reflect.String:
		v.SetString(fmt.Sprintf("s%d", n))
	case reflect.Bool:
		v.SetBool(true)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		v.SetInt(int64(n))
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		v.SetUint(uint64(n))
	case reflect.Float32, reflect.Float64:
		v.SetFloat(float64(n) + 0.5)
	case reflect.Ptr:
		if depth > 6 {
			return
		}
		v.Set(reflect.New(v.Type().Elem()))
		fillAll(v.Elem(), seq, skip, depth+1)
	case reflect.Slice:
		if v.Type() == jsonType {
			v.Set(reflect.ValueOf(datatypes.JSON(fmt.Sprintf(`{"n":%d}`, n))))
			return
		}
		if depth > 6 {
			return
		}
		v.Set(reflect.MakeSlice(v.Type(), 2, 2))
		for i := 0; i < 2; i++ {
			fillAll(v.Index(i), seq, skip, depth+1)
		}
	case reflect.Map:
		v.Set(reflect.MakeMap(v.Type()))
		k, e := reflect.New(v.Type().Key()).Elem(), reflect.New(v.Type().Elem()).Elem()
		fillAll(k, seq, skip, depth+1)
		fillAll(e, seq, skip, depth+1)
		v.SetMapIndex(k, e)
	case reflect.Struct:
		if v.Type() == timeType {
			v.Set(reflect.ValueOf(time.Unix(1_700_000_000+int64(n), 0).UTC()))
			return
		}
		for i := 0; i < v.NumField(); i++ {
			f := v.Type().Field(i)
			if !f.IsExported() || skip[v.Type().Name()+"."+f.Name] {
				continue
			}
			fillAll(v.Field(i), seq, skip, depth+1)
		}
	default:
		panic(fmt.Sprintf("fillAll: unhandled kind %s in %s", v.Kind(), v.Type()))
	}
}

// addresses records every pointer, slice backing array and map reachable from v.
func addresses(v reflect.Value, path string, into map[uintptr]string) {
	switch v.Kind() {
	case reflect.Ptr:
		if v.IsNil() {
			return
		}
		into[v.Pointer()] = path
		addresses(v.Elem(), path+"*", into)
	case reflect.Slice:
		if v.Len() == 0 {
			return
		}
		into[v.Pointer()] = path
		for i := 0; i < v.Len(); i++ {
			addresses(v.Index(i), fmt.Sprintf("%s[%d]", path, i), into)
		}
	case reflect.Map:
		if v.IsNil() {
			return
		}
		into[v.Pointer()] = path
		for _, k := range v.MapKeys() {
			addresses(v.MapIndex(k), fmt.Sprintf("%s[%v]", path, k), into)
		}
	case reflect.Struct:
		if v.Type() == timeType {
			return
		}
		for i := 0; i < v.NumField(); i++ {
			if v.Type().Field(i).IsExported() {
				addresses(v.Field(i), path+"."+v.Type().Field(i).Name, into)
			}
		}
	}
}

// requireNoZeroFields fails when any exported field reachable from v is still its zero
// value, which is what makes a "fully populated" fixture mean it.
func requireNoZeroFields(t *testing.T, v reflect.Value, path string, skip map[string]bool) {
	t.Helper()
	switch v.Kind() {
	case reflect.Ptr:
		if v.IsNil() {
			t.Errorf("%s is nil in the fixture", path)
			return
		}
		requireNoZeroFields(t, v.Elem(), path+"*", skip)
	case reflect.Slice:
		if v.Len() == 0 {
			t.Errorf("%s is empty in the fixture", path)
			return
		}
		for i := 0; i < v.Len(); i++ {
			requireNoZeroFields(t, v.Index(i), fmt.Sprintf("%s[%d]", path, i), skip)
		}
	case reflect.Struct:
		if v.Type() == timeType {
			if v.IsZero() {
				t.Errorf("%s is the zero time in the fixture", path)
			}
			return
		}
		for i := 0; i < v.NumField(); i++ {
			f := v.Type().Field(i)
			if !f.IsExported() || skip[v.Type().Name()+"."+f.Name] {
				continue
			}
			requireNoZeroFields(t, v.Field(i), path+"."+f.Name, skip)
		}
	case reflect.Map:
		if v.Len() == 0 {
			t.Errorf("%s is empty in the fixture", path)
		}
	default:
		if v.IsZero() {
			t.Errorf("%s is zero in the fixture", path)
		}
	}
}

func checkClone[T any](t *testing.T, name string, clone func(*T) *T, skip map[string]bool) {
	t.Helper()
	t.Run(name, func(t *testing.T) {
		var orig T
		seq := 0
		fillAll(reflect.ValueOf(&orig).Elem(), &seq, skip, 0)
		requireNoZeroFields(t, reflect.ValueOf(&orig).Elem(), name, skip)

		got := clone(&orig)
		if !reflect.DeepEqual(&orig, got) {
			t.Fatalf("clone differs from its argument:\n orig %+v\n got  %+v", orig, *got)
		}
		a, b := map[uintptr]string{}, map[uintptr]string{}
		addresses(reflect.ValueOf(&orig), name, a)
		addresses(reflect.ValueOf(got), name, b)
		if len(a) == 0 {
			t.Fatal("the fixture holds no pointer, slice or map: the walk proved nothing")
		}
		for addr, path := range b {
			if orig, shared := a[addr]; shared {
				t.Errorf("clone shares memory with its argument at %s (same address as %s)", path, orig)
			}
		}
	})
}

// TestCacheClonesShareNoMemory fills every field of every cached type, clones it, and
// requires an equal value that shares no address with the original. It fails for a field
// added to a cached struct as a pointer, slice or map without the clone copying it: the
// struct copy carries the pointer across, and the walk finds the shared address.
func TestCacheClonesShareNoMemory(t *testing.T) {
	// The profile and the device list under a cached device's type are never loaded on the
	// per-event path and are cloned through their JSON form; the test below covers them.
	deviceSkip := map[string]bool{"DeviceType.Profile": true, "DeviceType.Devices": true}

	checkClone(t, "Device", cloneCachedDevice, deviceSkip)
	checkClone(t, "ProfileResolution", cloneProfileResolution, nil)
	checkClone(t, "EntityRelationshipSearchResults", cloneRelationshipResults, nil)
	checkClone(t, "Memberships", cloneMemberships, nil)
	checkClone(t, "bool", cloneBool, nil)
}

// TestCachedDeviceTypeProfileCloneIsIndependent covers the rare branches of a cached
// device: a type that carries its profile, or its devices.
func TestCachedDeviceTypeProfileCloneIsIndependent(t *testing.T) {
	var orig Device
	seq := 0
	// DeviceProfile.Devices etc. are cyclic through DeviceType; fill the profile on its own.
	skip := map[string]bool{"DeviceType.Profile": true, "DeviceType.Devices": true}
	fillAll(reflect.ValueOf(&orig).Elem(), &seq, skip, 0)
	var profile DeviceProfile
	fillAll(reflect.ValueOf(&profile).Elem(), &seq, nil, 4)
	orig.DeviceType.Profile = &profile
	var peer Device
	fillAll(reflect.ValueOf(&peer).Elem(), &seq, skip, 3)
	orig.DeviceType.Devices = []Device{peer}

	got := cloneCachedDevice(&orig)
	want, _ := json.Marshal(&orig)
	if have, _ := json.Marshal(got); string(have) != string(want) {
		t.Fatalf("clone encodes differently:\n%s\n%s", have, want)
	}
	a, b := map[uintptr]string{}, map[uintptr]string{}
	addresses(reflect.ValueOf(&orig), "Device", a)
	addresses(reflect.ValueOf(got), "Device", b)
	for addr, path := range b {
		if o, shared := a[addr]; shared {
			t.Errorf("clone shares memory with its argument at %s (same address as %s)", path, o)
		}
	}
}

// TestCacheClonesPreserveNilAndEmpty: JSON decoding tells a nil slice from an empty one,
// and the clones keep the difference.
func TestCacheClonesPreserveNilAndEmpty(t *testing.T) {
	var nilMemberships []GroupMembership
	if got := cloneMemberships(&nilMemberships); *got != nil {
		t.Error("a nil membership list became non-nil")
	}
	empty := []GroupMembership{}
	if got := cloneMemberships(&empty); *got == nil || len(*got) != 0 {
		t.Error("an empty membership list became nil")
	}
	res := ProfileResolution{Metrics: []ResolvedMetric{{MetricKey: "a"}}}
	got := cloneProfileResolution(&res)
	if got.Metrics[0].Enum != nil || got.Metrics[0].Unit != nil {
		t.Error("an absent enum or unit became present")
	}
	if cloneProfileResolution(&ProfileResolution{}).Metrics != nil {
		t.Error("nil metrics became non-nil")
	}
}

// TestACachedReadCannotBeChangedByItsCaller reads a resolution through the cached API,
// writes to everything reachable from the result, and reads again: the cache must still
// hold what the database gave it. A clone that copied only the struct would fail here.
func TestACachedReadCannotBeChangedByItsCaller(t *testing.T) {
	r := newProfileResolutionRig(t)
	for i := 0; i < 3; i++ { // the first read fills; later ones are warm hits
		res := r.read(t)
		if got := unitOf(t, res); got != "Cel" {
			t.Fatalf("read %d: unit = %q, want Cel (an earlier caller's write leaked into the cache)", i, got)
		}
		m := &res.Metrics[0]
		*m.Unit = "MUTATED"
		m.MetricKey = "MUTATED"
		m.Enum = append(m.Enum, "MUTATED")
		res.Scope.ProfileVersionToken = "MUTATED"
		res.Metrics = append(res.Metrics, ResolvedMetric{MetricKey: "MUTATED"})
	}
}
