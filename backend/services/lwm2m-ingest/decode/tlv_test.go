// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package decode

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A Leshan 2.x Read of the whole Device instance /3/0 (trimmed to three resources): resources 0 and
// 1 as strings with an 8-bit length, 9 as a one-byte integer, and 6 (Available Power Sources) as a
// Multiple Resource of two one-byte integer instances.
var leshanDeviceInstance = []byte{
	0xC8, 0x00, 0x12, 'L', 'e', 's', 'h', 'a', 'n', ' ', 'D', 'e', 'm', 'o', ' ', 'D', 'e', 'v', 'i', 'c', 'e',
	0xC8, 0x01, 0x09, 'M', 'o', 'd', 'e', 'l', ' ', '5', '0', '0',
	0x86, 0x06, 0x41, 0x00, 0x01, 0x41, 0x01, 0x05,
	0xC1, 0x09, 0x57,
}

func TestDecodeTLVLeshanDeviceInstance(t *testing.T) {
	got, err := DecodeTLV(leshanDeviceInstance)
	require.NoError(t, err)
	require.Len(t, got, 4)
	assert.Equal(t, TLV{Kind: TLVResource, ID: 0, Value: []byte("Leshan Demo Device")}, got[0])
	assert.Equal(t, TLV{Kind: TLVResource, ID: 1, Value: []byte("Model 500")}, got[1])
	assert.Equal(t, TLV{Kind: TLVMultipleResource, ID: 6, Children: []TLV{
		{Kind: TLVResourceInstance, ID: 0, Value: []byte{0x01}},
		{Kind: TLVResourceInstance, ID: 1, Value: []byte{0x05}},
	}}, got[2])
	n, err := TLVInteger(got[3].Value)
	require.NoError(t, err)
	assert.Equal(t, int64(87), n)
}

func TestDecodeTLVWideIdentifierAndLengths(t *testing.T) {
	long := make([]byte, 300)
	for i := range long {
		long[i] = 'x'
	}
	// 0xF0 = resource, 16-bit id, 16-bit length.
	b := append([]byte{0xF0, 0x01, 0x2C, 0x01, 0x2C}, long...)
	// 0xD8 = resource, 8-bit id, 24-bit length.
	b = append(b, 0xD8, 0x07, 0x00, 0x00, 0x02, 'h', 'i')
	got, err := DecodeTLV(b)
	require.NoError(t, err)
	require.Len(t, got, 2)
	assert.Equal(t, uint16(300), got[0].ID)
	assert.Len(t, got[0].Value, 300)
	assert.Equal(t, []byte("hi"), got[1].Value)
}

func TestDecodeTLVRejectsMalformed(t *testing.T) {
	for name, b := range map[string][]byte{
		"empty":                      {},
		"truncated identifier":       {0xC1},
		"truncated 16-bit id":        {0xE1, 0x00},
		"truncated length field":     {0xD0, 0x00, 0x01},
		"value past the end":         {0xC8, 0x00, 0x12, 'L', 'e'},
		"inline length past the end": {0xC5, 0x00, 'a'},
		"trailing fragment":          {0xC1, 0x09, 0x57, 0xC1},
		"resource in multiple":       {0x83, 0x06, 0xC1, 0x00, 0x01},
		"instance in object":         {0x03, 0x00, 0x41, 0x00, 0x01},
		"object in object":           {0x02, 0x00, 0x00, 0x00},
		"child past its container":   {0x84, 0x06, 0x43, 0x00, 0x01, 0x02, 0x03},
	} {
		t.Run(name, func(t *testing.T) {
			got, err := DecodeTLV(b)
			require.Error(t, err)
			assert.True(t, errors.Is(err, ErrMalformedTLV), err.Error())
			assert.Nil(t, got, "a failed decode yields nothing, not a partial result")
		})
	}
}

func TestTLVInteger(t *testing.T) {
	for _, c := range []struct {
		in   []byte
		want int64
	}{
		{[]byte{0x7F}, 127},
		{[]byte{0x80}, -128},
		{[]byte{0xFF, 0xFE}, -2},
		{[]byte{0x00, 0x01, 0x00, 0x00}, 65536},
		{[]byte{0x80, 0, 0, 0, 0, 0, 0, 0}, -1 << 63},
	} {
		got, err := TLVInteger(c.in)
		require.NoError(t, err)
		assert.Equal(t, c.want, got)
	}
	for _, bad := range [][]byte{nil, {0, 0, 0}, {0, 0, 0, 0, 0}} {
		_, err := TLVInteger(bad)
		assert.ErrorIs(t, err, ErrMalformedTLV)
	}
}

func TestTLVResourceValue(t *testing.T) {
	ok := map[string][]byte{
		"resource":                 {0xC5, 0x03, '1', '.', '0', '.', '0'},
		"wrapped in instance":      {0x07, 0x00, 0xC5, 0x03, '1', '.', '0', '.', '0'},
		"single-instance multiple": {0x87, 0x03, 0x45, 0x00, '1', '.', '0', '.', '0'},
	}
	for name, b := range ok {
		v, err := TLVResourceValue(b, 3)
		require.NoError(t, err, name)
		assert.Equal(t, []byte("1.0.0"), v, name)
	}
	notSingle := map[string][]byte{
		"other resource":       {0xC1, 0x04, 0x01},
		"two entries":          {0xC1, 0x03, 0x01, 0xC1, 0x04, 0x01},
		"two instances":        {0x86, 0x03, 0x41, 0x00, 0x01, 0x41, 0x01, 0x02},
		"empty multiple":       {0x80, 0x03},
		"bare instance":        {0x41, 0x03, 0x01},
		"empty object":         {0x00, 0x00},
		"instance of other id": {0x03, 0x00, 0xC1, 0x04, 0x01},
	}
	for name, b := range notSingle {
		_, err := TLVResourceValue(b, 3)
		assert.ErrorIs(t, err, ErrNotSingleTLVValue, name)
	}
	_, err := TLVResourceValue([]byte{0xC5, 0x03, '1'}, 3)
	assert.ErrorIs(t, err, ErrMalformedTLV)
}
