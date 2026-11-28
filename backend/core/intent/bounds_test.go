// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package intent

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/ledger"
)

// The bounds the observation seam's cost rests on: the intended value's size, the oversize
// marker, the byte-bounded chunk and the duty cycle.

func TestIntendedValueSizeIsBounded(t *testing.T) {
	at := strings.Repeat("a", MaxIntentValueBytes)
	if _, err := Parse([]byte(with(`{"valueType":"LONG","value":"40"}`, `{"valueType":"STRING","value":"`+at+`"}`))); err != nil {
		t.Fatalf("a value of exactly %d bytes must be accepted: %v", MaxIntentValueBytes, err)
	}
	over := at + "a"
	_, err := Parse([]byte(with(`{"valueType":"LONG","value":"40"}`, `{"valueType":"STRING","value":"`+over+`"}`)))
	if !errors.Is(err, ErrInvalidSpec) {
		t.Fatalf("a value of %d bytes was accepted: %v", len(over), err)
	}
	// Bytes, not characters: 342 three-byte runes are 1,026 bytes.
	runes := strings.Repeat("€", 342)
	if _, err := Parse([]byte(with(`{"valueType":"LONG","value":"40"}`, `{"valueType":"STRING","value":"`+runes+`"}`))); !errors.Is(err, ErrInvalidSpec) {
		t.Fatalf("a value of %d bytes in %d characters was accepted", len(runes), len([]rune(runes)))
	}
}

func TestOversizeRowsAreDriftedNeverInSync(t *testing.T) {
	set := mustParse(t, validDoc) // dim = LONG "40"
	unset := mustParse(t, with(`{"valueType":"LONG","value":"40"}`, `{"unset":true}`))
	row := Row{DeviceID: 4, Token: "lamp-4", DeclaredValueType: ptr("LONG"), Present: true, ValueType: "LONG",
		ValueDigest: oversize(t, MaxIntentValueBytes+1)}
	for name, s := range map[string]Spec{"set": set, "unset": unset} {
		code, err := Classify(s, row, epoch)
		if err != nil || code != CodeDrifted {
			t.Errorf("%s intent, oversize row: %s, %v; want DRIFTED", name, code, err)
		}
	}
	undeclared := row
	undeclared.DeclaredValueType = nil
	if code, err := Classify(set, undeclared, epoch); err != nil || code != CodeKeyUndeclared {
		t.Errorf("an undeclared oversize row: %s, %v; want KEY_UNDECLARED (precedence holds)", code, err)
	}
	mismatch := row
	mismatch.DeclaredValueType = ptr("STRING")
	if code, err := Classify(set, mismatch, epoch); err != nil || code != CodeDeclaredTypeMismatch {
		t.Errorf("a mismatched oversize row: %s, %v; want DECLARED_TYPE_MISMATCH", code, err)
	}
	if _, err := ObservationDigest([]Row{row}); err != nil {
		t.Errorf("an oversize row must hash: %v", err)
	}
}

func TestMalformedOversizeMarkersAreRefused(t *testing.T) {
	for _, d := range []string{
		"OVERSIZE:", "OVERSIZE:1024", "OVERSIZE:5", "OVERSIZE:01025", "OVERSIZE:-2000", "OVERSIZE:+2000",
		"OVERSIZE: 2000", "OVERSIZE:2000 ", "oversize:2000", "OVERSIZE:99999999999999999999", "OVERSIZE:2e3",
	} {
		row := Row{DeviceID: 1, Token: "a", DeclaredValueType: ptr("LONG"), Present: true, ValueType: "LONG", ValueDigest: d}
		if _, err := ObservationDigest([]Row{row}); err == nil {
			t.Errorf("marker %q was accepted", d)
		}
	}

}

func TestOversizeMovesTheDigestByLengthOnly(t *testing.T) {
	digest := func(marker string) string {
		d, err := ObservationDigest([]Row{{DeviceID: 1, Token: "a", DeclaredValueType: ptr("LONG"), Present: true, ValueType: "LONG", ValueDigest: marker}})
		if err != nil {
			t.Fatal(err)
		}
		return d
	}
	if digest(oversize(t, 2000)) == digest(oversize(t, 2001)) {
		t.Error("a change of oversize length did not move the digest")
	}
	if digest(oversize(t, 2000)) == digest(vd(t, "LONG", "40")) {
		t.Error("an oversize row hashed like a hashed value")
	}
}

func tokenOf(n, width int) string {
	s := fmt.Sprintf("d%d-", n)
	return s + strings.Repeat("x", width-len(s))
}

func TestChunkFitsThePlannersByteBound(t *testing.T) {
	d := Drift{}
	for i := 1; i <= MaxLinesPerReconcile; i++ {
		d.Lines = append(d.Lines, Line{DeviceID: int64(i), Token: tokenOf(i, MaxTokenLen)})
	}
	got, err := Chunk(d, MaxLinesPerReconcile, ledger.MaxRequestBytes)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 8128 {
		t.Fatalf("10,000 lines of 128-byte tokens chunked to %d lines; want 8,128", len(got))
	}
	bytes := 0
	for _, l := range got {
		bytes += len(l.Token) + 1
	}
	if bytes > ledger.MaxRequestBytes {
		t.Fatalf("the chunk is %d bytes as the planner counts, over %d", bytes, ledger.MaxRequestBytes)
	}
	if next := bytes + len(d.Lines[len(got)].Token) + 1; next <= ledger.MaxRequestBytes {
		t.Fatalf("the chunk stopped early: one more line would be %d bytes", next)
	}
	if got[0].DeviceID != 1 || got[len(got)-1].DeviceID != 8128 {
		t.Errorf("the chunk is not the first lines by ascending id: %d..%d", got[0].DeviceID, got[len(got)-1].DeviceID)
	}
	// The line bound still applies first when it is the tighter one.
	if got, _ := Chunk(d, 100, ledger.MaxRequestBytes); len(got) != 100 {
		t.Errorf("Chunk(100 lines) = %d lines", len(got))
	}
	// A byte bound that admits exactly one line.
	if got, _ := Chunk(d, MaxLinesPerReconcile, MaxTokenLen+1); len(got) != 1 {
		t.Errorf("Chunk at one line of bytes = %d lines", len(got))
	}
	for _, b := range []int{0, -1, MaxTokenLen} {
		if _, err := Chunk(d, 10, b); err == nil {
			t.Errorf("Chunk accepted maxBytes %d, which cannot hold the longest token", b)
		}
	}
}

func TestEffectiveIntervalHoldsTheDutyCycle(t *testing.T) {
	for _, tc := range []struct {
		interval int64
		took     time.Duration
		want     int64
	}{
		{60, 5 * time.Second, 60},
		{60, 6 * time.Second, 60},
		{60, 6*time.Second + time.Millisecond, 61},
		{60, 15 * time.Second, 150},
		{3600, 15 * time.Second, 3600},
		{60, 0, 60},
		{60, -time.Second, 60},
		{60, 2 * time.Minute, 1200},
	} {
		if got := EffectiveIntervalSeconds(tc.interval, tc.took); got != tc.want {
			t.Errorf("EffectiveIntervalSeconds(%d, %s) = %d, want %d", tc.interval, tc.took, got, tc.want)
		}
	}
}

func TestPreviewLinesSkipsTheEmptyAndTheSatisfied(t *testing.T) {
	s := mustParse(t, validDoc) // maxLinesPerReconcile 500
	for name, tc := range map[string]struct {
		members int64
		drift   Drift
	}{
		"an empty target":     {0, Drift{Lines: []Line{}}},
		"a fleet in sync":     {3, Drift{Lines: []Line{}, Counts: Counts{InSync: 3}}},
		"only undeclared":     {2, Drift{Lines: []Line{}, Counts: Counts{KeyUndeclared: 1, DeclaredTypeMismatch: 1}}},
		"members but no diff": {5, Drift{}},
	} {
		lines, send, err := PreviewLines(s, tc.members, tc.drift, ledger.MaxRequestBytes)
		if err != nil || send || len(lines) != 0 {
			t.Errorf("%s: PreviewLines = %v, %v, %v; want nothing to send", name, lines, send, err)
		}
	}
	d := Drift{}
	for i := 1; i <= 600; i++ {
		d.Lines = append(d.Lines, Line{DeviceID: int64(i), Token: tokenOf(i, MaxTokenLen)})
	}
	lines, send, err := PreviewLines(s, 600, d, ledger.MaxRequestBytes)
	if err != nil || !send || len(lines) != 500 {
		t.Fatalf("600 drifted under a 500-line spec: %d lines, send %v, %v", len(lines), send, err)
	}
	if _, _, err := PreviewLines(s, -1, d, ledger.MaxRequestBytes); err == nil {
		t.Error("a negative member count was accepted")
	}
	if _, _, err := PreviewLines(Spec{}, 1, d, ledger.MaxRequestBytes); err == nil {
		t.Error("an invalid spec was accepted")
	}
}
