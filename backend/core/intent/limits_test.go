// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package intent

import (
	"errors"
	"math"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/devicechain-io/dc-microservice/ledger"
)

// The three round-2 limits are the accepted values; a change to any of them is a decision, not a
// refactor.
func TestRoundTwoLimits(t *testing.T) {
	if MaxIntentValueBytes != 1024 {
		t.Errorf("MaxIntentValueBytes = %d, want 1024", MaxIntentValueBytes)
	}
	if MaxObservedDevicesPerTenant != 500_000 {
		t.Errorf("MaxObservedDevicesPerTenant = %d, want 500000", MaxObservedDevicesPerTenant)
	}
	if EvaluationDutyCyclePercent != 10 {
		t.Errorf("EvaluationDutyCyclePercent = %d, want 10", EvaluationDutyCyclePercent)
	}
	// One intent at its ceiling must fit a tenant's observed-device budget.
	if MaxObservedDevicesPerTenant < MaxDevicesPerIntent {
		t.Errorf("MaxObservedDevicesPerTenant %d is below MaxDevicesPerIntent %d", MaxObservedDevicesPerTenant, MaxDevicesPerIntent)
	}
	if MaxChunkBytes != ledger.MaxRequestBytes {
		t.Errorf("MaxChunkBytes = %d; it must be the planner's bound %d", MaxChunkBytes, ledger.MaxRequestBytes)
	}
}

func stringValueDoc(text string) string {
	return with(`{"valueType":"LONG","value":"40"}`, `{"valueType":"STRING","value":"`+text+`"}`)
}

// The intended value is bounded in bytes (not runes, not canonical bytes): at the cap it is
// accepted, one byte over it is refused, by Parse and by Validate on a built spec alike.
func TestIntendedValueIsAtMostMaxIntentValueBytes(t *testing.T) {
	accepted := map[string]string{
		"ASCII at the cap":     strings.Repeat("a", MaxIntentValueBytes),
		"two-byte runes at it": strings.Repeat("é", MaxIntentValueBytes/2),
		// Each control character canonicalizes as six bytes; the cap is on the value, not on that.
		"control characters at the cap": strings.Repeat("\x01", MaxIntentValueBytes),
	}
	refused := map[string]string{
		"ASCII one over":          strings.Repeat("a", MaxIntentValueBytes+1),
		"runes one over in bytes": strings.Repeat("é", MaxIntentValueBytes/2) + "a",
		"far over":                strings.Repeat("a", 4*MaxIntentValueBytes),
	}
	build := func(text string) Spec {
		s := mustParse(t, validDoc)
		s.Value = Value{ValueType: ValueTypeString, Value: &text}
		return s
	}
	for name, text := range accepted {
		if err := build(text).Validate(); err != nil {
			t.Errorf("%s: Validate refused a %d-byte value: %v", name, len(text), err)
		}
		if _, err := Parse([]byte(stringValueDoc(jsonEscape(text)))); err != nil {
			t.Errorf("%s: Parse refused a %d-byte value: %v", name, len(text), err)
		}
		if _, err := ValueDigest(ValueTypeString, text); err != nil {
			t.Errorf("%s: ValueDigest refused a %d-byte value: %v", name, len(text), err)
		}
	}
	for name, text := range refused {
		if err := build(text).Validate(); !errors.Is(err, ErrInvalidSpec) {
			t.Errorf("%s: Validate of a %d-byte value = %v; want ErrInvalidSpec", name, len(text), err)
		}
		if _, err := Parse([]byte(stringValueDoc(jsonEscape(text)))); !errors.Is(err, ErrInvalidSpec) {
			t.Errorf("%s: Parse of a %d-byte value = %v; want ErrInvalidSpec", name, len(text), err)
		}
		// A value over the cap has no value digest: it is observed as OVERSIZE, never hashed.
		if d, err := ValueDigest(ValueTypeString, text); err == nil {
			t.Errorf("%s: ValueDigest hashed a %d-byte value: %s", name, len(text), d)
		}
	}
	// A JSON value is bounded the same way.
	big := `[` + strings.Repeat(`1,`, MaxIntentValueBytes/2) + `1]`
	s := mustParse(t, validDoc)
	s.Value = Value{ValueType: ValueTypeJSON, Value: &big}
	if err := s.Validate(); !errors.Is(err, ErrInvalidSpec) {
		t.Errorf("Validate of a %d-byte JSON value = %v; want ErrInvalidSpec", len(big), err)
	}
}

func jsonEscape(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r == '"' || r == '\\':
			b.WriteByte('\\')
			b.WriteRune(r)
		case r < 0x20:
			b.WriteString(`\u00`)
			b.WriteString(strconv.FormatInt(int64(r)>>4, 16))
			b.WriteString(strconv.FormatInt(int64(r)&0xf, 16))
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

func oversize(t testing.TB, n int64) string {
	t.Helper()
	m, err := OversizeMarker(n)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestOversizeMarker(t *testing.T) {
	if got := oversize(t, MaxIntentValueBytes+1); got != "OVERSIZE:1025" {
		t.Errorf("OversizeMarker(1025) = %q", got)
	}
	if got := oversize(t, 65536); got != "OVERSIZE:65536" {
		t.Errorf("OversizeMarker(65536) = %q", got)
	}
	// A value at the cap is hashed, so it has no oversize marker.
	for _, n := range []int64{MaxIntentValueBytes, 0, -1, math.MaxInt32 + 1} {
		if m, err := OversizeMarker(n); err == nil {
			t.Errorf("OversizeMarker(%d) = %q; want a refusal", n, m)
		}
	}
}

// An OVERSIZE row holds a value longer than any value an intent may hold, so it can never equal
// the intended value: it is DRIFTED once the key is declared with the intended type, whatever the
// spec, and never IN_SYNC.
func TestOversizeRowIsNeverInSync(t *testing.T) {
	long := mustParse(t, validDoc) // LONG "40"
	atCap := mustParse(t, stringValueDoc(strings.Repeat("s", MaxIntentValueBytes)))
	unset := mustParse(t, with(`{"valueType":"LONG","value":"40"}`, `{"unset":true}`))
	for _, tc := range []struct {
		name string
		spec Spec
		row  Row
		want Code
	}{
		{"LONG intent", long, Row{DeclaredValueType: ptr("LONG"), Present: true, ValueType: "LONG", ValueDigest: oversize(t, 1025)}, CodeDrifted},
		{"STRING intent at the cap", atCap, Row{DeclaredValueType: ptr("STRING"), Present: true, ValueType: "STRING", ValueDigest: oversize(t, 1025)}, CodeDrifted},
		{"STRING intent, far over", atCap, Row{DeclaredValueType: ptr("STRING"), Present: true, ValueType: "STRING", ValueDigest: oversize(t, 65536)}, CodeDrifted},
		{"unset intent", unset, Row{DeclaredValueType: ptr("LONG"), Present: true, ValueType: "LONG", ValueDigest: oversize(t, 2048)}, CodeDrifted},
		// The precedence is unchanged: an undeclared key or a type mismatch still wins.
		{"undeclared", long, Row{DeclaredValueType: nil, Present: true, ValueType: "LONG", ValueDigest: oversize(t, 2048)}, CodeKeyUndeclared},
		{"declared another type", long, Row{DeclaredValueType: ptr("STRING"), Present: true, ValueType: "STRING", ValueDigest: oversize(t, 2048)}, CodeDeclaredTypeMismatch},
	} {
		tc.row.DeviceID, tc.row.Token = 4, "big-4"
		got, err := Classify(tc.spec, tc.row, epoch)
		if err != nil || got != tc.want {
			t.Errorf("%s: Classify = %s, %v; want %s", tc.name, got, err, tc.want)
		}
		drift, err := Diff(tc.spec, []Row{tc.row}, epoch)
		if err != nil {
			t.Fatalf("%s: Diff: %v", tc.name, err)
		}
		if drift.Counts.InSync != 0 {
			t.Errorf("%s: an OVERSIZE row was counted IN_SYNC: %+v", tc.name, drift.Counts)
		}
		if planned := len(drift.Lines) == 1; planned != (tc.want == CodeDrifted) {
			t.Errorf("%s: a line was planned = %v, but the code is %s", tc.name, planned, tc.want)
		}
	}
}

func TestOversizeRowIsHashedAsItsMarker(t *testing.T) {
	row := func(digest string) Row {
		return Row{DeviceID: 7, Token: "big-7", DeclaredValueType: ptr("STRING"), Present: true, ValueType: "STRING", ValueDigest: digest}
	}
	a, err := ObservationDigest([]Row{row(oversize(t, 2048))})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := ObservationDigest([]Row{row(oversize(t, 2049))})
	c, _ := ObservationDigest([]Row{row(vd(t, "STRING", "x"))})
	if a == b {
		t.Error("a change of an oversize value's length must move the observation digest")
	}
	if a == c {
		t.Error("an oversize value and a hashed one must not share an observation digest")
	}
	// Malformed markers are refused like a malformed digest: an observation that cannot have
	// come from the seam is an error, never a guess.
	for _, bad := range []string{
		"OVERSIZE:1024", "OVERSIZE:0", "OVERSIZE:", "OVERSIZE:-2048", "OVERSIZE:+2048",
		"OVERSIZE:02048", "OVERSIZE:2048 ", "OVERSIZE: 2048", "oversize:2048", "OVERSIZE:2048x",
		"OVERSIZE:2147483648", "OVERSIZE:99999999999999999999",
	} {
		if _, err := ObservationDigest([]Row{row(bad)}); err == nil {
			t.Errorf("ObservationDigest accepted the marker %q", bad)
		}
		if _, err := Diff(mustParse(t, validDoc), []Row{row(bad)}, epoch); err == nil {
			t.Errorf("Diff accepted the marker %q", bad)
		}
	}
	absent := Row{DeviceID: 7, Token: "t", DeclaredValueType: ptr("STRING"), ValueDigest: oversize(t, 2048)}
	if _, err := ObservationDigest([]Row{absent}); err == nil {
		t.Error("an absent row carrying an oversize marker was accepted")
	}
}

func token128(i int) string {
	s := strconv.Itoa(i)
	return strings.Repeat("t", MaxTokenLen-len(s)) + s
}

func driftOf(tokens []string) Drift {
	d := Drift{Lines: make([]Line, 0, len(tokens))}
	for i, tok := range tokens {
		d.Lines = append(d.Lines, Line{DeviceID: int64(i + 1), Token: tok})
	}
	d.Counts.Drifted = int64(len(tokens))
	return d
}

// plannerAccepts is the planner normalizer's own byte bound: Σ(len(token) + 1) must not exceed
// ledger.MaxRequestBytes, or the request is refused INPUT_TOO_LARGE.
func plannerAccepts(lines []Line) bool {
	n := 0
	for _, l := range lines {
		n += len(l.Token) + 1
	}
	return n <= ledger.MaxRequestBytes
}

// The worst legal fleet: 10,000 drifted devices whose tokens are all 128 bytes. Sent whole, the
// list is 1,290,000 bytes and the planner refuses it. The chunk stops at the byte bound: 8,128
// lines of 129 bytes are 1,048,512 bytes, and one more would be 1,048,641 > 1,048,576.
func TestChunkStopsAtThePlannersByteBound(t *testing.T) {
	tokens := make([]string, MaxLinesPerReconcile)
	for i := range tokens {
		tokens[i] = token128(i)
	}
	d := driftOf(tokens)
	if plannerAccepts(d.Lines) {
		t.Fatal("fixture: the whole fleet must be over the planner's bound, or this test proves nothing")
	}
	got, err := Chunk(d, MaxLinesPerReconcile, MaxChunkBytes)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 8128 {
		t.Fatalf("Chunk of 10,000 128-byte tokens = %d lines, want 8128", len(got))
	}
	if !plannerAccepts(got) {
		t.Error("the chunk is over the planner's byte bound")
	}
	if plannerAccepts(d.Lines[:len(got)+1]) {
		t.Error("one more line would still fit; the chunk stopped early")
	}
	if !reflect.DeepEqual(got, d.Lines[:8128]) {
		t.Error("the chunk is not the first drifted devices by ascending id")
	}
}

func TestChunkStopsAtWhicheverBoundComesFirst(t *testing.T) {
	long := []string{token128(1), token128(2), token128(3), token128(4)} // 129 bytes each
	d := driftOf(long)
	for _, tc := range []struct {
		maxLines, maxBytes, want int
	}{
		{4, 4 * 129, 4},   // both bounds exactly met
		{4, 4*129 - 1, 3}, // one byte short: the byte bound wins
		{2, 4 * 129, 2},   // the line bound wins
		{4, 129, 1},       // the smallest byte bound still plans one line
		{1, MaxChunkBytes, 1},
	} {
		got, err := Chunk(d, tc.maxLines, tc.maxBytes)
		if err != nil || len(got) != tc.want {
			t.Errorf("Chunk(maxLines %d, maxBytes %d) = %d lines, %v; want %d", tc.maxLines, tc.maxBytes, len(got), err, tc.want)
		}
	}
	short := driftOf([]string{"a", "b", "c", "d", "e"}) // 2 bytes each
	if got, err := Chunk(short, 5, MaxTokenLen+1); err != nil || len(got) != 5 {
		t.Errorf("short tokens under the byte bound = %d lines, %v; want 5", len(got), err)
	}
	for _, maxBytes := range []int{0, -1, MaxTokenLen, MaxChunkBytes + 1} {
		if _, err := Chunk(d, 4, maxBytes); err == nil {
			t.Errorf("Chunk accepted maxBytes %d", maxBytes)
		}
	}
}

// Zero drift is the empty chunk, so the caller has nothing to send; a non-empty drift is never
// an empty chunk, so drift is never mistaken for none.
func TestChunkOfZeroDriftIsEmpty(t *testing.T) {
	for name, d := range map[string]Drift{
		"zero value":  {},
		"no lines":    {Lines: []Line{}},
		"all in sync": {Lines: []Line{}, Counts: Counts{InSync: 7}},
	} {
		got, err := Chunk(d, MaxLinesPerReconcile, MaxChunkBytes)
		if err != nil || len(got) != 0 {
			t.Errorf("%s: Chunk = %v, %v; want the empty chunk", name, got, err)
		}
	}
	// Through Diff: a fleet already in sync plans nothing.
	s := mustParse(t, validDoc)
	rows := []Row{{DeviceID: 1, Token: "a", DeclaredValueType: ptr("LONG"), Present: true, ValueType: "LONG", ValueDigest: vd(t, "LONG", "40")}}
	drift, err := Diff(s, rows, epoch)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := Chunk(drift, 1, MaxTokenLen+1); err != nil || len(got) != 0 {
		t.Errorf("Chunk of an in-sync fleet = %v, %v; want the empty chunk", got, err)
	}
	// A hand-built line no byte bound can hold is an error, not an empty chunk.
	huge := Drift{Lines: []Line{{DeviceID: 1, Token: strings.Repeat("x", 300)}}}
	if got, err := Chunk(huge, 1, MaxTokenLen+1); err == nil {
		t.Errorf("Chunk of a line over maxBytes = %v; want an error, never an empty chunk", got)
	}
}
