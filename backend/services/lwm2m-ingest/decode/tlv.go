// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package decode

import (
	"encoding/binary"
	"errors"
	"fmt"
)

// LwM2M TLV (OMA-TS-LightweightM2M-V1_0 §6.4.3, content format 11542). Every entry is
//
//	type byte | identifier (8 or 16 bits) | length (0, 8, 16 or 24 bits) | value
//
// where the type byte carries, from the high bit down: the entry kind (2 bits), whether the
// identifier is 16 bits wide (1 bit), how wide the length field is (2 bits), and — when that width
// is zero — the value length itself (3 bits). An Object Instance holds Resource and Multiple
// Resource entries; a Multiple Resource holds Resource Instance entries; the other two carry a
// value.

// ErrMalformedTLV is returned for bytes that are not a well-formed TLV document. A decode that
// fails never yields a partial result: a truncated payload is not an empty one.
var ErrMalformedTLV = errors.New("lwm2m: malformed TLV")

// TLVKind is the kind of one TLV entry (the two high bits of its type byte).
type TLVKind uint8

const (
	TLVObjectInstance   TLVKind = 0 // 00: holds Resource / Multiple Resource entries
	TLVResourceInstance TLVKind = 1 // 01: one instance of a multiple resource, carries a value
	TLVMultipleResource TLVKind = 2 // 10: holds Resource Instance entries
	TLVResource         TLVKind = 3 // 11: a single resource, carries a value
)

func (k TLVKind) String() string {
	switch k {
	case TLVObjectInstance:
		return "object instance"
	case TLVResourceInstance:
		return "resource instance"
	case TLVMultipleResource:
		return "multiple resource"
	default:
		return "resource"
	}
}

// TLV is one decoded entry. Value is set for the two value-carrying kinds; Children for the two
// container kinds. Value aliases the input slice.
type TLV struct {
	Kind     TLVKind
	ID       uint16
	Value    []byte
	Children []TLV
}

// DecodeTLV decodes a whole TLV document. The input must be consumed exactly: a trailing fragment,
// a length that runs past the end, an empty document, or a container holding a kind it may not hold
// is an error wrapping ErrMalformedTLV.
func DecodeTLV(b []byte) ([]TLV, error) {
	if len(b) == 0 {
		return nil, fmt.Errorf("%w: empty payload", ErrMalformedTLV)
	}
	return decodeTLVSeq(b, nil)
}

// decodeTLVSeq decodes a sequence of entries filling b. parent is the enclosing container kind (nil
// at the top level), which restricts what may appear.
func decodeTLVSeq(b []byte, parent *TLVKind) ([]TLV, error) {
	var out []TLV
	for len(b) > 0 {
		e, n, err := decodeTLVEntry(b)
		if err != nil {
			return nil, err
		}
		if parent != nil {
			switch *parent {
			case TLVObjectInstance:
				if e.Kind != TLVResource && e.Kind != TLVMultipleResource {
					return nil, fmt.Errorf("%w: %s inside an object instance", ErrMalformedTLV, e.Kind)
				}
			case TLVMultipleResource:
				if e.Kind != TLVResourceInstance {
					return nil, fmt.Errorf("%w: %s inside a multiple resource", ErrMalformedTLV, e.Kind)
				}
			}
		}
		if e.Kind == TLVObjectInstance || e.Kind == TLVMultipleResource {
			kind := e.Kind
			children, err := decodeTLVSeq(e.Value, &kind)
			if err != nil {
				return nil, err
			}
			e.Children, e.Value = children, nil
		}
		out = append(out, e)
		b = b[n:]
	}
	return out, nil
}

// decodeTLVEntry decodes the entry at the start of b and reports how many bytes it spans. A
// container's value is returned raw; the caller decodes its children.
func decodeTLVEntry(b []byte) (TLV, int, error) {
	typ := b[0]
	pos := 1
	e := TLV{Kind: TLVKind(typ >> 6)}

	if typ&0x20 != 0 {
		if len(b) < pos+2 {
			return TLV{}, 0, fmt.Errorf("%w: truncated identifier", ErrMalformedTLV)
		}
		e.ID = binary.BigEndian.Uint16(b[pos:])
		pos += 2
	} else {
		if len(b) < pos+1 {
			return TLV{}, 0, fmt.Errorf("%w: truncated identifier", ErrMalformedTLV)
		}
		e.ID = uint16(b[pos])
		pos++
	}

	var length int
	switch lw := int(typ>>3) & 0x03; lw {
	case 0:
		length = int(typ & 0x07)
	default:
		if len(b) < pos+lw {
			return TLV{}, 0, fmt.Errorf("%w: truncated length", ErrMalformedTLV)
		}
		for _, x := range b[pos : pos+lw] {
			length = length<<8 | int(x)
		}
		pos += lw
	}
	if len(b)-pos < length {
		return TLV{}, 0, fmt.Errorf("%w: value of %d bytes runs past the end (%d left)", ErrMalformedTLV, length, len(b)-pos)
	}
	e.Value = b[pos : pos+length]
	return e, pos + length, nil
}

// TLVInteger decodes a TLV Integer value: a signed big-endian two's-complement number of exactly 1,
// 2, 4 or 8 bytes. Any other width is an error, not a best-effort read.
func TLVInteger(v []byte) (int64, error) {
	switch len(v) {
	case 1:
		return int64(int8(v[0])), nil
	case 2:
		return int64(int16(binary.BigEndian.Uint16(v))), nil
	case 4:
		return int64(int32(binary.BigEndian.Uint32(v))), nil
	case 8:
		return int64(binary.BigEndian.Uint64(v)), nil
	default:
		return 0, fmt.Errorf("%w: an integer is 1, 2, 4 or 8 bytes, not %d", ErrMalformedTLV, len(v))
	}
}

// ErrNotSingleTLVValue is returned by TLVResourceValue when a well-formed document does not carry
// exactly one value for the resource asked about.
var ErrNotSingleTLVValue = errors.New("lwm2m: TLV does not carry a single value for the resource")

// TLVResourceValue decodes the answer to a Read of ONE resource (/o/i/r) and returns its value
// bytes. It accepts what conformant clients send for that Read:
//
//   - a Resource-with-Value entry for resourceID (what Leshan sends);
//   - the same wrapped in its Object Instance entry;
//   - a Multiple Resource entry for resourceID holding exactly one Resource Instance.
//
// A multiple resource with zero or several instances, an entry for a different resource, or more
// than one top-level entry is ErrNotSingleTLVValue: picking one of several values would be inventing
// the answer. Bytes that do not decode are ErrMalformedTLV.
func TLVResourceValue(b []byte, resourceID uint16) ([]byte, error) {
	entries, err := DecodeTLV(b)
	if err != nil {
		return nil, err
	}
	if len(entries) == 1 && entries[0].Kind == TLVObjectInstance {
		entries = entries[0].Children
	}
	if len(entries) != 1 {
		return nil, fmt.Errorf("%w: %d entries", ErrNotSingleTLVValue, len(entries))
	}
	e := entries[0]
	switch e.Kind {
	case TLVResource, TLVMultipleResource:
	default:
		return nil, fmt.Errorf("%w: a %s at the top level", ErrNotSingleTLVValue, e.Kind)
	}
	if e.ID != resourceID {
		return nil, fmt.Errorf("%w: the answer is for resource %d, not %d", ErrNotSingleTLVValue, e.ID, resourceID)
	}
	if e.Kind == TLVResource {
		return e.Value, nil
	}
	if len(e.Children) != 1 {
		return nil, fmt.Errorf("%w: a multiple resource with %d instances", ErrNotSingleTLVValue, len(e.Children))
	}
	return e.Children[0].Value, nil
}
