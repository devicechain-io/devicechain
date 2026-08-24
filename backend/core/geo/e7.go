// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package geo

import (
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"math"
)

// The E7 coordinate codec: WGS84 degrees x 10^7 as int32, packed big-endian.
//
// 🔴 IT EXISTS BECAUSE A COORDINATE WRITTEN AS JSON TEXT HAS NO BOUNDED LENGTH, so a
// fence's byte size was not a function of its position count at all. A position is two
// JSON numbers, each of unbounded text length, which is why the authoring side had to
// grow a byte bound, a canonicaliser, an intake guard and a rendered-length scanner just
// to say how big a fence could be — and why the fence SET was over both 1 MiB seams (the
// broker's per-message ceiling and the cross-service response cap) at the very limits the
// registry documents. Measured at the ceiling of 100 fences x 512 positions: GeoJSON text
// at 7 decimal places is 1.36x of 1 MiB. Packed E7 in this encoding is 0.53x.
//
// 🔴 QUANTISING ALONE DOES NOT ACHIEVE THAT, WHICH IS WHY THIS PACKS. E7 values written
// back into the GeoJSON coordinate array as plain integers measure 1.28x — barely better
// than the text they replace, because "-1224194000" is nearly as long as "-122.4194". The
// saving is in the PACKING, not in the quantisation; the quantisation is what makes the
// packing fixed-width, and the fixed width is what finally makes a position count a byte
// bound again.
//
// 1e7 is the OSM / Android / Google convention. It resolves ~1.1 cm at the equator and
// fits int32 with room to spare: the extreme longitude is +/-1,800,000,000 against int32's
// 2,147,483,647.
//
// 🔴 THE CONVERSION IS LOSSY AND THAT IS DELIBERATE, so it must happen exactly once, at
// AUTHORING time, and the quantised ring is what everything downstream sees — validated,
// stored, snapshotted, published and evaluated. Quantising at read time instead would put
// a rounding step between the fence that was checked and the fence that answers, which is
// the same shape of defect as validating one document and storing another. See
// QuantizeRings.
const (
	// E7Scale is the fixed-point scale: degrees x 10^7.
	E7Scale = 1e7

	// e7Version is the first byte of every packed blob.
	//
	// 🔴 IT IS NOT CEREMONY. This blob is opaque — it travels base64'd inside a JSON
	// document, so nothing downstream can see its shape by looking. Without a version
	// byte a later format change would be read by an older decoder as a valid blob of
	// the wrong geometry: no error, just a boundary in the wrong place, which is the
	// exact failure mode this package was created to prevent.
	e7Version = 1

	// e7PositionBytes is one position: int32 longitude then int32 latitude.
	e7PositionBytes = 8

	// e7HeaderBytes is the version byte plus the uint16 ring count.
	e7HeaderBytes = 3

	// e7RingHeaderBytes is one ring's uint16 position count.
	e7RingHeaderBytes = 2

	// e7MaxRings and e7MaxRingPositions bound what a HEADER may declare, independently
	// of any policy limit the authoring side applies. See DecodeRings: they are a
	// sanity ceiling, not the allocation guard, because a declared length is never what
	// this decoder allocates from.
	e7MaxRings         = 1 << 12
	e7MaxRingPositions = 1 << 20
)

// QuantizeRings snaps every ordinate of every ring to the E7 grid, returning rings in the
// same [][][]float64 shape the GeoJSON path already uses.
//
// 🔴 CALL THIS BEFORE VALIDATING, NOT AFTER. Quantisation can move a vertex by up to half
// a grid step, and two vertices closer together than one step COLLAPSE ONTO EACH OTHER —
// which turns an edge into a point and can make a ring that was a well-formed polygon into
// a degenerate one. Validating the authored ring and storing the quantised one would let
// exactly that fence save cleanly and answer nothing, which is the defect ValidateClosedRing
// was extracted into this package to prevent. Quantise first, then validate what you will
// actually store.
//
// Closure survives this in both directions and neither is an accident: two positions that
// were bit-identical quantise to the same pair, and two that differed by less than a grid
// step become identical, which closes a ring that was within a centimetre of closed rather
// than opening one that was closed.
func QuantizeRings(rings [][][]float64) [][][]float64 {
	out := make([][][]float64, len(rings))
	for i, ring := range rings {
		q := make([][]float64, len(ring))
		for j, pos := range ring {
			p := make([]float64, len(pos))
			for k, ord := range pos {
				p[k] = DequantizeE7(QuantizeE7(ord))
			}
			q[j] = p
		}
		out[i] = q
	}
	return out
}

// QuantizeE7 converts degrees to the fixed-point grid.
//
// A non-finite or out-of-int32 input saturates rather than wrapping. Callers range-check
// coordinates long before this — every real caller has already refused anything outside
// [-180, 180] — but the failure modes differ by more than politeness: an int32 conversion
// of a value it cannot hold is implementation-defined in the sense that matters here, and
// wrapping +1e9 degrees into a valid-looking negative longitude would place a boundary
// somewhere plausible on the other side of the planet. Saturation is wrong loudly.
func QuantizeE7(deg float64) int32 {
	if math.IsNaN(deg) {
		return 0
	}
	v := math.Round(deg * E7Scale)
	if v > math.MaxInt32 {
		return math.MaxInt32
	}
	if v < math.MinInt32 {
		return math.MinInt32
	}
	return int32(v)
}

// DequantizeE7 converts a fixed-point value back to degrees.
func DequantizeE7(v int32) float64 {
	return float64(v) / E7Scale
}

// EncodeRings packs rings into the E7 wire format.
//
// Layout, all integers big-endian:
//
//	1 byte   version
//	uint16   ring count
//	per ring:
//	  uint16 position count
//	  per position: int32 longitude, int32 latitude
//
// Big-endian so a hex dump of a blob reads in the order the numbers are written; the
// format is never mmap'd, so native order buys nothing and costs legibility on the one
// occasion anyone looks.
//
// Ordinates beyond the first two are DROPPED. The authoring side already refuses a
// position that is not exactly [longitude, latitude] — a maximum, not a minimum, because
// the minimum is what let a position be arbitrarily wide — so this cannot silently discard
// a third ordinate that some caller meant. It is written to take the first two rather than
// to require exactly two so that the codec has no opinion the validator does not already
// enforce.
func EncodeRings(rings [][][]float64) ([]byte, error) {
	if len(rings) == 0 {
		return nil, fmt.Errorf("cannot encode a geometry with no rings")
	}
	if len(rings) > e7MaxRings {
		return nil, fmt.Errorf("geometry has %d rings; the encoder's ceiling is %d", len(rings), e7MaxRings)
	}
	size := e7HeaderBytes
	for _, ring := range rings {
		if len(ring) > e7MaxRingPositions {
			return nil, fmt.Errorf("ring has %d positions; the encoder's ceiling is %d",
				len(ring), e7MaxRingPositions)
		}
		size += e7RingHeaderBytes + len(ring)*e7PositionBytes
	}

	buf := make([]byte, 0, size)
	buf = append(buf, e7Version)
	buf = binary.BigEndian.AppendUint16(buf, uint16(len(rings)))
	for _, ring := range rings {
		buf = binary.BigEndian.AppendUint16(buf, uint16(len(ring)))
		for _, pos := range ring {
			if len(pos) < 2 {
				return nil, fmt.Errorf("a position needs at least [longitude, latitude], got %d ordinates",
					len(pos))
			}
			buf = binary.BigEndian.AppendUint32(buf, uint32(QuantizeE7(pos[0])))
			buf = binary.BigEndian.AppendUint32(buf, uint32(QuantizeE7(pos[1])))
		}
	}
	return buf, nil
}

// DecodeRings unpacks the E7 wire format back into degrees.
//
// 🔴 NOTHING HERE ALLOCATES FROM A DECLARED LENGTH. Every count read out of the blob is
// checked against the bytes that are actually present before it is used to size anything,
// so a truncated or hostile blob claiming 65,535 positions fails on arithmetic rather than
// reserving memory for positions that were never sent. The ceilings above are a second,
// coarser gate; this bound is the one that matters, because it cannot be outrun by a
// declared number no matter how the ceilings move.
//
// The returned rings are the SAME grid points the encoder was given, exactly: E7 -> float64
// -> E7 is lossless because every int32 in range is exactly representable in a float64. The
// loss is entirely in QuantizeE7, which is why that runs once at authoring time and this
// never re-rounds anything.
func DecodeRings(blob []byte) ([][][]float64, error) {
	if len(blob) < e7HeaderBytes {
		return nil, fmt.Errorf("packed geometry is %d bytes; it needs at least %d for a header",
			len(blob), e7HeaderBytes)
	}
	if v := blob[0]; v != e7Version {
		return nil, fmt.Errorf("packed geometry declares format version %d; this decoder reads version %d",
			v, e7Version)
	}
	ringCount := int(binary.BigEndian.Uint16(blob[1:3]))
	if ringCount == 0 {
		return nil, fmt.Errorf("packed geometry declares no rings")
	}
	if ringCount > e7MaxRings {
		return nil, fmt.Errorf("packed geometry declares %d rings; the ceiling is %d", ringCount, e7MaxRings)
	}

	off := e7HeaderBytes
	rings := make([][][]float64, 0, ringCount)
	for i := 0; i < ringCount; i++ {
		if len(blob)-off < e7RingHeaderBytes {
			return nil, fmt.Errorf("packed geometry ends inside ring %d's header", i)
		}
		n := int(binary.BigEndian.Uint16(blob[off : off+e7RingHeaderBytes]))
		off += e7RingHeaderBytes
		// The allocation guard: the declared count has to be BACKED BY BYTES already in
		// hand before it sizes a slice.
		if avail := (len(blob) - off) / e7PositionBytes; n > avail {
			return nil, fmt.Errorf("packed geometry ring %d declares %d positions but only %d remain",
				i, n, avail)
		}
		ring := make([][]float64, 0, n)
		for j := 0; j < n; j++ {
			lon := int32(binary.BigEndian.Uint32(blob[off : off+4]))
			lat := int32(binary.BigEndian.Uint32(blob[off+4 : off+8]))
			off += e7PositionBytes
			ring = append(ring, []float64{DequantizeE7(lon), DequantizeE7(lat)})
		}
		rings = append(rings, ring)
	}
	// Trailing bytes mean the blob is not what its header says it is. Ignoring them would
	// make two different blobs decode to the same geometry, which is how a byte bound
	// stops bounding anything: pad to taste, store it, and nothing downstream objects.
	if off != len(blob) {
		return nil, fmt.Errorf("packed geometry has %d trailing bytes after its last ring", len(blob)-off)
	}
	return rings, nil
}

// EncodeRingsBase64 / DecodeRingsBase64 are the forms that travel inside a JSON document.
//
// Standard base64 with padding, so the text is what every JSON reader, psql session and
// language runtime already agrees on. It costs 4 bytes of text per 3 of payload, which is
// the price of keeping the coordinates INSIDE the self-describing geometry envelope rather
// than in a column of their own: the kind discriminator stays in the document, so a later
// geometry kind is still new members of an existing object rather than DDL.
func EncodeRingsBase64(rings [][][]float64) (string, error) {
	blob, err := EncodeRings(rings)
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(blob), nil
}

// DecodeRingsBase64 reverses EncodeRingsBase64.
func DecodeRingsBase64(s string) ([][][]float64, error) {
	blob, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return nil, fmt.Errorf("packed geometry is not valid base64: %w", err)
	}
	return DecodeRings(blob)
}

// EncodedSize reports the packed byte length of a geometry with the given per-ring position
// counts, without building it.
//
// 🔴 THIS FUNCTION IS THE POINT OF THE WHOLE FORMAT: a fence's size is now a pure function
// of its position count, computable before anything is encoded. The text encoding it
// replaces had no such function — the same 512 positions could store as 11 KB or as 1.4 MB
// depending on how the numbers were written — which is why bounding fence size previously
// required canonicalising the document and measuring the result.
func EncodedSize(ringPositionCounts []int) int {
	n := e7HeaderBytes
	for _, c := range ringPositionCounts {
		n += e7RingHeaderBytes + c*e7PositionBytes
	}
	return n
}

// Base64Size reports the base64 text length of a packed blob of n bytes.
func Base64Size(n int) int {
	return (n + 2) / 3 * 4
}
