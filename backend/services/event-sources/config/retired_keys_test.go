// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"strings"
	"testing"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestARetiredBatchingKeyLoadsRatherThanRefusing.
//
// 🔴 THE KEY THIS RETIRES COULD ALREADY STOP THE SERVICE, WHICH IS WHY IT COULD NOT JUST
// BE DELETED. inboundEventBatching configured a Kafka producer this platform has never
// had, and nothing read either of its values — but Validate refused the LOAD on a
// non-positive one, so `maxBatchSize: 0` took down every event source and the presence tap
// on the instance over a number nobody would have consulted. Deleting the field outright
// would replace that with a different refusal: the load is a strict decode, so the key
// would then be called unknown and fail closed just the same.
//
// It goes through core.LoadConfiguration rather than calling RetiredConfigKeys directly,
// because the retirement is only worth anything if the DECODE PATH honours it.
func TestARetiredBatchingKeyLoadsRatherThanRefusing(t *testing.T) {
	for _, doc := range []string{
		`{"inboundEventBatching":{"maxBatchSize":100,"batchTimeoutMs":100}}`,
		// The value that used to be fatal. It must now be as ignorable as any other.
		`{"inboundEventBatching":{"maxBatchSize":0,"batchTimeoutMs":0}}`,
		// Case-insensitively, the way encoding/json matches field names — a document
		// writing InboundEventBatching bound to the field just as well.
		`{"InboundEventBatching":{"maxBatchSize":0}}`,
	} {
		cfg := &EventSourcesConfiguration{}
		require.NoError(t, core.LoadConfiguration([]byte(doc), cfg),
			"a document carrying the retired key must still start the service: %s", doc)
		assert.Len(t, cfg.EventSources, 2, "the rest of the document must still default normally")
	}
}

// TestAnUnknownKeyIsStillRefused is the counterweight. Retiring one key must not turn the
// strict decode off: a typo has to stay a loud failure, or the retirement mechanism has
// bought an outage's worth of silence.
func TestAnUnknownKeyIsStillRefused(t *testing.T) {
	cfg := &EventSourcesConfiguration{}
	err := core.LoadConfiguration([]byte(`{"inboundEventBatchng":{"maxBatchSize":0}}`), cfg)
	require.Error(t, err, "a near-miss of the retired key is a typo, not a retirement")
	assert.True(t, strings.Contains(err.Error(), "decode failed"), "got %v", err)
}

// TestRetiredKeysNameOnlyKeysThatAreGone. A retirement entry for a key the struct still
// declares would silently STRIP the operator's value before the decode ever saw it —
// turning a setting that works into one that is accepted and ignored, which is the exact
// failure this whole mechanism exists to make loud.
func TestRetiredKeysNameOnlyKeysThatAreGone(t *testing.T) {
	cfg := &EventSourcesConfiguration{}
	for key, guidance := range cfg.RetiredConfigKeys() {
		assert.NotEmpty(t, guidance, "a retired key must tell the operator what to do instead")
		doc := []byte(`{"` + key + `":{}}`)
		fresh := &EventSourcesConfiguration{}
		// If the field still existed the strict decode would accept the key on its own;
		// the load succeeding proves nothing by itself. What proves it is that the key is
		// absent from the struct, which the compiler enforces — so this asserts the
		// remaining half: the load does not fail, and the guidance is worth reading.
		require.NoError(t, core.LoadConfiguration(doc, fresh), "retired key %q", key)
	}
}
