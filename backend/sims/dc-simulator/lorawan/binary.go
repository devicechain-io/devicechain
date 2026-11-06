// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package lorawan

import (
	"encoding/binary"
	"math"
)

// BinaryFPort is the application port of a binary-mode uplink.
const BinaryFPort = 2

// BinaryFrame is the application frame of a binary-mode uplink, six bytes:
//
//	offset 0  u8       layout version, 1
//	offset 1  i16 BE   temperature, tenths of a degree
//	offset 3  u8       humidity, percent
//	offset 4  u8       battery voltage, tenths of a volt
//	offset 5  bit 0    door open
//
// Every value is one ReadingsOf makes, so BinaryDecoder reproduces them exactly.
func BinaryFrame(r Readings) []byte {
	b := make([]byte, 6)
	b[0] = 1
	binary.BigEndian.PutUint16(b[1:], uint16(int16(math.Round(r.Temperature*10))))
	b[3] = uint8(math.Round(r.Humidity))
	b[4] = uint8(math.Round(r.Voltage * 10))
	if r.Door {
		b[5] = 1
	}
	return b
}

// BinaryDecoder is the payload decoder document that reads BinaryFrame: paste it into a device
// profile's Decoder tab (or set it with setDeviceProfileDecoder), over a profile that declares the
// metrics temperature, humidity and battery.voltage as doubles and door as a boolean. It carries
// one saved vector, so publishing the profile checks the layout. dc-simulator prints it with
// --lorawan-print-decoder.
const BinaryDecoder = `{
  "kind": "CEL",
  "envVersion": 1,
  "input": "FRAME",
  "sections": [
    {
      "name": "telemetry",
      "when": "fPort == 2 && u8(frame, 0) == 1",
      "outputs": [
        {"key": "temperature", "expr": "double(i16be(frame, 1)) / 10.0"},
        {"key": "humidity", "expr": "double(u8(frame, 3))"},
        {"key": "battery.voltage", "expr": "double(u8(frame, 4)) / 10.0"},
        {"key": "door", "expr": "bit(frame, 5, 0)"}
      ]
    }
  ],
  "vectors": [
    {
      "name": "door open",
      "input": {"frameHex": "0100fa2d1f01", "fPort": 2},
      "expect": {"temperature": 25, "humidity": 45, "battery.voltage": 3.1, "door": 1}
    }
  ]
}`
