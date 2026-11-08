// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package lpwan is the command downlink to a LoRaWAN device through its network server: it
// consumes the device-commands stream for devices that report through an inbound integration,
// encodes the command into a LoRaWAN application payload, enqueues it on the network server's
// downlink API, then answers the command's outcome.
//
// Every network call goes through the platform egress guard, and the network server's credential
// is the connector's sealed secret; nothing here logs or returns either.
package lpwan

import (
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
)

// MaxPayloadBytes is the LoRaWAN application-payload ceiling (the largest FRMPayload any region and
// data rate allows). A command bigger than this cannot be sent by anyone, so it is refused before a
// network call; the network server still applies the tighter limit of the device's current data
// rate and answers it with a 4xx.
const MaxPayloadBytes = 242

// Params is a command's downlink contract in slice 1: raw bytes for a port. The profile's typed
// command definition validates the same keys at enqueue; this parser is the enforcement point and
// does not trust that it ran.
type Params struct {
	// FPort is the LoRaWAN application port, 1..223 (0 is MAC commands, 224 is test, 225-255 reserved).
	FPort int
	// Data is the application payload.
	Data []byte
	// Confirmed asks the network server for a confirmed downlink.
	Confirmed bool
}

// ParamError is a refusal naming the offending parameter. It is always safe to show the tenant: it
// carries no payload bytes.
type ParamError struct{ Param, Reason string }

func (e *ParamError) Error() string { return fmt.Sprintf("parameter %q: %s", e.Param, e.Reason) }

func refuse(param, format string, args ...any) error {
	return &ParamError{Param: param, Reason: fmt.Sprintf(format, args...)}
}

// ParseParams reads a command's payload as downlink params. It rejects unknown keys, a missing or
// out-of-range fPort, neither or both of hex/base64, an undecodable or oversized payload, and a
// non-boolean confirmed.
func ParseParams(raw []byte) (Params, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return Params{}, refuse("fPort", "a downlink command needs parameters: fPort and one of hex or base64")
	}
	var obj map[string]json.RawMessage
	dec := json.NewDecoder(bytes.NewReader(raw))
	if err := dec.Decode(&obj); err != nil || obj == nil {
		return Params{}, refuse("(payload)", "the command parameters must be a JSON object")
	}
	if dec.More() {
		return Params{}, refuse("(payload)", "trailing data after the parameters object")
	}
	var p Params
	var hexText, b64Text *string
	for k, v := range obj {
		switch k {
		case "fPort":
			// A quoted number decodes into json.Number; a port is an integer, not text.
			if t := bytes.TrimSpace(v); len(t) == 0 || t[0] == '"' {
				return Params{}, refuse("fPort", "must be an integer")
			}
			var n json.Number
			d := json.NewDecoder(bytes.NewReader(v))
			d.UseNumber()
			if err := d.Decode(&n); err != nil {
				return Params{}, refuse("fPort", "must be an integer")
			}
			i, err := n.Int64()
			if err != nil {
				return Params{}, refuse("fPort", "must be an integer")
			}
			if i < 1 || i > 223 {
				return Params{}, refuse("fPort", "must be 1 to 223")
			}
			p.FPort = int(i)
		case "hex":
			var s string
			if err := json.Unmarshal(v, &s); err != nil {
				return Params{}, refuse("hex", "must be a string")
			}
			hexText = &s
		case "base64":
			var s string
			if err := json.Unmarshal(v, &s); err != nil {
				return Params{}, refuse("base64", "must be a string")
			}
			b64Text = &s
		case "confirmed":
			if err := json.Unmarshal(v, &p.Confirmed); err != nil {
				return Params{}, refuse("confirmed", "must be true or false")
			}
		default:
			return Params{}, refuse(clip(k), "is not a downlink parameter (fPort, hex, base64, confirmed)")
		}
	}
	if p.FPort == 0 {
		return Params{}, refuse("fPort", "is required")
	}
	switch {
	case hexText != nil && b64Text != nil:
		return Params{}, refuse("hex", "give hex or base64, not both")
	case hexText == nil && b64Text == nil:
		return Params{}, refuse("hex", "give the payload as hex or base64")
	case hexText != nil:
		// Bound the TEXT before decoding so an oversized parameter costs nothing.
		if len(*hexText) > 2*MaxPayloadBytes {
			return Params{}, refuse("hex", "decodes to more than %d bytes", MaxPayloadBytes)
		}
		b, err := hex.DecodeString(*hexText)
		if err != nil {
			return Params{}, refuse("hex", "is not valid hexadecimal")
		}
		p.Data = b
	default:
		if len(*b64Text) > base64.StdEncoding.EncodedLen(MaxPayloadBytes) {
			return Params{}, refuse("base64", "decodes to more than %d bytes", MaxPayloadBytes)
		}
		b, err := base64.StdEncoding.Strict().DecodeString(*b64Text)
		if err != nil {
			return Params{}, refuse("base64", "is not valid standard base64")
		}
		p.Data = b
	}
	if len(p.Data) > MaxPayloadBytes {
		return Params{}, refuse("payload", "is %d bytes; the LoRaWAN ceiling is %d", len(p.Data), MaxPayloadBytes)
	}
	if len(p.Data) == 0 {
		return Params{}, refuse("payload", "is empty")
	}
	return p, nil
}

// clip bounds a tenant-supplied name echoed back in a refusal.
func clip(s string) string {
	if len(s) > 32 {
		return s[:32] + "..."
	}
	return s
}
