// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package lorawan

import (
	"encoding/json"
	"fmt"
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/devicechain-io/dc-microservice/calcfield"
	"github.com/devicechain-io/dc-microservice/payloaddecode"
)

// profile declares the metrics the binary-mode decoder writes.
type profile map[string]calcfield.Type

func (p profile) MetricType(k string) (calcfield.Type, bool) { t, ok := p[k]; return t, ok }
func (p profile) ValidateMeasurement(string, string) error   { return nil }

var declared = profile{
	"temperature": calcfield.Double, "humidity": calcfield.Double,
	"battery.voltage": calcfield.Double, "door": calcfield.Boolean,
}

func plan(t *testing.T) *payloaddecode.Plan {
	t.Helper()
	doc, err := payloaddecode.ParseDocument([]byte(BinaryDecoder))
	require.NoError(t, err)
	p, err := payloaddecode.Compile(doc, declared)
	require.NoError(t, err)
	return p
}

func TestBinaryDecoderPassesItsOwnVector(t *testing.T) {
	vs := plan(t).RunVectors()
	require.Len(t, vs, 1)
	for _, v := range vs {
		require.True(t, v.OK, "%s: %s", v.Name, v.Error)
	}
}

// A binary-mode uplink goes through the platform's own codec and then the shipped decoder, and the
// readings are the ones the device's values were made from, for both network servers, every uplink
// of several devices.
func TestBinaryUplinkDecodesToTheDeviceReadings(t *testing.T) {
	p := plan(t)
	for _, ns := range []string{ChirpStack, TTS} {
		for _, eui := range []string{"70b3d57ed0012345", "0004a30b001c0530", "ffffffffffffffff"} {
			for n := 0; n < 12; n++ {
				t.Run(fmt.Sprintf("%s/%s/%d", ns, eui, n), func(t *testing.T) {
					c := cfg(ns)
					c.Binary = true
					r, err := Build(c, "run1", eui, n, fixedNow)
					require.NoError(t, err)
					res := decode(t, ns, r, false)
					require.Len(t, res.Uplinks, 1)
					u := res.Uplinks[0]
					require.Nil(t, u.Decoded, "a network server with no codec sends no decoded object")
					require.NotEmpty(t, u.Frame)
					require.EqualValues(t, BinaryFPort, u.FPort)

					out, derr := p.Decode(payloaddecode.Input{Frame: u.Frame, LoRa: &payloaddecode.LoRa{FPort: int64(u.FPort), FCnt: u.FCnt}})
					require.Nil(t, derr)
					require.Equal(t, "telemetry", out.Section)
					got := map[string]float64{}
					for _, rd := range out.Readings {
						f, err := strconv.ParseFloat(rd.Value, 64)
						require.NoError(t, err)
						got[rd.Key] = f
					}
					want := ReadingsOf(eui, n)
					door := 0.0
					if want.Door {
						door = 1
					}
					require.Equal(t, map[string]float64{
						"temperature": want.Temperature, "humidity": want.Humidity,
						"battery.voltage": want.Voltage, "door": door,
					}, got)
				})
			}
		}
	}
}

// A port other than 2 is not the layout BinaryDecoder reads: it must not be decoded as telemetry.
func TestBinaryDecoderRefusesOtherPorts(t *testing.T) {
	_, derr := plan(t).Decode(payloaddecode.Input{Frame: BinaryFrame(ReadingsOf("70B3D57ED0012345", 1)), LoRa: &payloaddecode.LoRa{FPort: 1, FCnt: 1}})
	require.NotNil(t, derr)
	require.Equal(t, payloaddecode.ReasonNoSection, derr.Reason)
}

func TestBinaryFrameLayout(t *testing.T) {
	require.Equal(t, []byte{1, 0x00, 0xfa, 45, 31, 1}, BinaryFrame(Readings{Temperature: 25, Humidity: 45, Voltage: 3.1, Door: true}))
	require.Equal(t, []byte{1, 0xff, 0x9c, 45, 31, 0}, BinaryFrame(Readings{Temperature: -10, Humidity: 45, Voltage: 3.1}), "temperature is signed")
}

// The default mode is untouched: the object is still sent on port 1.
func TestDefaultModeStillCarriesTheDecodedObject(t *testing.T) {
	for _, ns := range []string{ChirpStack, TTS} {
		r, err := Build(cfg(ns), "run1", "70b3d57ed0012345", 1, fixedNow)
		require.NoError(t, err)
		u := decode(t, ns, r, false).Uplinks[0]
		require.NotNil(t, u.Decoded)
		require.EqualValues(t, 1, u.FPort)
	}
}

func TestBinaryDecoderIsTheDocumentFormat(t *testing.T) {
	var m map[string]any
	require.NoError(t, json.Unmarshal([]byte(BinaryDecoder), &m))
}

// The simulator's own temperatures are never negative, so the sign is pinned here: a decoder that
// read the field unsigned would store 6,553.6 for a frost.
func TestBinaryDecoderReadsANegativeTemperature(t *testing.T) {
	out, derr := plan(t).Decode(payloaddecode.Input{
		Frame: BinaryFrame(Readings{Temperature: -10, Humidity: 45, Voltage: 3.1}), LoRa: &payloaddecode.LoRa{FPort: BinaryFPort},
	})
	require.Nil(t, derr)
	got := map[string]string{}
	for _, r := range out.Readings {
		got[r.Key] = r.Value
	}
	f, err := strconv.ParseFloat(got["temperature"], 64)
	require.NoError(t, err)
	require.Equal(t, -10.0, f)
}
