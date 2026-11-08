// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package downlink

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/plgd-dev/go-coap/v3/message"
	"github.com/plgd-dev/go-coap/v3/message/codes"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Eclipse Leshan answers a single-resource Read that carries no Accept option in LwM2M TLV
// (content format 11542), not text/plain. The fixtures below are the bytes a Leshan 2.x client
// puts on the wire for its demo Device object: one Resource-with-Value TLV per resource, a string
// as raw UTF-8, an integer in the smallest of 1/2/4/8 big-endian bytes.

func tlvFormat() *message.MediaType { cf := message.AppLwm2mTLV; return &cf }

// leshanTLV builds a Resource-with-Value TLV with an 8-bit identifier, picking the length encoding
// the way Leshan's TlvEncoder does: inline for 0-7 bytes, an 8-bit length field otherwise.
func leshanTLV(id byte, value []byte) string {
	if len(value) <= 7 {
		return string(append([]byte{0xC0 | byte(len(value)), id}, value...))
	}
	return string(append([]byte{0xC8, id, byte(len(value))}, value...))
}

func leshanDevice() map[string]infoReply {
	return map[string]infoReply{
		"/3/0/0": {code: codes.Content, body: leshanTLV(0, []byte("Leshan Demo Device")), cf: tlvFormat()},
		"/3/0/1": {code: codes.Content, body: leshanTLV(1, []byte("Model 500")), cf: tlvFormat()},
		"/3/0/2": {code: codes.Content, body: leshanTLV(2, []byte("LT-500-000-0001")), cf: tlvFormat()},
		"/3/0/3": {code: codes.Content, body: leshanTLV(3, []byte("1.0.0")), cf: tlvFormat()},
		"/3/0/9": {code: codes.Content, body: leshanTLV(9, []byte{0x57}), cf: tlvFormat()}, // 87
	}
}

func TestReadDeviceInfoDecodesLeshanTLV(t *testing.T) {
	// A pinned byte fixture, so the helper above cannot drift away from the wire form unnoticed.
	require.Equal(t, "\xC8\x00\x12Leshan Demo Device", leshanDevice()["/3/0/0"].body)
	require.Equal(t, "\xC1\x09\x57", leshanDevice()["/3/0/9"].body)

	info := ReadDeviceInfo(context.Background(), &infoConn{replies: leshanDevice()}, time.Second)
	assert.Empty(t, info.Missing)
	assert.Equal(t, map[string]string{
		"manufacturer":    "Leshan Demo Device",
		"modelNumber":     "Model 500",
		"serialNumber":    "LT-500-000-0001",
		"firmwareVersion": "1.0.0",
		"batteryLevel":    "87",
	}, info.Values)
}

func TestReadDeviceInfoCommandAnswersFromTLV(t *testing.T) {
	res := NewOps().Execute(context.Background(), &infoConn{replies: leshanDevice()}, CommandReadDeviceInfo, nil)
	require.True(t, res.Success, "err: %v", res.Err)
	var got map[string]any
	require.NoError(t, json.Unmarshal([]byte(*res.Payload), &got))
	assert.Equal(t, "Leshan Demo Device", got["manufacturer"])
	assert.Equal(t, float64(87), got["batteryLevel"], "a TLV integer answers as a JSON number")
	assert.Empty(t, got["missing"])
}

func TestHarvestWritesAttributesFromTLV(t *testing.T) {
	g := newHarvestRig(t, leshanDevice())
	g.d.Harvest("acme", "dev-1")
	g.waitWrites(t, 6)
	got := g.rec.byKey()
	assert.Equal(t, "Leshan Demo Device", got["manufacturer"]["value"])
	assert.Equal(t, "1.0.0", got["firmwareVersion"]["value"])
	assert.Equal(t, "LONG", got["batteryLevel"]["valueType"])
	assert.Equal(t, "87", got["batteryLevel"]["value"])
	assert.Equal(t, "[]", got[missingAttrKey]["value"])
}

// Integers wider than one byte, and a negative one, which is not a percentage.
func TestReadDeviceInfoTLVIntegerWidths(t *testing.T) {
	for name, tc := range map[string]struct {
		value []byte
		want  string // "" => missing
	}{
		"1 byte":           {[]byte{0x64}, "100"},
		"2 bytes":          {[]byte{0x00, 0x2A}, "42"},
		"4 bytes":          {[]byte{0x00, 0x00, 0x00, 0x05}, "5"},
		"8 bytes":          {[]byte{0, 0, 0, 0, 0, 0, 0, 0x00}, "0"},
		"negative":         {[]byte{0xFF}, ""},
		"above 100":        {[]byte{0x00, 0xFA}, ""},
		"3 bytes (no int)": {[]byte{0x00, 0x00, 0x05}, ""},
	} {
		t.Run(name, func(t *testing.T) {
			conn := &infoConn{replies: map[string]infoReply{
				"/3/0/9": {code: codes.Content, body: leshanTLV(9, tc.value), cf: tlvFormat()},
			}}
			info := ReadDeviceInfo(context.Background(), conn, time.Second)
			if tc.want == "" {
				assert.NotContains(t, info.Values, "batteryLevel")
				return
			}
			assert.Equal(t, tc.want, info.Values["batteryLevel"])
		})
	}
}

// A Multiple-Resource TLV wrapping Resource-Instance TLVs. A single instance is the one value; more
// than one is not a single value, and choosing one of them would be inventing an answer.
func TestReadDeviceInfoMultiInstanceTLV(t *testing.T) {
	// 0x88 = Multiple Resource, 8-bit id, 8-bit length. 0x45 = Resource Instance, inline length 5.
	one := "\x88\x03\x07" + "\x45\x00" + "1.0.0"
	two := "\x88\x01\x0E" + "\x45\x00" + "1.0.0" + "\x45\x01" + "2.0.0"
	info := ReadDeviceInfo(context.Background(), &infoConn{replies: map[string]infoReply{
		"/3/0/3": {code: codes.Content, body: one, cf: tlvFormat()},
		"/3/0/1": {code: codes.Content, body: two, cf: tlvFormat()},
	}}, time.Second)
	assert.Equal(t, "1.0.0", info.Values["firmwareVersion"])
	assert.NotContains(t, info.Values, "modelNumber")
	assert.Contains(t, reasonFor(info, "modelNumber"), "2 instances")
}

// Some clients wrap the resource in its Object Instance TLV even for a single-resource Read.
func TestReadDeviceInfoObjectInstanceWrappedTLV(t *testing.T) {
	// 0x08 = Object Instance, 8-bit id, 8-bit length; it wraps C5 03 "1.0.0" (7 bytes).
	body := "\x08\x00\x07" + "\xC5\x03" + "1.0.0"
	info := ReadDeviceInfo(context.Background(), &infoConn{replies: map[string]infoReply{
		"/3/0/3": {code: codes.Content, body: body, cf: tlvFormat()},
	}}, time.Second)
	assert.Equal(t, "1.0.0", info.Values["firmwareVersion"])
}

// Malformed or mismatched TLV is recorded as missing with a reason. It is never read as the raw
// bytes, and never as a plausible empty value.
func TestReadDeviceInfoRejectsMalformedTLV(t *testing.T) {
	cases := map[string]struct {
		path, body, reason string
	}{
		"truncated value":     {"/3/0/0", "\xC8\x00\x12Leshan", "malformed TLV"},
		"trailing bytes":      {"/3/0/1", leshanTLV(1, []byte("Model 500")) + "\x00", "malformed TLV"},
		"wrong resource id":   {"/3/0/2", leshanTLV(7, []byte("LT-500")), "resource 7"},
		"empty body":          {"/3/0/3", "", "malformed TLV"},
		"instance at the top": {"/3/0/9", "\x41\x00\x57", "resource instance"},
	}
	replies := map[string]infoReply{}
	for _, c := range cases {
		replies[c.path] = infoReply{code: codes.Content, body: c.body, cf: tlvFormat()}
	}
	info := ReadDeviceInfo(context.Background(), &infoConn{replies: replies}, time.Second)
	assert.Empty(t, info.Values)
	for name, c := range cases {
		key := keyForPath(c.path)
		assert.Contains(t, reasonFor(info, key), c.reason, name)
	}
}

// A format this adapter does not decode is named in the reason; the bytes are not guessed at.
func TestReadDeviceInfoNamesAnUndecodedFormat(t *testing.T) {
	cbor := message.AppCBOR
	info := ReadDeviceInfo(context.Background(), &infoConn{replies: map[string]infoReply{
		"/3/0/0": {code: codes.Content, body: "\x64Acme", cf: &cbor},
	}}, time.Second)
	assert.NotContains(t, info.Values, "manufacturer")
	assert.Contains(t, reasonFor(info, "manufacturer"), "unsupported content format 60")
}

func reasonFor(info DeviceInfo, key string) string {
	for _, m := range info.Missing {
		if m.Resource == key {
			return m.Reason
		}
	}
	return ""
}

func keyForPath(path string) string {
	for _, r := range deviceInfoResources {
		if r.Path == path {
			return r.Key
		}
	}
	return ""
}
