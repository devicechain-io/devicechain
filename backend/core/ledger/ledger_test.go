// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package ledger

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"math"
	"reflect"
	"sort"
	"strings"
	"testing"
)

func ptr[T any](v T) *T { return &v }
func TestVocabularies(t *testing.T) {
	for _, v := range []Dispatch{DispatchPlanning, DispatchReady, DispatchRunning, DispatchPaused, DispatchDispatched, DispatchAborted} {
		if !v.Valid() {
			t.Errorf("invalid %s", v)
		}
	}
	for _, v := range []Dispatch{DispatchDraft, "", "bad"} {
		if v.Valid() {
			t.Errorf("accepted %s", v)
		}
	}
	for _, v := range []Posting{PostingPlanned, PostingPosted, PostingRefused, PostingSkipped, PostingCancelled} {
		if !v.Valid() {
			t.Errorf("invalid %s", v)
		}
	}
	for _, v := range []Posting{PostingDispatching, PostingUnknown, "", "bad"} {
		if v.Valid() {
			t.Errorf("accepted %s", v)
		}
	}
	for _, v := range []Evidence{EvidenceOpen, EvidenceClosed} {
		if !v.Valid() {
			t.Errorf("invalid %s", v)
		}
	}
	for _, v := range []Evidence{"", "bad"} {
		if v.Valid() {
			t.Errorf("accepted %s", v)
		}
	}
	for _, v := range []Settlement{SettlementConfirmed, SettlementFailed, SettlementSuperseded, SettlementAmbiguous, SettlementResolvedConfirmed, SettlementResolvedFailed} {
		if !v.Valid() {
			t.Errorf("invalid %s", v)
		}
	}
	for _, v := range []Settlement{"", "bad"} {
		if v.Valid() {
			t.Errorf("accepted %s", v)
		}
	}
}
func TestLegalLineState(t *testing.T) {
	ps := []Posting{PostingPlanned, PostingPosted, PostingRefused, PostingSkipped, PostingCancelled, PostingDispatching, PostingUnknown, "", "bad"}
	ss := []*Settlement{nil, ptr(SettlementConfirmed), ptr(SettlementFailed), ptr(SettlementSuperseded), ptr(SettlementAmbiguous), ptr(SettlementResolvedConfirmed), ptr(SettlementResolvedFailed), ptr(Settlement("")), ptr(Settlement("bad"))}
	allowed := map[[2]string]bool{}
	for _, p := range []Posting{PostingPlanned, PostingPosted, PostingRefused, PostingSkipped, PostingCancelled} {
		allowed[[2]string{string(p), "NULL"}] = true
	}
	for _, s := range []string{"CONFIRMED", "FAILED", "SUPERSEDED", "AMBIGUOUS", "RESOLVED_CONFIRMED", "RESOLVED_FAILED"} {
		allowed[[2]string{"POSTED", s}] = true
	}
	for _, p := range ps {
		for _, s := range ss {
			name := "NULL"
			if s != nil {
				name = string(*s)
			}
			t.Run(string(p)+"/"+name, func(t *testing.T) {
				if got := LegalLineState(p, s); got != allowed[[2]string{string(p), name}] {
					t.Fatalf("got %v", got)
				}
			})
		}
	}
}
func TestLegalTransitions(t *testing.T) {
	p := []Posting{PostingPlanned, PostingPosted, PostingRefused, PostingSkipped, PostingCancelled, PostingDispatching, PostingUnknown, "", "bad"}
	pt := map[[2]Posting]bool{{PostingPlanned, PostingPosted}: true, {PostingPlanned, PostingRefused}: true, {PostingPlanned, PostingSkipped}: true, {PostingPlanned, PostingCancelled}: true}
	for _, a := range p {
		for _, b := range p {
			t.Run("posting/"+string(a)+"/"+string(b), func(t *testing.T) {
				if LegalPostingTransition(a, b) != pt[[2]Posting{a, b}] {
					t.Fatal("wrong legality")
				}
			})
		}
	}
	d := []Dispatch{DispatchDraft, DispatchPlanning, DispatchReady, DispatchRunning, DispatchPaused, DispatchDispatched, DispatchAborted, "", "bad"}
	dt := map[[2]Dispatch]bool{{DispatchPlanning, DispatchReady}: true, {DispatchPlanning, DispatchAborted}: true, {DispatchReady, DispatchRunning}: true, {DispatchReady, DispatchAborted}: true, {DispatchRunning, DispatchPaused}: true, {DispatchRunning, DispatchDispatched}: true, {DispatchRunning, DispatchAborted}: true, {DispatchPaused, DispatchRunning}: true, {DispatchPaused, DispatchAborted}: true}
	for _, a := range d {
		for _, b := range d {
			t.Run("dispatch/"+string(a)+"/"+string(b), func(t *testing.T) {
				if LegalDispatchTransition(a, b) != dt[[2]Dispatch{a, b}] {
					t.Fatal("wrong legality")
				}
			})
		}
	}
	s := []*Settlement{nil, ptr(SettlementConfirmed), ptr(SettlementFailed), ptr(SettlementSuperseded), ptr(SettlementAmbiguous), ptr(SettlementResolvedConfirmed), ptr(SettlementResolvedFailed), ptr(Settlement("")), ptr(Settlement("bad"))}
	st := map[[2]string]bool{{"NULL", "CONFIRMED"}: true, {"NULL", "FAILED"}: true, {"NULL", "SUPERSEDED"}: true, {"NULL", "AMBIGUOUS"}: true, {"AMBIGUOUS", "CONFIRMED"}: true, {"AMBIGUOUS", "FAILED"}: true, {"AMBIGUOUS", "SUPERSEDED"}: true, {"AMBIGUOUS", "RESOLVED_CONFIRMED"}: true, {"AMBIGUOUS", "RESOLVED_FAILED"}: true}
	for _, a := range s {
		for _, b := range s {
			an, bn := "NULL", "NULL"
			if a != nil {
				an = string(*a)
			}
			if b != nil {
				bn = string(*b)
			}
			t.Run("settlement/"+an+"/"+bn, func(t *testing.T) {
				if LegalSettlementTransition(a, b) != st[[2]string{an, bn}] {
					t.Fatal("wrong legality")
				}
			})
		}
	}
}
func safetyFixture() Safety { return Safety{ptr(int64(50)), ptr(int64(10)), ptr(int64(5)), 3600} }
func setSafety(s *Safety, name string, v *int64) {
	switch name {
	case "MaxPostsPerSecond":
		s.MaxPostsPerSecond = v
	case "FailureThreshold":
		s.FailureThreshold = v
	case "AmbiguityThreshold":
		s.AmbiguityThreshold = v
	default:
		panic(name)
	}
}
func TestStrictPolicyOrder(t *testing.T) {
	base := safetyFixture()
	cases := []struct {
		name     string
		from, to Safety
		want     PolicyChange
	}{{"equal", base, safetyFixture(), PolicySame}}
	add := func(name string, from, to Safety, want PolicyChange) {
		cases = append(cases, struct {
			name     string
			from, to Safety
			want     PolicyChange
		}{name, from, to, want})
	}
	for _, field := range []string{"MaxPostsPerSecond", "FailureThreshold", "AmbiguityThreshold"} {
		low, none, zero := safetyFixture(), safetyFixture(), safetyFixture()
		setSafety(&low, field, ptr(int64(1)))
		setSafety(&none, field, nil)
		setSafety(&zero, field, ptr(int64(0)))
		add(field+" lower", base, low, PolicyNarrower)
		add(field+" previous ceiling", low, base, PolicyWider)
		add(field+" nil to value", none, base, PolicyNarrower)
		add(field+" value to nil", base, none, PolicyWider)
		add(field+" nil equal", none, none, PolicySame)
		add(field+" zero", low, zero, PolicyNarrower)
	}
	mixed := safetyFixture()
	mixed.MaxPostsPerSecond = ptr(int64(1))
	mixed.FailureThreshold = ptr(int64(11))
	add("mixed", base, mixed, PolicyIncomparable)
	deadline := safetyFixture()
	deadline.SettleDeadlineSeconds--
	add("deadline lower", base, deadline, PolicyIncomparable)
	add("deadline higher", deadline, base, PolicyIncomparable)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := (StrictPolicyOrder{}).Compare(tc.from, tc.to); got != tc.want {
				t.Fatalf("got %s, want %s", got, tc.want)
			}
		})
	}
}
func TestCanonical(t *testing.T) {
	for _, tc := range []struct {
		name string
		v    any
		want string
	}{
		{"keys", map[string]any{"z": []any{true, nil}, "a": map[string]any{"y": 2.0, "x": 1}}, `{"a":{"x":1,"y":2},"z":[true,null]}`},
		{"numbers", []any{json.Number("1.00"), json.Number("1e3"), json.Number("1.25e-2"), json.Number("-0.00"), json.Number("9007199254740993"), json.Number("1.2300e2")}, `[1,1000,0.0125,0,9007199254740993,123]`},
		{"fractions", []any{json.Number("-12.500"), json.Number("0.00100"), json.Number("1e-3"), 1.5}, `[-12.5,0.001,0.001,1.5]`},
		{"strings", map[string]string{"text": "<>&\n\"\\é\u2028"}, "{\"text\":\"<>&\\u000a\\\"\\\\é\u2028\"}"},
		{"raw", json.RawMessage(` { "z": 1.0, "a": 2 } `), `{"a":2,"z":1}`}, {"nil", nil, `null`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Canonical(tc.v)
			if err != nil || string(got) != tc.want {
				t.Fatalf("got %s, %v; want %s", got, err, tc.want)
			}
		})
	}
	cycle := map[string]any{}
	cycle["self"] = cycle
	for _, v := range []any{math.NaN(), math.Inf(1), math.Inf(-1), map[string]any{"x": math.NaN()}, map[int]string{1: "a"}, map[int]string(nil), map[string]any{"x": map[int]string{1: "a"}}, json.Number("NaN"), json.RawMessage(`{"x":NaN}`), make(chan int), cycle} {
		if _, err := Canonical(v); err == nil {
			t.Errorf("Canonical(%T) accepted invalid input", v)
		}
		if _, err := Digest(v); err == nil {
			t.Errorf("Digest(%T) accepted invalid input", v)
		}
	}
	for _, v := range []any{map[string]int{"b": 2, "a": 1}, map[string]int{"a": 1, "b": 2}} {
		d, err := Digest(v)
		sum := sha256.Sum256([]byte(`{"a":1,"b":2}`))
		want := "sha256:" + hex.EncodeToString(sum[:])
		if err != nil || d != want || len(d) != 71 || !strings.HasPrefix(d, "sha256:") {
			t.Fatalf("bad digest %s %v", d, err)
		}
	}
}
func overridesFixture() Overrides {
	return Overrides{ReversalOf: ptr(int64(42)), LineSelection: []int64{3, 1}, OverrideOrdinals: []int64{3}}
}
func requestFixture() RequestDigestInput {
	return RequestDigestInput{Kind: "config.set", Intent: map[string]any{"key": "mode", "value": 1}, TargetTokens: []string{"b", "a"}, Safety: safetyFixture(), Overrides: overridesFixture()}
}
func contractFixture() ContractDigestInput {
	return ContractDigestInput{V: 1, Kind: "config.set", Intent: map[string]any{"key": "mode", "value": 1}, PlanDigest: "sha256:plan", Safety: safetyFixture(), Overrides: overridesFixture(), Approval: Approval{Kind: "HUMAN_DIRECT", PolicyVersion: 1}}
}
func leafPaths(typ reflect.Type, prefix string) []string {
	var result []string
	for i := 0; i < typ.NumField(); i++ {
		f := typ.Field(i)
		path := prefix + f.Name
		if f.Type.Kind() == reflect.Struct {
			result = append(result, leafPaths(f.Type, path+".")...)
		} else {
			result = append(result, path)
		}
	}
	return result
}
func checkFields[T any](t *testing.T, fixture func() T, digest func(T) (string, error), changes map[string]func(*T)) {
	t.Helper()
	want := leafPaths(reflect.TypeOf(fixture()), "")
	var got []string
	for key := range changes {
		got = append(got, key)
	}
	sort.Strings(want)
	sort.Strings(got)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("field coverage %v, want %v", got, want)
	}
	base, err := digest(fixture())
	if err != nil {
		t.Fatal(err)
	}
	if len(base) != 71 || !strings.HasPrefix(base, "sha256:") {
		t.Fatalf("bad digest %s", base)
	}
	for name, change := range changes {
		t.Run(name, func(t *testing.T) {
			v := fixture()
			change(&v)
			d, err := digest(v)
			if err != nil {
				t.Fatal(err)
			}
			if d == base {
				t.Fatalf("changing %s did not change digest", name)
			}
		})
	}
}
func TestRequestDigest(t *testing.T) {
	changes := map[string]func(*RequestDigestInput){
		"Kind": func(v *RequestDigestInput) { v.Kind = "other" }, "Intent": func(v *RequestDigestInput) { v.Intent = map[string]any{"key": "mode", "value": 2} }, "TargetTokens": func(v *RequestDigestInput) { v.TargetTokens = []string{"a", "c"} },
		"Safety.MaxPostsPerSecond": func(v *RequestDigestInput) { v.Safety.MaxPostsPerSecond = ptr(int64(49)) }, "Safety.FailureThreshold": func(v *RequestDigestInput) { v.Safety.FailureThreshold = ptr(int64(9)) }, "Safety.AmbiguityThreshold": func(v *RequestDigestInput) { v.Safety.AmbiguityThreshold = ptr(int64(4)) }, "Safety.SettleDeadlineSeconds": func(v *RequestDigestInput) { v.Safety.SettleDeadlineSeconds++ },
		"Overrides.ReversalOf": func(v *RequestDigestInput) { v.Overrides.ReversalOf = ptr(int64(43)) }, "Overrides.LineSelection": func(v *RequestDigestInput) { v.Overrides.LineSelection = []int64{1, 3} }, "Overrides.OverrideOrdinals": func(v *RequestDigestInput) { v.Overrides.OverrideOrdinals = []int64{1} }}
	checkFields(t, requestFixture, RequestDigest, changes)
	base, _ := RequestDigest(requestFixture())
	v := requestFixture()
	v.TargetTokens = []string{" a ", "b", "a"}
	v.Intent = map[string]any{"value": 1.0, "key": "mode"}
	before := append([]string(nil), v.TargetTokens...)
	d, err := RequestDigest(v)
	if err != nil || d != base {
		t.Fatalf("normalization changed digest %s %v", d, err)
	}
	if !reflect.DeepEqual(v.TargetTokens, before) {
		t.Fatal("mutated tokens")
	}
	v.Intent = math.NaN()
	if _, err := RequestDigest(v); err == nil {
		t.Fatal("NaN accepted")
	}
}
func planFixture() PlanDigestInput {
	return PlanDigestInput{Lines: []PlanLine{{Ordinal: 1, TargetID: ptr(int64(7)), Admission: "ADMITTED"}, {Ordinal: 2, Admission: "TARGET_NOT_FOUND"}}}
}
func TestPlanDigest(t *testing.T) {
	checkFields(t, func() PlanLine { return planFixture().Lines[0] }, func(v PlanLine) (string, error) { return PlanDigest(PlanDigestInput{Lines: []PlanLine{v}}) }, map[string]func(*PlanLine){"Ordinal": func(v *PlanLine) { v.Ordinal++ }, "TargetID": func(v *PlanLine) { v.TargetID = ptr(int64(8)) }, "Admission": func(v *PlanLine) { v.Admission = "TARGET_NOT_FOUND" }})
	checkFields(t, planFixture, PlanDigest, map[string]func(*PlanDigestInput){"Lines": func(v *PlanDigestInput) { v.Lines = v.Lines[:1] }})
	v := planFixture()
	base, _ := PlanDigest(v)
	v.Lines[0], v.Lines[1] = v.Lines[1], v.Lines[0]
	d, _ := PlanDigest(v)
	if d == base {
		t.Fatal("order ignored")
	}
	v = planFixture()
	v.Lines[0].TargetID = nil
	d, _ = PlanDigest(v)
	if d == base {
		t.Fatal("null ignored")
	}
	want, _ := Digest([]any{[]any{1, 7, "ADMITTED"}, []any{2, nil, "TARGET_NOT_FOUND"}})
	if base != want {
		t.Fatal("plan must contain only ordered tuples")
	}
}
func TestContractDigest(t *testing.T) {
	checkFields(t, contractFixture, ContractDigest, map[string]func(*ContractDigestInput){
		"V": func(v *ContractDigestInput) { v.V++ }, "Kind": func(v *ContractDigestInput) { v.Kind = "other" }, "Intent": func(v *ContractDigestInput) { v.Intent = map[string]any{"key": "mode", "value": 2} }, "PlanDigest": func(v *ContractDigestInput) { v.PlanDigest = "other" },
		"Safety.MaxPostsPerSecond": func(v *ContractDigestInput) { v.Safety.MaxPostsPerSecond = ptr(int64(49)) }, "Safety.FailureThreshold": func(v *ContractDigestInput) { v.Safety.FailureThreshold = ptr(int64(9)) }, "Safety.AmbiguityThreshold": func(v *ContractDigestInput) { v.Safety.AmbiguityThreshold = ptr(int64(4)) }, "Safety.SettleDeadlineSeconds": func(v *ContractDigestInput) { v.Safety.SettleDeadlineSeconds++ },
		"Overrides.ReversalOf": func(v *ContractDigestInput) { v.Overrides.ReversalOf = ptr(int64(43)) }, "Overrides.LineSelection": func(v *ContractDigestInput) { v.Overrides.LineSelection = []int64{1, 3} }, "Overrides.OverrideOrdinals": func(v *ContractDigestInput) { v.Overrides.OverrideOrdinals = []int64{1} }, "Approval.Kind": func(v *ContractDigestInput) { v.Approval.Kind = "other" }, "Approval.PolicyVersion": func(v *ContractDigestInput) { v.Approval.PolicyVersion++ }})
	v := contractFixture()
	base, _ := ContractDigest(v)
	v.Intent = map[string]any{"value": 1.0, "key": "mode"}
	d, err := ContractDigest(v)
	if err != nil || d != base {
		t.Fatal("key order changed contract")
	}
	want, _ := Digest(json.RawMessage(`{"v":1,"kind":"config.set","intent":{"key":"mode","value":1},"planDigest":"sha256:plan","safety":{"maxPostsPerSecond":50,"failureThreshold":10,"ambiguityThreshold":5,"settleDeadlineSeconds":3600},"overrides":{"reversalOf":42,"lineSelection":[3,1],"overrideOrdinals":[3]},"approval":{"kind":"HUMAN_DIRECT","policyVersion":1}}`))
	if base != want {
		t.Fatal("contract envelope differs from H05")
	}
	v.Intent = math.NaN()
	if _, err := ContractDigest(v); err == nil {
		t.Fatal("NaN accepted")
	}
}
func TestOverrideAndNullCoverage(t *testing.T) {
	for _, tc := range []struct {
		name   string
		digest func(Overrides) (string, error)
	}{{"request", func(o Overrides) (string, error) { v := requestFixture(); v.Overrides = o; return RequestDigest(v) }}, {"contract", func(o Overrides) (string, error) { v := contractFixture(); v.Overrides = o; return ContractDigest(v) }}} {
		t.Run(tc.name, func(t *testing.T) {
			o := overridesFixture()
			o.OverrideOrdinals = []int64{3, 1}
			base, err := tc.digest(o)
			if err != nil {
				t.Fatal(err)
			}
			o.OverrideOrdinals = []int64{1, 3, 1}
			before := append([]int64(nil), o.OverrideOrdinals...)
			d, err := tc.digest(o)
			if err != nil || d != base {
				t.Fatal("override set order/duplicates changed digest")
			}
			if !reflect.DeepEqual(before, o.OverrideOrdinals) {
				t.Fatal("mutated override set")
			}
			o = overridesFixture()
			o.LineSelection = nil
			a, _ := tc.digest(o)
			o.LineSelection = []int64{}
			b, _ := tc.digest(o)
			if a == b {
				t.Fatal("all and empty selection collide")
			}
		})
	}
	for _, field := range []string{"MaxPostsPerSecond", "FailureThreshold", "AmbiguityThreshold"} {
		v := contractFixture()
		base, _ := ContractDigest(v)
		setSafety(&v.Safety, field, nil)
		d, _ := ContractDigest(v)
		if d == base {
			t.Errorf("contract ignored null %s", field)
		}
		r := requestFixture()
		base, _ = RequestDigest(r)
		setSafety(&r.Safety, field, nil)
		d, _ = RequestDigest(r)
		if d == base {
			t.Errorf("request ignored null %s", field)
		}
	}
}

type testKind string

func (k testKind) Name() string { return string(k) }

type nilKind struct{}

func (k *nilKind) Name() string { return "nil-kind" }
func TestServedRegistry(t *testing.T) {
	if !Served {
		t.Fatal("the engine is served from v1.10; an unserved registry here is a regression")
	}
	var typedNil *nilKind
	for _, k := range []Kind{testKind(""), testKind(" "), testKind(" ledger.test"), testKind("ledger.test "), nil, typedNil} {
		if err := Register(k); err == nil {
			t.Errorf("Register(%#v) succeeded", k)
		}
	}
	if Executable("") || Executable("nil-kind") || Executable(" ") {
		t.Fatal("a refused registration became executable")
	}
	for _, name := range []string{"ledger.test.b", "ledger.test.a"} {
		if Executable(name) {
			t.Fatalf("%q executable before it was registered", name)
		}
		if err := Register(testKind(name)); err != nil {
			t.Fatalf("Register(%q): %v", name, err)
		}
		if !Executable(name) {
			t.Fatalf("%q not executable after it was registered", name)
		}
	}
	if err := Register(testKind("ledger.test.a")); err == nil {
		t.Fatal("a duplicate registration succeeded")
	}
	if Executable("ledger.test") || Executable("custody.transfer") {
		t.Fatal("an unregistered name is executable")
	}
	got := ExecutableKinds()
	ia, ib := -1, -1
	for i, name := range got {
		switch name {
		case "ledger.test.a":
			ia = i
		case "ledger.test.b":
			ib = i
		}
	}
	if ia < 0 || ib < 0 || ia > ib {
		t.Fatalf("executable kinds %v: want both test kinds, sorted", got)
	}
	got[ia] = "injected"
	_ = append(got, "injected-too")
	if Executable("injected") || Executable("injected-too") || !Executable("ledger.test.a") {
		t.Fatal("exposed mutable registry")
	}
}

func TestRequiredProtocolIsOneUntilAFeatureNeedsTwo(t *testing.T) {
	if got := RequiredProtocol(ProtocolFeatures{}); got != 1 {
		t.Fatalf("no features = %d, want 1", got)
	}
	for name, f := range map[string]ProtocolFeatures{
		"counters": {Counters: true}, "waves": {Waves: true}, "snapshot": {Snapshot: true}, "multikey": {MultiKey: true},
		"all": {true, true, true, true},
	} {
		if got := RequiredProtocol(f); got != 2 {
			t.Fatalf("%s = %d, want 2", name, got)
		}
	}
}

func TestDrivableRefusesNewerAndNonPositiveProtocols(t *testing.T) {
	for p, want := range map[DriverProtocol]bool{-1: false, 0: false, 1: true, 2: false, 3: false} {
		if got := Drivable(p); got != want {
			t.Fatalf("Drivable(%d) = %v, want %v", p, got, want)
		}
	}
	// A build only drives what it can: the protocol an unfeatured operation needs is drivable.
	if !Drivable(RequiredProtocol(ProtocolFeatures{})) {
		t.Fatal("this build cannot drive its own ordinary operation")
	}
}
