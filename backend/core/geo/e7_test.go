// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package geo

import (
	"encoding/binary"
	"math"
	"testing"
)

// square returns a closed ring, n positions around a small circle, so a test can ask for a
// ring of a given SIZE without hand-writing one.
func circle(n int, lonC, latC, r float64) [][]float64 {
	ring := make([][]float64, 0, n)
	for i := 0; i < n-1; i++ {
		a := 2 * math.Pi * float64(i) / float64(n-1)
		ring = append(ring, []float64{lonC + r*math.Cos(a), latC + r*math.Sin(a)})
	}
	return append(ring, []float64{ring[0][0], ring[0][1]})
}

// TestPackedSizeIsAFunctionOfPositionCountAlone is the property the format exists for.
//
// The text encoding it replaces had no such function: the SAME position count stored as
// wildly different byte counts depending on how the numbers were written. Here, two
// geometries with the same shape have the same size no matter what the coordinates are.
func TestPackedSizeIsAFunctionOfPositionCountAlone(t *testing.T) {
	tidy := circle(512, -122.42, 37.77, 0.01)
	// The same position count, written at a precision that made the text encoding explode.
	nasty := make([][]float64, len(tidy))
	for i, p := range tidy {
		nasty[i] = []float64{p[0] + 1e-11, p[1] - 1e-11}
	}

	a, err := EncodeRings([][][]float64{tidy})
	if err != nil {
		t.Fatalf("encode tidy: %v", err)
	}
	b, err := EncodeRings([][][]float64{nasty})
	if err != nil {
		t.Fatalf("encode nasty: %v", err)
	}
	if len(a) != len(b) {
		t.Fatalf("same position count encoded to different sizes: %d vs %d", len(a), len(b))
	}
	if want := EncodedSize([]int{512}); len(a) != want {
		t.Fatalf("EncodedSize said %d, encoder produced %d", want, len(a))
	}
}

// TestCeilingFenceSetFitsOneMessage is the whole reason for the change: at the registry's
// documented ceiling the fence set has to fit inside the 1 MiB budget both delivery seams
// have, so the pointer fact and the paged read can be deleted rather than maintained.
//
// 🔴 The numbers here are MaxGeoFenceVertices and MaxGeoFencesPerTenant from
// device-management, restated rather than imported: core must not depend on a service. If
// that service raises either limit, this test keeps passing while the claim it encodes
// stops being true — so the constants there carry a pointer back to this test.
func TestCeilingFenceSetFitsOneMessage(t *testing.T) {
	const (
		vertices = 512
		fences   = 100
		budget   = 1 << 20
	)
	blob, err := EncodeRings([][][]float64{circle(vertices, -122.42, 37.77, 0.01)})
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	// What actually travels is the base64 text inside the JSON envelope.
	perFence := Base64Size(len(blob))
	if got := perFence * fences; got >= budget {
		t.Fatalf("ceiling set is %d bytes of coordinates against a %d budget", got, budget)
	}
	// And the negative control: the same set in the TEXT encoding this replaces does not
	// fit, which is the defect. Seven decimal places is 12 bytes an ordinate plus
	// punctuation — about 27 bytes a position.
	if textual := vertices * 27 * fences; textual < budget {
		t.Fatalf("the text encoding fit in %d bytes, so this test is not measuring the "+
			"problem the format was written to solve", budget)
	}
}

// TestRoundTripIsExactOnTheGrid pins that the loss is confined to QuantizeE7. Once a
// coordinate is on the grid, encode/decode must not move it again — a codec that re-rounds
// would make a fence drift every time it was republished.
func TestRoundTripIsExactOnTheGrid(t *testing.T) {
	rings := QuantizeRings([][][]float64{
		circle(8, -122.4194, 37.7749, 0.01),
		circle(6, -122.4194, 37.7749, 0.002),
	})
	blob, err := EncodeRings(rings)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	got, err := DecodeRings(blob)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got) != len(rings) {
		t.Fatalf("ring count %d != %d", len(got), len(rings))
	}
	for i := range rings {
		if len(got[i]) != len(rings[i]) {
			t.Fatalf("ring %d position count %d != %d", i, len(got[i]), len(rings[i]))
		}
		for j := range rings[i] {
			if got[i][j][0] != rings[i][j][0] || got[i][j][1] != rings[i][j][1] {
				t.Fatalf("ring %d position %d: %v != %v", i, j, got[i][j], rings[i][j])
			}
		}
	}
}

// TestQuantizeIsIdempotent — quantising an already-quantised ring must be a no-op, or
// re-saving an unchanged fence would move its boundary.
func TestQuantizeIsIdempotent(t *testing.T) {
	once := QuantizeRings([][][]float64{circle(16, -122.4194, 37.7749, 0.0137)})
	twice := QuantizeRings(once)
	for i := range once {
		for j := range once[i] {
			for k := range once[i][j] {
				if once[i][j][k] != twice[i][j][k] {
					t.Fatalf("ring %d position %d ordinate %d moved on the second pass: %v -> %v",
						i, j, k, once[i][j][k], twice[i][j][k])
				}
			}
		}
	}
}

// TestQuantizationPreservesClosure — a closed ring must stay closed, because every consumer
// downstream treats closure as a structural guarantee.
func TestQuantizationPreservesClosure(t *testing.T) {
	q := QuantizeRings([][][]float64{circle(32, 179.9999999, 89.9999999, 0.00001)})
	for i, ring := range q {
		first, last := ring[0], ring[len(ring)-1]
		if first[0] != last[0] || first[1] != last[1] {
			t.Fatalf("ring %d opened under quantisation: %v != %v", i, first, last)
		}
	}
}

// TestQuantizationCanCollapseAdjacentVertices documents the hazard that forces
// QuantizeRings to run BEFORE validation rather than after.
//
// 🔴 THIS TEST ASSERTS A DEFECT IS REACHABLE, NOT THAT IT IS HANDLED. Two vertices closer
// together than one grid step become the same point, turning an edge into nothing. If the
// authoring side validated the authored ring and stored the quantised one, a fence could
// pass every structural check and be stored degenerate. Keeping the collapse visible here
// is what keeps that ordering honest.
func TestQuantizationCanCollapseAdjacentVertices(t *testing.T) {
	// 1e-9 degrees is about a tenth of a millimetre — two orders below the 1.1 cm grid.
	ring := [][]float64{
		{-122.0, 37.0},
		{-122.0 + 1e-9, 37.0},
		{-122.0, 37.001},
		{-122.0, 37.0},
	}
	q := QuantizeRings([][][]float64{ring})[0]
	if q[0][0] != q[1][0] || q[0][1] != q[1][1] {
		t.Fatalf("expected the two sub-grid vertices to collapse, got %v and %v", q[0], q[1])
	}
}

func TestSaturatesRatherThanWraps(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   float64
		want int32
	}{
		{"far positive", 1e9, math.MaxInt32},
		{"far negative", -1e9, math.MinInt32},
		{"positive infinity", math.Inf(1), math.MaxInt32},
		{"negative infinity", math.Inf(-1), math.MinInt32},
		{"nan", math.NaN(), 0},
		{"max longitude", 180, 1_800_000_000},
		{"min longitude", -180, -1_800_000_000},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := QuantizeE7(tc.in); got != tc.want {
				t.Fatalf("QuantizeE7(%v) = %d, want %d", tc.in, got, tc.want)
			}
		})
	}
}

// TestDecodeRefusesMalformedBlobs. Each case is a way a blob can be wrong that a decoder
// reading declared lengths on trust would accept or would allocate for.
func TestDecodeRefusesMalformedBlobs(t *testing.T) {
	good, err := EncodeRings([][][]float64{circle(8, -122.42, 37.77, 0.01)})
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	// The positive control: the fixture these mutations start from must decode.
	if _, err := DecodeRings(good); err != nil {
		t.Fatalf("the unmutated fixture did not decode: %v", err)
	}

	overrun := append([]byte(nil), good...)
	binary.BigEndian.PutUint16(overrun[3:5], 0xFFFF) // ring 0 claims 65,535 positions

	noRings := append([]byte(nil), good...)
	binary.BigEndian.PutUint16(noRings[1:3], 0)

	badVersion := append([]byte(nil), good...)
	badVersion[0] = 2

	for _, tc := range []struct {
		name string
		blob []byte
	}{
		{"empty", nil},
		{"header only", good[:2]},
		{"wrong version", badVersion},
		{"zero rings", noRings},
		{"declared count overruns the buffer", overrun},
		{"truncated mid-position", good[:len(good)-3]},
		{"trailing bytes", append(append([]byte(nil), good...), 0x00)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := DecodeRings(tc.blob); err == nil {
				t.Fatalf("decoded a %s blob without error", tc.name)
			}
		})
	}
}

// TestBase64RoundTrip covers the form that actually travels inside the JSON envelope, and
// that a non-base64 string is refused rather than decoded into garbage.
func TestBase64RoundTrip(t *testing.T) {
	rings := QuantizeRings([][][]float64{circle(12, 4.8952, 52.3702, 0.005)})
	s, err := EncodeRingsBase64(rings)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	got, err := DecodeRingsBase64(s)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got) != 1 || len(got[0]) != len(rings[0]) {
		t.Fatalf("round trip changed the shape: %d rings", len(got))
	}
	if want := Base64Size(EncodedSize([]int{len(rings[0])})); len(s) != want {
		t.Fatalf("Base64Size said %d, got %d", want, len(s))
	}
	if _, err := DecodeRingsBase64("not base64 !!!"); err == nil {
		t.Fatal("decoded a non-base64 string without error")
	}
}

// TestEncodeRefusesShapesItCannotDescribe.
func TestEncodeRefusesShapesItCannotDescribe(t *testing.T) {
	if _, err := EncodeRings(nil); err == nil {
		t.Fatal("encoded a geometry with no rings")
	}
	if _, err := EncodeRings([][][]float64{{{1}}}); err == nil {
		t.Fatal("encoded a one-ordinate position")
	}
}
