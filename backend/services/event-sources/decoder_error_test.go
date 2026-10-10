// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"testing"

	"github.com/devicechain-io/dc-event-sources/config"
	"github.com/stretchr/testify/require"
)

// An unknown decoder type must be refused naming the DECODER type the operator wrote, not
// the event source's transport type, which is a different setting and is valid.
func TestUnknownDecoderTypeNamesTheDecoder(t *testing.T) {
	_, err := createDecoder(config.EventSource{
		Id:      "src-1",
		Type:    "mqtt",
		Decoder: config.EventDecoder{Type: "protobuf-nope"},
	})
	require.Error(t, err)
	require.EqualError(t, err, "unknown decoder type: protobuf-nope")
}
