// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package codec decodes and encodes Sparkplug B protobuf payloads (ADR-069 SP0).
//
// It is the seam between the wire and the session machine (SP2): the transport
// hands it the raw protobuf body of a Sparkplug message (NBIRTH / NDATA / NDEATH
// / DBIRTH / DDATA / DDEATH — the MQTT topic carries the message TYPE, the payload
// carries the metrics), and it returns the generated Payload. The schema it
// targets is the Apache-clean, house-generated one in the sibling proto package
// (no Eclipse Tahu / EPL dependency).
package codec

import (
	sppb "github.com/devicechain-io/dc-sparkplug-ingest/proto"
	"google.golang.org/protobuf/proto"
)

// Decode unmarshals a Sparkplug B payload body. The caller has already stripped
// the MQTT framing and knows the message type from the topic; this operates on
// the raw protobuf bytes only.
func Decode(b []byte) (*sppb.Payload, error) {
	p := &sppb.Payload{}
	if err := proto.Unmarshal(b, p); err != nil {
		return nil, err
	}
	return p, nil
}

// Encode marshals a Sparkplug B payload — used for the host's own outbound
// messages (the NCMD `Node Control/Rebirth` control at GA) and by tests.
func Encode(p *sppb.Payload) ([]byte, error) {
	return proto.Marshal(p)
}
