// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package payloaddecode

import "strconv"

// lppType is one record type of the Cayenne LPP type table. Values are divided by div (never
// multiplied by a reciprocal, so the result is correctly rounded on every architecture).
type lppType struct {
	name   string
	parts  []string // axis suffixes; one empty entry for a scalar
	width  int      // bytes per part
	signed bool
	div    float64
}

var lppTypes = map[byte]lppType{
	0:   {"digital_in", []string{""}, 1, false, 1},
	1:   {"digital_out", []string{""}, 1, false, 1},
	2:   {"analog_in", []string{""}, 2, true, 100},
	3:   {"analog_out", []string{""}, 2, true, 100},
	101: {"illuminance", []string{""}, 2, false, 1},
	102: {"presence", []string{""}, 1, false, 1},
	103: {"temperature", []string{""}, 2, true, 10},
	104: {"humidity", []string{""}, 1, false, 2},
	113: {"accelerometer", []string{"x", "y", "z"}, 2, true, 1000},
	115: {"barometer", []string{""}, 2, false, 10},
	134: {"gyrometer", []string{"x", "y", "z"}, 2, true, 100},
}

// decodeLPP decodes a Cayenne LPP frame: records of (channel, type, data...). Keys are the
// type name, then the axis for a multi-part type, then the channel: temperature_1,
// accelerometer_x_2, gps_lat_3. A truncated record, an unknown type or a repeated key is an
// error naming the offset of the record.
func decodeLPP(b []byte) (map[string]float64, error) {
	out := make(map[string]float64, len(b)/3+1)
	put := func(off int, key string, v float64) error {
		if _, dup := out[key]; dup {
			return &nativeError{Fn: "lpp.decode", Offset: off, Msg: "key " + key + " appears twice"}
		}
		out[key] = v
		return nil
	}
	for i := 0; i < len(b); {
		start := i
		if len(b)-i < 2 {
			return nil, &nativeError{Fn: "lpp.decode", Offset: start, Msg: "truncated record header"}
		}
		ch, typ := b[i], b[i+1]
		i += 2
		chs := strconv.Itoa(int(ch))
		if typ == 136 { // GPS: three 24-bit signed values
			if len(b)-i < 9 {
				return nil, &nativeError{Fn: "lpp.decode", Offset: start, Msg: "truncated gps record"}
			}
			for _, p := range []struct {
				name string
				div  float64
			}{{"lat", 10000}, {"lon", 10000}, {"alt", 100}} {
				u := uint32(b[i])<<16 | uint32(b[i+1])<<8 | uint32(b[i+2])
				v := float64(int32(u<<8) >> 8)
				if err := put(start, "gps_"+p.name+"_"+chs, v/p.div); err != nil {
					return nil, err
				}
				i += 3
			}
			continue
		}
		t, ok := lppTypes[typ]
		if !ok {
			return nil, &nativeError{Fn: "lpp.decode", Offset: start, Msg: "unknown record type " + strconv.Itoa(int(typ))}
		}
		if len(b)-i < t.width*len(t.parts) {
			return nil, &nativeError{Fn: "lpp.decode", Offset: start, Msg: "truncated " + t.name + " record"}
		}
		for _, axis := range t.parts {
			var v float64
			if t.width == 1 {
				v = float64(b[i])
			} else {
				u := uint16(b[i])<<8 | uint16(b[i+1])
				if t.signed {
					v = float64(int16(u))
				} else {
					v = float64(u)
				}
			}
			i += t.width
			key := t.name + "_" + chs
			if axis != "" {
				key = t.name + "_" + axis + "_" + chs
			}
			if err := put(start, key, v/t.div); err != nil {
				return nil, err
			}
		}
	}
	return out, nil
}
