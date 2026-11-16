// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package credbinding

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestGuard(t *testing.T) {
	unavailable := errors.New("unavailable")
	before := New("mqtt").With("urls", "private-value")
	after := New("kafka").With("urls", "other-private-value")
	for _, tc := range []struct {
		name                        string
		named, exists, broken, want bool
	}{
		{"refuse", false, true, false, true}, {"replace", true, true, false, false}, {"anonymous", false, false, false, false}, {"store failure", false, false, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			called := false
			err := Guard("connector", "sink", before, after, tc.named, func() (bool, error) {
				called = true
				if tc.broken {
					return false, unavailable
				}
				return tc.exists, nil
			})
			if tc.broken && !errors.Is(err, unavailable) {
				t.Fatalf("store error lost: %v", err)
			}
			if (err != nil) != tc.want {
				t.Fatalf("error=%v", err)
			}
			if tc.named && called {
				t.Fatal("unneeded store read")
			}
			if err != nil && strings.Contains(err.Error(), "private-value") {
				t.Fatal("value leaked")
			}
			var refusal *ResupplyRequiredError
			if tc.name == "refuse" {
				if !errors.As(err, &refusal) || refusal.Extensions()["code"] != Code || !reflect.DeepEqual(refusal.Changed, []string{"type", "urls"}) {
					t.Fatalf("refusal=%v", err)
				}
			}
		})
	}
	if err := Guard("connector", "sink", before, before, false, func() (bool, error) { t.Fatal("unchanged read store"); return false, nil }); err != nil {
		t.Fatal(err)
	}
}
func TestOpaqueAndCopy(t *testing.T) {
	a := New("mqtt")
	b := a.With("urls", "one")
	if len(a.Diff(b)) != 1 {
		t.Fatal("With mutated baseline")
	}
	if len(Opaque("x", []byte("one")).Diff(Opaque("x", []byte("two")))) != 1 {
		t.Fatal("opaque change ignored")
	}
	if len(Opaque("x", []byte("one")).Diff(Opaque("x", []byte("one")))) != 0 {
		t.Fatal("opaque no-op changed")
	}
}

func TestUnclassifiedFieldsBindByDefault(t *testing.T) {
	type config struct {
		Destination string `json:"destination"`
		Label       string `json:"label"`
	}
	neutral := map[string]bool{"label": true}
	before := Fields(New("sink"), &config{Destination: "one", Label: "a"}, neutral)
	after := Fields(New("sink"), &config{Destination: "two", Label: "a"}, neutral)
	if !reflect.DeepEqual(before.Diff(after), []string{"destination"}) {
		t.Fatal("unclassified field ignored")
	}
	if len(before.Diff(Fields(New("sink"), &config{Destination: "one", Label: "b"}, neutral))) != 0 {
		t.Fatal("neutral edit bound")
	}
}

func TestBindingDiffNamesAreSortedWithTypeFirst(t *testing.T) {
	before := New("one").With("z", "old")
	after := New("two").With("a", "new")
	if got := before.Diff(after); !reflect.DeepEqual(got, []string{"type", "a", "z"}) {
		t.Fatalf("changed=%v", got)
	}
}
