// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package intent

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
)

// checkRoundTrip is the property both fuzz targets assert of a valid spec: its canonical bytes
// parse back to the same spec, canonicalize to the same bytes (canonicalization is idempotent),
// fit the size limit, and digest to SHA-256 of those bytes.
func checkRoundTrip(t *testing.T, s Spec) {
	t.Helper()
	canonical, err := Canonical(s)
	if err != nil {
		t.Fatalf("a valid spec has no canonical form: %v", err)
	}
	if len(canonical) > MaxSpecBytes {
		t.Fatalf("canonical form is %d bytes, over MaxSpecBytes", len(canonical))
	}
	again, err := Parse(canonical)
	if err != nil {
		t.Fatalf("the canonical form does not parse: %v\n%s", err, canonical)
	}
	if !reflect.DeepEqual(again, s) {
		t.Fatalf("the canonical form parses to a different spec:\n got %+v\nwant %+v", again, s)
	}
	twice, err := Canonical(again)
	if err != nil || string(twice) != string(canonical) {
		t.Fatalf("canonicalization is not idempotent:\n%s\n%s (%v)", canonical, twice, err)
	}
	digest, err := SpecDigest(s)
	sum := sha256.Sum256(canonical)
	if err != nil || digest != "sha256:"+hex.EncodeToString(sum[:]) {
		t.Fatalf("SpecDigest = %s, %v; not SHA-256 of the canonical bytes", digest, err)
	}
}

// FuzzParseCanonical feeds arbitrary documents to Parse. Whatever it accepts must round-trip;
// whatever it refuses must be refused as an invalid spec, never by a panic.
func FuzzParseCanonical(f *testing.F) {
	f.Add([]byte(validDoc))
	f.Add([]byte(with(`{"valueType":"LONG","value":"40"}`, `{"unset":true}`)))
	f.Add([]byte(with(`"value":"40"`, `"value":"😀\\u0000\n"`)))
	f.Add([]byte(with(`"class":"SCHEDULE"`, `"class":"SCHEDULE","class":"OPTIMIZATION"`)))
	f.Add([]byte(`{"a":[1,{"b":null}]}`))
	f.Add([]byte(`"\ud800"`))
	for _, v := range loadVectors(f).Specs {
		f.Add([]byte(v.Input))
		f.Add([]byte(v.Canonical))
	}
	f.Fuzz(func(t *testing.T, doc []byte) {
		s, err := Parse(doc)
		if err != nil {
			if !errors.Is(err, ErrInvalidSpec) {
				t.Fatalf("Parse refused without ErrInvalidSpec: %v", err)
			}
			return
		}
		checkRoundTrip(t, s)
	})
}

// FuzzSpecRoundTrip builds specs field by field, so the fuzzer reaches valid specs Parse rarely
// sees from random bytes. A spec Validate accepts must round-trip, and must also parse from
// encoding/json's own rendering of it (so the canonical form is not the only spelling accepted).
func FuzzSpecRoundTrip(f *testing.F) {
	f.Add("quay-lamps", int64(3), "dim", "LONG", "40", false, "SCHEDULE", int64(2000), int64(60), int64(500), int64(4), int64(3600))
	f.Add("g", int64(1), "k", "", "", true, "OPTIMIZATION", int64(1), int64(86400), int64(10000), int64(1440), int64(0))
	f.Add("Signs_01", int64(7), "banner", "STRING", "quay \"east\"\\\né ", false, "SCHEDULE", int64(9), int64(61), int64(1), int64(1), int64(1))
	f.Add("g", int64(2), "j", "JSON", `{"b": 1, "a": [true, null]}`, false, "SCHEDULE", int64(9), int64(61), int64(1), int64(1), int64(1))
	f.Add("g", int64(2), "d", "DOUBLE", "-0.0e-3", false, "SCHEDULE", int64(9), int64(61), int64(1), int64(1), int64(1))
	f.Fuzz(func(t *testing.T, group string, version int64, key, valueType, value string, unset bool,
		class string, maxDevices, interval, lines, actuations, dwell int64) {
		s := Spec{
			Target:  Target{GroupToken: group, GroupVersion: version},
			Point:   Point{ConfigKey: key},
			Value:   Value{ValueType: valueType, Value: &value, Unset: unset},
			Class:   Class(class),
			Ceiling: Ceiling{MaxDevices: maxDevices},
			Reconcile: Reconcile{MinIntervalSeconds: interval, MaxLinesPerReconcile: lines,
				MaxActuationsPerDevicePerDay: actuations, ManualDwellSeconds: dwell},
		}
		if unset {
			s.Value = Value{Unset: true}
		}
		if err := s.Validate(); err != nil {
			if !errors.Is(err, ErrInvalidSpec) {
				t.Fatalf("Validate refused without ErrInvalidSpec: %v", err)
			}
			return
		}
		checkRoundTrip(t, s)
		plain, err := json.Marshal(s)
		if err != nil {
			t.Fatal(err)
		}
		fromPlain, err := Parse(plain)
		if err != nil {
			if len(plain) > MaxSpecBytes {
				return // encoding/json's escaping of <, >, & can be longer than the canonical form
			}
			t.Fatalf("encoding/json's rendering of a valid spec does not parse: %v\n%s", err, plain)
		}
		if !reflect.DeepEqual(fromPlain, s) {
			t.Fatalf("encoding/json's rendering parses to a different spec:\n got %+v\nwant %+v", fromPlain, s)
		}
	})
}
