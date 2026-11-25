// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package waves

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
)

// These expected assignments are literal vectors, independent of the rounding
// implementation. Fleet order deliberately differs from lexical order.
func TestAssignmentGoldenVectors(t *testing.T) {
	for _, tc := range []struct {
		name    string
		plan    []Cohort
		devices []string
		want    map[string]int
		refusal string
	}{
		{"no plan", nil, []string{"b", "a"}, map[string]int{}, ""},
		{"empty fleet without plan", nil, nil, map[string]int{}, ""},
		{"one device", []Cohort{{Name: "all", Percentage: 100}}, []string{"a"}, map[string]int{"a": 0}, ""},
		{"tiny fleet rounds up", []Cohort{{Name: "first", Percentage: 1}, {Name: "all", Percentage: 100}}, []string{"z", "a"}, map[string]int{"z": 0, "a": 1}, ""},
		{"cumulative rounding", []Cohort{{Name: "first", Percentage: 34}, {Name: "all", Percentage: 100}}, []string{"c", "b", "a"}, map[string]int{"c": 0, "b": 0, "a": 1}, ""},
		{"exact boundary", []Cohort{{Name: "half", Percentage: 50}, {Name: "all", Percentage: 100}}, []string{"d", "b", "c", "a"}, map[string]int{"d": 0, "b": 0, "c": 1, "a": 1}, ""},
		{"canaries count toward percentage", []Cohort{{Name: "canary", DeviceTokens: []string{"a"}}, {Name: "half", Percentage: 50}, {Name: "all", Percentage: 100}}, []string{"d", "b", "c", "a"}, map[string]int{"a": 0, "d": 1, "b": 2, "c": 2}, ""},
		{"multiple canary cohorts", []Cohort{{Name: "one", DeviceTokens: []string{"c"}}, {Name: "two", DeviceTokens: []string{"a"}}, {Name: "all", Percentage: 100}}, []string{"c", "b", "a"}, map[string]int{"c": 0, "a": 1, "b": 2}, ""},
		{"outside canary", []Cohort{{Name: "canary", DeviceTokens: []string{"other"}}, {Name: "all", Percentage: 100}}, []string{"a"}, nil, `canary "other" is outside the frozen fleet`},
		{"all canary boundary", []Cohort{{Name: "canary", DeviceTokens: []string{"b", "a"}}, {Name: "all", Percentage: 100}}, []string{"b", "a"}, nil, `cohort "all" is empty for this fleet`},
		{"canary fills percentage", []Cohort{{Name: "canary", DeviceTokens: []string{"a"}}, {Name: "half", Percentage: 50}, {Name: "all", Percentage: 100}}, []string{"b", "a"}, nil, `cohort "half" is empty for this fleet`},
		{"rounded cohorts collide", []Cohort{{Name: "first", Percentage: 1}, {Name: "next", Percentage: 2}, {Name: "all", Percentage: 100}}, []string{"b", "a"}, nil, `cohort "next" is empty for this fleet`},
		{"empty fleet with plan", []Cohort{{Name: "all", Percentage: 100}}, nil, nil, `cohort "all" is empty for this fleet`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			beforePlan, beforeDevices := fmt.Sprint(tc.plan), strings.Join(tc.devices, ",")
			if err := Validate(tc.plan); err != nil {
				t.Fatalf("invalid test plan: %v", err)
			}
			// Neither validation nor assignment may reorder caller-owned inputs.
			got, err := Assign(tc.plan, tc.devices)
			if tc.refusal != "" {
				if err == nil || err.Error() != tc.refusal || got != nil {
					t.Fatalf("got (%v, %v), want (nil, %q)", got, err, tc.refusal)
				}
			} else if err != nil || !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got (%v, %v), want (%v, nil)", got, err, tc.want)
			}
			if beforePlan != fmt.Sprint(tc.plan) || beforeDevices != strings.Join(tc.devices, ",") {
				t.Fatal("caller inputs changed")
			}
		})
	}
}

func TestValidationGoldenVectors(t *testing.T) {
	selector := "cohorts need unique names and exactly one selector"
	canary := "canaries must be unique and precede percentage cohorts"
	percent := "percentages must increase through 100"
	final := "the last cohort must cover 100 percent"
	all := Cohort{Name: "all", Percentage: 100}
	maxPlan := make([]Cohort, 20)
	for i := range maxPlan {
		maxPlan[i] = Cohort{Name: strings.Repeat("n", i+1), Percentage: int32((i + 1) * 5)}
	}
	maxCanaries := make([]string, 10_000)
	for i := range maxCanaries {
		maxCanaries[i] = fmt.Sprintf("dev-%d", i)
	}
	for _, tc := range []struct {
		name string
		plan []Cohort
		want string
	}{
		{"empty", nil, ""},
		{"full coverage", []Cohort{all}, ""},
		{"maximum cohorts", maxPlan, ""},
		{"too many cohorts", append(append([]Cohort{}, maxPlan...), Cohort{Name: "extra", Percentage: 100}), "at most 20 cohorts are allowed"},
		{"maximum name", []Cohort{{Name: strings.Repeat("n", 128), Percentage: 100}}, ""},
		{"long name", []Cohort{{Name: strings.Repeat("n", 129), Percentage: 100}}, selector},
		{"missing name", []Cohort{{Percentage: 100}}, selector},
		{"duplicate names", []Cohort{{Name: "all", Percentage: 50}, all}, selector},
		{"missing selector", []Cohort{{Name: "none"}, all}, selector},
		{"both selectors", []Cohort{{Name: "both", DeviceTokens: []string{"a"}, Percentage: 100}}, selector},
		{"maximum canaries", []Cohort{{Name: "canary", DeviceTokens: maxCanaries}, all}, ""},
		{"too many canaries", []Cohort{{Name: "canary", DeviceTokens: append(append([]string{}, maxCanaries...), "extra")}, all}, "too many canary devices"},
		{"duplicate within canary", []Cohort{{Name: "canary", DeviceTokens: []string{"a", "a"}}, all}, canary},
		{"duplicate across canaries", []Cohort{{Name: "one", DeviceTokens: []string{"a"}}, {Name: "two", DeviceTokens: []string{"a"}}, all}, canary},
		{"empty token", []Cohort{{Name: "canary", DeviceTokens: []string{""}}, all}, canary},
		{"invalid token", []Cohort{{Name: "canary", DeviceTokens: []string{"a.b"}}, all}, canary},
		{"canary after percentage", []Cohort{{Name: "half", Percentage: 50}, {Name: "canary", DeviceTokens: []string{"a"}}, all}, canary},
		{"negative percentage", []Cohort{{Name: "negative", Percentage: -1}, all}, percent},
		{"over 100", []Cohort{{Name: "large", Percentage: 101}}, percent},
		{"equal percentages", []Cohort{{Name: "one", Percentage: 50}, {Name: "two", Percentage: 50}, all}, percent},
		{"descending percentages", []Cohort{{Name: "one", Percentage: 60}, {Name: "two", Percentage: 50}, all}, percent},
		{"incomplete coverage", []Cohort{{Name: "half", Percentage: 50}}, final},
		{"canaries only", []Cohort{{Name: "canary", DeviceTokens: []string{"a"}}}, final},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := Validate(tc.plan)
			if tc.want == "" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil || err.Error() != tc.want {
				t.Fatalf("got %v, want %q", err, tc.want)
			}
		})
	}
}
