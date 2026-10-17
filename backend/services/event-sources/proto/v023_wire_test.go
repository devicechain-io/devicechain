// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package proto

import (
	"bytes"
	"encoding/hex"
	"testing"

	"github.com/devicechain-io/dc-microservice/credential"
	"github.com/devicechain-io/dc-microservice/eventtime"
	"github.com/stretchr/testify/require"
)

// 🔴 THE ROLLBACK GUARANTEE, for the inbound stream. v0.23.0 is the first release whose
// device-management reads the compact format; it wrote legacy by default and compact only when
// configured to, and its reader was tested against what its own compact writer produced. So an
// inbound event this release's default producer writes is readable by a v0.23.0
// device-management (a rollback, or a pod that has not rolled yet) exactly when it is what
// v0.23.0's compact writer wrote for the same event.
//
// These bytes were captured by running MarshalUnresolvedEventAs(event, eventtime.WireCompact)
// on the v0.23.0 tag (0132165d4) over goldenInboundEvents; the binary seals are the text-form
// seals of mainInboundHex, converted without re-sealing. If one of them has to change, this
// release's default output is no longer v0.23.0's, and a rollback to v0.23.0 has to be proven
// again rather than assumed.
var v023CompactInboundHex = map[string]string{
	"alert out of range": "0a04687474701a03642d312a1e313530302d30312d30325430333a30343a30352e3030303030303030365a3223393939392d31322d33315432333a35393a35392e3939393939393939392d30353a3030380342490a250a054641554c5410031a016d2201732a14323330302d30312d30315430303a30303a30305a0a100a0545504f43483100000000000000000a0e0a0350524531ffffffffffffffff6001",
	"password location":  "0a046d7174741205616c742d311a0873656e736f722d39220372656c380142240a1a0a0434352e3512052d37332e361a0231304101fc2a7893f8de180a060a01311201324a0a4d5154545f4241534943520873656e736f722d3971159321bb93f8de1879471f39bb93f8de18820136d2e296a0eab07944e5e406c986548fb7c6bd02e4d37fa45064d09618ba17e406a39194a60f9f3808da3eaf5e52b3c853635106bdbf8c",
	"presence zero time": "0a056c776d326d1a03642d322a14303030312d30312d30315430303a30303a30305a3214303030312d30312d30315430303a30303a30305a380442290a09434f4e4e454354454412037265671a01372214323032362d31302d31365430393a33303a31355a6001",
	"relationship":       "0a04687474701a03642d3342170a08636f6e7461696e7312066465766963651a03642d3471471f39bb93f8de1879471f39bb93f8de18",
	"token measurement":  "0a04687474701a1164657669636570756c73652d30303030313802423d0a170a0c0a0474656d70120432312e3519b16409b393f8de180a0b0a090a0368756d120234300a150a0a0a0474656d70120232321901fc2a7893f8de184a0c4143434553535f544f4b454e71159321bb93f8de1879471f39bb93f8de188a0142d2e296a0ea936b95318d4bc38924e16e4c6676049839d50c02072c557f54e1862cc590066686a951f9b98e474d753f3b528125210446a6456f76f1843171afb6930e",
}

// What this release writes by default is, byte for byte, what v0.23.0's compact writer wrote.
func TestTheDefaultInboundBytesAreWhatV023ReadsAsCompact(t *testing.T) {
	require.Equal(t, eventtime.WireCompact, eventtime.ProducerWireFormat())
	events := goldenInboundEvents()
	require.Len(t, v023CompactInboundHex, len(events))
	for name, event := range events {
		got, err := MarshalUnresolvedEvent(event)
		require.NoError(t, err, name)
		require.Equal(t, v023CompactInboundHex[name], hex.EncodeToString(got),
			"%s: the default bytes are not what v0.23.0 wrote as compact, so v0.23.0 reading them is unproven", name)
	}
}

// And the other direction: compact bytes a v0.23.0 producer wrote decode here to the event it
// wrote, every instant to the nanosecond and every seal opening.
func TestV023CompactInboundBytesDecode(t *testing.T) {
	tr, err := credential.DeriveSecretTransit(bytes.Repeat([]byte{9}, 32))
	require.NoError(t, err)
	for name, event := range goldenInboundEvents() {
		encoded, err := hex.DecodeString(v023CompactInboundHex[name])
		require.NoError(t, err, name)
		got, err := UnmarshalUnresolvedEvent(encoded)
		require.NoError(t, err, name)
		legacy, err := hex.DecodeString(mainInboundHex[name])
		require.NoError(t, err, name)
		want, err := UnmarshalUnresolvedEvent(legacy)
		require.NoError(t, err, name)
		require.Equal(t, want, got, "%s: v0.23.0's compact bytes and the legacy bytes decode differently", name)
		require.True(t, got.OccurredTime.Equal(event.OccurredTime), name)
		require.True(t, got.ProcessedTime.Equal(event.ProcessedTime), name)
		require.Equal(t, event.SealedCredentialId, got.SealedCredentialId, name)
		require.Equal(t, event.SealedCredentialSecret, got.SealedCredentialSecret, name)
	}
	got, err := UnmarshalUnresolvedEvent(mustV023Hex(t, "password location"))
	require.NoError(t, err)
	pw, err := tr.Open("acme", "sensor-9", got.SealedCredentialSecret)
	require.NoError(t, err)
	require.Equal(t, "pw-golden", pw)
	got, err = UnmarshalUnresolvedEvent(mustV023Hex(t, "token measurement"))
	require.NoError(t, err)
	tok, err := tr.OpenToken("acme", "ACCESS_TOKEN", got.SealedCredentialId)
	require.NoError(t, err)
	require.Equal(t, "tok-golden-0123456789", tok)
}

func mustV023Hex(t *testing.T, name string) []byte {
	t.Helper()
	b, err := hex.DecodeString(v023CompactInboundHex[name])
	require.NoError(t, err)
	return b
}
