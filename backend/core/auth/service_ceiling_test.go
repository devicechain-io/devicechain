// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package auth

import (
	"reflect"
	"sort"
	"testing"
)

func TestServiceCeilingTableIsSound(t *testing.T) {
	names := ServiceCallerNames()
	if len(names) == 0 {
		t.Fatal("the ceiling table is empty")
	}
	if !sort.StringsAreSorted(names) {
		t.Fatalf("ServiceCallerNames is not sorted: %v", names)
	}
	for _, name := range names {
		if name == UnknownCallerLabel {
			t.Errorf("a caller is named %q, which is the label for 'not in the table'", name)
		}
		ceiling, ok := ServiceCeiling(name)
		if !ok || len(ceiling) == 0 {
			t.Errorf("%s: no ceiling", name)
		}
		seen := map[Authority]bool{}
		for _, a := range ceiling {
			if a == AuthorityAll {
				t.Errorf("%s: the ceiling contains the wildcard", name)
			}
			if !ValidAuthority(string(a)) {
				t.Errorf("%s: %q is not a known authority", name, a)
			}
			if seen[a] {
				t.Errorf("%s: %q listed twice", name, a)
			}
			seen[a] = true
		}
		// Every caller must be able to mint exactly its own ceiling.
		strs := make([]string, len(ceiling))
		for i, a := range ceiling {
			strs[i] = string(a)
		}
		if r := CheckServiceCeiling(name, strs); r.Verdict != CeilingWithin || r.Caller != name {
			t.Errorf("%s: its own full ceiling is judged %+v", name, r)
		}
	}
}

func TestServiceCeilingReturnsACopy(t *testing.T) {
	c, _ := ServiceCeiling("event-management")
	c[0] = AuthorityAll
	again, _ := ServiceCeiling("event-management")
	if again[0] == AuthorityAll {
		t.Fatal("mutating a returned ceiling widened the table")
	}
	if r := CheckServiceCeiling("event-management", []string{"device:read"}); r.Verdict != CeilingWithin {
		t.Fatalf("the table changed: %+v", r)
	}
	if _, ok := ServiceCeiling("nobody"); ok {
		t.Fatal("an unknown caller has a ceiling")
	}
}

func TestCheckServiceCeiling(t *testing.T) {
	cases := []struct {
		name      string
		subject   string
		req       []string
		caller    string
		verdict   ServiceCeilingVerdict
		offending []string
	}{
		{"within", "event-management", []string{"device:read"}, "event-management", CeilingWithin, nil},
		{"subset", "command-delivery", []string{"state:read"}, "command-delivery", CeilingWithin, nil},
		{"over", "event-management", []string{"device:read", "device:write"}, "event-management", CeilingOver, []string{"device:write"}},
		{"over de-duplicates", "event-management", []string{"device:write", "device:write"}, "event-management", CeilingOver, []string{"device:write"}},
		{"wildcard from a known caller", "event-management", []string{"device:read", "*"}, "event-management", CeilingWildcard, []string{"*"}},
		{"wildcard outranks unknown", "mallory", []string{"*"}, UnknownCallerLabel, CeilingWildcard, []string{"*"}},
		{"unknown", "mallory", []string{"device:read"}, UnknownCallerLabel, CeilingUnknownCaller, []string{"device:read"}},
		{"unknown, empty subject", "", []string{"tenant:read"}, UnknownCallerLabel, CeilingUnknownCaller, []string{"tenant:read"}},
		{"wildcard outranks over", "ai-inference", []string{"device:write", "*"}, "ai-inference", CeilingWildcard, []string{"device:write", "*"}},
		{"case matters", "Event-Management", []string{"device:read"}, UnknownCallerLabel, CeilingUnknownCaller, []string{"device:read"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := CheckServiceCeiling(tc.subject, tc.req)
			if got.Caller != tc.caller || got.Verdict != tc.verdict || !reflect.DeepEqual(got.Offending, tc.offending) {
				t.Fatalf("got %+v; want caller=%s verdict=%s offending=%v", got, tc.caller, tc.verdict, tc.offending)
			}
		})
	}
}

// The table is pinned to the call sites it was derived from. Adding a service, dropping one or
// widening a ceiling is a deliberate edit to BOTH this list and the table, in one review; a silent
// change to the table alone (a caller removed, an authority added) fails here.
func TestServiceCeilingTableIsPinned(t *testing.T) {
	want := map[string][]string{
		"ai-inference":            {"tenant:read"},
		"command-delivery":        {"device:read", "state:read", "tenant:read"},
		"dashboard-management":    {"device:read", "tenant:read"},
		"device-management":       {"device:read", "tenant:read"},
		"event-management":        {"device:read"},
		"event-processing":        {"ai:infer", "command:write", "device:read", "tenant:read"},
		"event-sources":           {"command:wake", "device:read", "state:read", "tenant:read"},
		"lwm2m-ingest":            {"command:claim", "command:park", "command:read", "device:read", "device:write", "state:read", "tenant:read"},
		"notification-management": {"tenant:read"},
		"outbound-connectors":     {"tenant:read"},
		"sparkplug-ingest":        {"command:claim", "command:park", "command:read", "device:read", "device:write", "state:read", "tenant:read"},
		"update-management":       {"device:read", "tenant:read"},
	}
	got := map[string][]string{}
	for _, name := range ServiceCallerNames() {
		c, _ := ServiceCeiling(name)
		strs := make([]string, len(c))
		for i, a := range c {
			strs[i] = string(a)
		}
		sort.Strings(strs)
		got[name] = strs
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("the ceiling table changed:\n got %v\nwant %v", got, want)
	}
}
