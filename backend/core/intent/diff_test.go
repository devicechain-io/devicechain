// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package intent

import (
	"errors"
	"reflect"
	"testing"
	"time"
)

var epoch = time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC)

func ptr(s string) *string { return &s }

func vd(t testing.TB, valueType, value string) string {
	t.Helper()
	d, err := ValueDigest(valueType, value)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func TestClassifyEveryCode(t *testing.T) {
	set := mustParse(t, validDoc) // dim = LONG "40"
	unset := mustParse(t, with(`{"valueType":"LONG","value":"40"}`, `{"unset":true}`))
	d40, d41 := vd(t, "LONG", "40"), vd(t, "LONG", "41")
	s40 := vd(t, "STRING", "40")
	for _, tc := range []struct {
		name string
		spec Spec
		row  Row
		want Code
	}{
		// KEY_UNDECLARED precedes everything, even a row that holds the intended value.
		{"undeclared, absent", set, Row{DeclaredValueType: nil}, CodeKeyUndeclared},
		{"undeclared, holding the intended value", set,
			Row{DeclaredValueType: nil, Present: true, ValueType: "LONG", ValueDigest: d40}, CodeKeyUndeclared},
		{"undeclared, unset intent", unset, Row{DeclaredValueType: nil}, CodeKeyUndeclared},

		// DECLARED_TYPE_MISMATCH: the profile declares the key with a type the value cannot carry.
		{"declared BOOLEAN, absent", set, Row{DeclaredValueType: ptr("BOOLEAN")}, CodeDeclaredTypeMismatch},
		{"declared STRING, holding the intended LONG", set,
			Row{DeclaredValueType: ptr("STRING"), Present: true, ValueType: "LONG", ValueDigest: d40}, CodeDeclaredTypeMismatch},
		{"declared DOUBLE, holding something else", set,
			Row{DeclaredValueType: ptr("DOUBLE"), Present: true, ValueType: "DOUBLE", ValueDigest: vd(t, "DOUBLE", "40")}, CodeDeclaredTypeMismatch},
		{"unset needs no type match", unset, Row{DeclaredValueType: ptr("BOOLEAN")}, CodeInSync},

		// IN_SYNC.
		{"declared LONG, holding 40", set,
			Row{DeclaredValueType: ptr("LONG"), Present: true, ValueType: "LONG", ValueDigest: d40}, CodeInSync},
		{"unset, absent", unset, Row{DeclaredValueType: ptr("LONG")}, CodeInSync},

		// DRIFTED.
		{"declared LONG, absent", set, Row{DeclaredValueType: ptr("LONG")}, CodeDrifted},
		{"declared LONG, holding 41", set,
			Row{DeclaredValueType: ptr("LONG"), Present: true, ValueType: "LONG", ValueDigest: d41}, CodeDrifted},
		{"declared LONG, holding 040", set,
			Row{DeclaredValueType: ptr("LONG"), Present: true, ValueType: "LONG", ValueDigest: vd(t, "LONG", "040")}, CodeDrifted},
		{"declared LONG, holding STRING 40", set,
			Row{DeclaredValueType: ptr("LONG"), Present: true, ValueType: "STRING", ValueDigest: s40}, CodeDrifted},
		// A row whose type disagrees with its digest is not trusted to be in sync.
		{"declared LONG, typed STRING with LONG's digest", set,
			Row{DeclaredValueType: ptr("LONG"), Present: true, ValueType: "STRING", ValueDigest: d40}, CodeDrifted},
		{"unset, present", unset,
			Row{DeclaredValueType: ptr("LONG"), Present: true, ValueType: "LONG", ValueDigest: d40}, CodeDrifted},
	} {
		tc.row.DeviceID, tc.row.Token = 1, "dev-1"
		got, err := Classify(tc.spec, tc.row, epoch)
		if err != nil || got != tc.want {
			t.Errorf("%s: Classify = %s, %v; want %s", tc.name, got, err, tc.want)
		}
		drift, err := Diff(tc.spec, []Row{tc.row}, epoch)
		if err != nil {
			t.Errorf("%s: Diff: %v", tc.name, err)
			continue
		}
		want := Counts{}
		want.add(tc.want)
		if drift.Counts != want {
			t.Errorf("%s: Diff counts = %+v, want %+v", tc.name, drift.Counts, want)
		}
		if got := len(drift.Lines) == 1; got != (tc.want == CodeDrifted) {
			t.Errorf("%s: a line was planned = %v, but the code is %s", tc.name, got, tc.want)
		}
	}
}

func TestDiffCountsAndOrdersAFleet(t *testing.T) {
	s := mustParse(t, validDoc)
	d40 := vd(t, "LONG", "40")
	rows := []Row{
		{DeviceID: 2, Token: "a", DeclaredValueType: ptr("LONG")},                                                                     // DRIFTED
		{DeviceID: 5, Token: "b", DeclaredValueType: nil},                                                                             // KEY_UNDECLARED
		{DeviceID: 6, Token: "c", DeclaredValueType: ptr("LONG"), Present: true, ValueType: "LONG", ValueDigest: d40},                 // IN_SYNC
		{DeviceID: 9, Token: "d", DeclaredValueType: ptr("BOOLEAN")},                                                                  // DECLARED_TYPE_MISMATCH
		{DeviceID: 10, Token: "e", DeclaredValueType: ptr("LONG"), Present: true, ValueType: "LONG", ValueDigest: vd(t, "LONG", "1")}, // DRIFTED
	}
	drift, err := Diff(s, rows, epoch)
	if err != nil {
		t.Fatal(err)
	}
	if want := (Counts{KeyUndeclared: 1, DeclaredTypeMismatch: 1, InSync: 1, Drifted: 2}); drift.Counts != want {
		t.Errorf("counts = %+v, want %+v", drift.Counts, want)
	}
	if want := []Line{{2, "a"}, {10, "e"}}; !reflect.DeepEqual(drift.Lines, want) {
		t.Errorf("lines = %v, want %v", drift.Lines, want)
	}
	empty, err := Diff(s, nil, epoch)
	if err != nil || len(empty.Lines) != 0 || empty.Counts != (Counts{}) || empty.Lines == nil {
		t.Errorf("an empty target must diff to no lines and zero counts: %+v, %v", empty, err)
	}
}

func TestDiffRefusesWhatItCannotClassify(t *testing.T) {
	s := mustParse(t, validDoc)
	ok := func(id int64) Row { return Row{DeviceID: id, Token: "t", DeclaredValueType: ptr("LONG")} }
	for name, rows := range map[string][]Row{
		"descending":            {ok(2), ok(1)},
		"duplicate":             {ok(2), ok(2)},
		"zero id":               {{DeviceID: 0, Token: "t"}},
		"empty token":           {{DeviceID: 1}},
		"unknown declared type": {{DeviceID: 1, Token: "t", DeclaredValueType: ptr("INTEGER")}},
		"empty declared type":   {{DeviceID: 1, Token: "t", DeclaredValueType: ptr("")}},
		"present without type":  {{DeviceID: 1, Token: "t", DeclaredValueType: ptr("LONG"), Present: true, ValueDigest: vd(t, "LONG", "1")}},
		"present, bad digest":   {{DeviceID: 1, Token: "t", DeclaredValueType: ptr("LONG"), Present: true, ValueType: "LONG", ValueDigest: "sha256:00"}},
		"upper-case digest":     {{DeviceID: 1, Token: "t", DeclaredValueType: ptr("LONG"), Present: true, ValueType: "LONG", ValueDigest: "sha256:B3B50FCEAC38355D8742E70EC5E937E317C5D7A484B69D5E8EB7A716ACA4BC68"}},
		"absent with a digest":  {{DeviceID: 1, Token: "t", DeclaredValueType: ptr("LONG"), ValueDigest: vd(t, "LONG", "1")}},
		"absent with a type":    {{DeviceID: 1, Token: "t", DeclaredValueType: ptr("LONG"), ValueType: "LONG"}},
		"type with a tab":       {{DeviceID: 1, Token: "t", DeclaredValueType: ptr("LONG"), Present: true, ValueType: "LO\tNG", ValueDigest: vd(t, "LONG", "1")}},
	} {
		if _, err := Diff(s, rows, epoch); err == nil {
			t.Errorf("%s: Diff accepted %+v", name, rows)
		}
		if _, err := ObservationDigest(rows); err == nil {
			t.Errorf("%s: ObservationDigest accepted %+v", name, rows)
		}
	}
}

// A target over the ceiling is refused, never truncated to it.
func TestDiffRefusesATargetOverTheCeiling(t *testing.T) {
	s := mustParse(t, with(`"maxDevices":2000`, `"maxDevices":2`))
	rows := []Row{
		{DeviceID: 1, Token: "a", DeclaredValueType: ptr("LONG")},
		{DeviceID: 2, Token: "b", DeclaredValueType: ptr("LONG")},
	}
	if _, err := Diff(s, rows, epoch); err != nil {
		t.Fatalf("a target at its ceiling must diff: %v", err)
	}
	rows = append(rows, Row{DeviceID: 3, Token: "c", DeclaredValueType: ptr("LONG")})
	drift, err := Diff(s, rows, epoch)
	if !errors.Is(err, ErrCeilingExceeded) {
		t.Fatalf("Diff over the ceiling = %+v, %v; want ErrCeilingExceeded", drift, err)
	}
}

func TestChunk(t *testing.T) {
	d := Drift{Lines: []Line{{9, "i"}, {2, "b"}, {5, "e"}, {1, "a"}}}
	got, err := Chunk(d, 3)
	if err != nil {
		t.Fatal(err)
	}
	if want := []Line{{1, "a"}, {2, "b"}, {5, "e"}}; !reflect.DeepEqual(got, want) {
		t.Errorf("Chunk = %v, want the first three by ascending id %v", got, want)
	}
	if d.Lines[0].DeviceID != 9 {
		t.Error("Chunk reordered its argument")
	}
	all, _ := Chunk(d, MaxLinesPerReconcile)
	if len(all) != 4 {
		t.Errorf("Chunk under the limit returned %d lines, want 4", len(all))
	}
	for _, n := range []int{0, -1, MaxLinesPerReconcile + 1} {
		if _, err := Chunk(d, n); err == nil {
			t.Errorf("Chunk accepted maxLines %d", n)
		}
	}
	one, err := Chunk(d, 1)
	if err != nil || len(one) != 1 {
		t.Errorf("Chunk(1) = %v, %v", one, err)
	}
}

func TestObservationDigestCoversEveryHashedField(t *testing.T) {
	base := Row{DeviceID: 7, Token: "lamp-7", DeclaredValueType: ptr("LONG"), Present: true, ValueType: "LONG", ValueDigest: vd(t, "LONG", "40")}
	digest := func(r Row) string {
		d, err := ObservationDigest([]Row{r})
		if err != nil {
			t.Fatal(err)
		}
		return d
	}
	want := digest(base)
	for name, mutate := range map[string]func(*Row){
		"device id":             func(r *Row) { r.DeviceID = 8 },
		"declared type":         func(r *Row) { r.DeclaredValueType = ptr("BOOLEAN") },
		"declared type to none": func(r *Row) { r.DeclaredValueType = nil },
		"value type":            func(r *Row) { r.ValueType = "STRING" },
		"value digest":          func(r *Row) { r.ValueDigest = vd(t, "LONG", "41") },
		"present to absent":     func(r *Row) { r.Present, r.ValueType, r.ValueDigest = false, "", "" },
	} {
		r := base
		mutate(&r)
		if digest(r) == want {
			t.Errorf("changing the %s did not move the observation digest", name)
		}
	}
	r := base
	r.Token = "renamed"
	if digest(r) != want {
		t.Error("the device token must not be in the observation digest")
	}
	// A profile-only change: the declared type moves, no attribute is written.
	a, _ := ObservationDigest([]Row{{DeviceID: 1, Token: "t", DeclaredValueType: ptr("LONG")}})
	b, _ := ObservationDigest([]Row{{DeviceID: 1, Token: "t", DeclaredValueType: ptr("BOOLEAN")}})
	if a == b {
		t.Error("a declared-type change alone must move the observation digest")
	}
	// Row boundaries are framed: two rows are not one row with the same bytes.
	two, _ := ObservationDigest([]Row{{DeviceID: 1, Token: "t"}, {DeviceID: 2, Token: "t"}})
	one, _ := ObservationDigest([]Row{{DeviceID: 1, Token: "t"}})
	if two == one {
		t.Error("adding a member must move the observation digest")
	}
}

// The incremental hasher, fed page by page, gives the one-shot digest, and refuses a page that
// goes backwards.
func TestObservationHasherAcrossPages(t *testing.T) {
	rows := make([]Row, 0, 2500)
	for i := int64(1); i <= 2500; i++ {
		r := Row{DeviceID: i * 3, Token: "d", DeclaredValueType: ptr("LONG")}
		if i%2 == 0 {
			r.Present, r.ValueType, r.ValueDigest = true, "LONG", vd(t, "LONG", "40")
		}
		rows = append(rows, r)
	}
	whole, err := ObservationDigest(rows)
	if err != nil {
		t.Fatal(err)
	}
	h := NewObservationHasher()
	for start := 0; start < len(rows); start += 1000 {
		for _, r := range rows[start:min(start+1000, len(rows))] {
			if err := h.Add(r); err != nil {
				t.Fatal(err)
			}
		}
	}
	if h.Sum() != whole || h.Count() != 2500 {
		t.Errorf("paged digest %s over %d rows, whole %s", h.Sum(), h.Count(), whole)
	}
	if err := h.Add(rows[10]); err == nil {
		t.Error("the hasher accepted a row behind the last one")
	}
}

func TestValueDigest(t *testing.T) {
	if vd(t, "LONG", "40") == vd(t, "STRING", "40") {
		t.Error("the type must be part of the value digest")
	}
	if vd(t, "STRING", "a:b") == vd(t, "STRING", "a") {
		t.Error("the value must be part of the value digest")
	}
	if _, err := ValueDigest("INTEGER", "40"); err == nil {
		t.Error("ValueDigest accepted an unknown type")
	}
	if _, err := ValueDigest("", "LONG:40"); err == nil {
		t.Error("ValueDigest accepted an empty type")
	}
}

func TestIntendedValueAndSameSubject(t *testing.T) {
	s := mustParse(t, validDoc)
	want := Intended{ValueType: "LONG", ValueDigest: vd(t, "LONG", "40")}
	for _, at := range []time.Time{epoch, epoch.Add(time.Hour), time.Time{}} {
		for _, dev := range []int64{1, 99} {
			if got, err := IntendedValue(s, dev, at); err != nil || got != want {
				t.Errorf("IntendedValue(%d, %v) = %+v, %v; want the constant %+v", dev, at, got, err, want)
			}
			if b, err := InstantBucket(s, at); err != nil || b != 0 {
				t.Errorf("InstantBucket(%v) = %d, %v; a constant value has one bucket", at, b, err)
			}
		}
	}
	unset := mustParse(t, with(`{"valueType":"LONG","value":"40"}`, `{"unset":true}`))
	if got, _ := IntendedValue(unset, 1, epoch); got != (Intended{Unset: true}) {
		t.Errorf("IntendedValue(unset) = %+v", got)
	}

	other := mustParse(t, with(`"value":"40"`, `"value":"70"`))
	otherGroup := mustParse(t, with(`"groupToken":"quay-lamps"`, `"groupToken":"other"`))
	otherKey := mustParse(t, with(`"configKey":"dim"`, `"configKey":"mode"`))
	otherClass := mustParse(t, with(`"SCHEDULE"`, `"OPTIMIZATION"`))
	otherClassOtherValue := otherClass
	otherClassOtherValue.Value = other.Value
	otherType := mustParse(t, with(`{"valueType":"LONG","value":"40"}`, `{"valueType":"STRING","value":"40"}`))
	otherGroupOtherValue := mustParse(t, with(`"groupToken":"quay-lamps","groupVersion":3`, `"groupToken":"x","groupVersion":9`))
	otherGroupOtherValue.Value = other.Value
	for _, tc := range []struct {
		name string
		a, b Spec
		want bool
	}{
		{"same key, class, different value", s, other, true},
		{"same key, class, value type differs", s, otherType, true},
		{"same key, class, set against unset", s, unset, true},
		{"targets do not matter", s, otherGroupOtherValue, true},
		{"same value is no competition", s, otherGroup, false},
		{"both unset", unset, unset, false},
		{"different key", s, otherKey, false},
		{"different class", s, otherClass, false},
		{"different class, different value", s, otherClassOtherValue, false},
	} {
		for _, pair := range [][2]Spec{{tc.a, tc.b}, {tc.b, tc.a}} {
			if got, err := SameSubject(pair[0], pair[1]); err != nil || got != tc.want {
				t.Errorf("%s: SameSubject = %v, %v; want %v", tc.name, got, err, tc.want)
			}
		}
	}
}

func TestInputsDigestCoversEveryInput(t *testing.T) {
	spec, obs := vd(t, "LONG", "1"), vd(t, "LONG", "2")
	base, err := InputsDigest(spec, obs, 0, CompilerBuild)
	if err != nil {
		t.Fatal(err)
	}
	pair := func(d string, err error) [2]any { return [2]any{d, err} }
	for name, got := range map[string][2]any{
		"spec digest":        pair(InputsDigest(vd(t, "LONG", "3"), obs, 0, CompilerBuild)),
		"observation digest": pair(InputsDigest(spec, vd(t, "LONG", "3"), 0, CompilerBuild)),
		"instant bucket":     pair(InputsDigest(spec, obs, 1, CompilerBuild)),
		"compiler build":     pair(InputsDigest(spec, obs, 0, CompilerBuild+"x")),
	} {
		if got[1] != nil || got[0] == base {
			t.Errorf("changing the %s did not move InputsDigest (%v)", name, got[1])
		}
	}
	if again, _ := InputsDigest(spec, obs, 0, CompilerBuild); again != base {
		t.Error("InputsDigest is not deterministic")
	}
	for name, args := range map[string][2]string{
		"empty spec digest":        {"", obs},
		"empty observation digest": {spec, ""},
		"short observation digest": {spec, "sha256:00"},
	} {
		if _, err := InputsDigest(args[0], args[1], 0, CompilerBuild); err == nil {
			t.Errorf("%s: InputsDigest accepted it", name)
		}
	}
	if _, err := InputsDigest(spec, obs, 0, ""); err == nil {
		t.Error("InputsDigest accepted an empty compiler build")
	}
}
