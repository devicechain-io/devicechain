// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package geo

import (
	"github.com/golang/geo/s2"
)

// signBit is the high bit of a 64-bit cell id.
const signBit = uint64(1) << 63

// EncodeCell maps an S2 cell id to the signed bigint a database stores, PRESERVING ORDER.
//
// 🔴 S2 ids order as UNSIGNED 64-bit. Stored as a plain int64, the ids of faces 4 and 5 are
// negative and sort before face 0, and a range that spans the face 3/face 4 boundary has its
// bounds the wrong way round: a signed range scan silently drops half of the planet and nothing
// about the answer looks wrong. Flipping the high bit is an order-preserving bijection from
// unsigned to signed, so unsigned order IS signed order and a contiguous range stays contiguous.
// The same encoding must be applied to every range endpoint.
func EncodeCell(id s2.CellID) int64 { return int64(uint64(id) ^ signBit) }

// DecodeCell inverts EncodeCell.
func DecodeCell(v int64) s2.CellID { return s2.CellID(uint64(v) ^ signBit) }

// LeafCell is the encoded leaf cell id for a position: the value stored beside a row and indexed.
func LeafCell(p Position) (int64, error) {
	if err := p.Validate(); err != nil {
		return 0, err
	}
	return EncodeCell(s2.CellIDFromLatLng(s2.LatLngFromDegrees(p.Lat, p.Lon))), nil
}

// Range is an INCLUSIVE interval of encoded leaf cells.
type Range struct{ Lo, Hi int64 }
