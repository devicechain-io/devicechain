// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package publish

import (
	"testing"

	"github.com/devicechain-io/dc-outbound-connectors/connectorspec"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/warpstreamlabs/bento/public/service"
)

// TestGeneratedMQTTConfigAcceptedByBento is the cross-check that connectorspec's Bento
// field mapping is actually valid: the config it generates must parse + lint as a real
// Bento mqtt output. It needs no broker — Build() constructs the output without dialing.
// If a field name drifts from Bento's schema, AddOutputYAML/Build fails here.
func TestGeneratedMQTTConfigAcceptedByBento(t *testing.T) {
	out, err := connectorspec.BuildOutput("mqtt",
		[]byte(`{"urls":["tcp://b:1883"],"topic":"alerts","qos":1,"clientId":"dc","username":"u"}`), "p4ss")
	require.NoError(t, err)

	builder := service.NewStreamBuilder()
	builder.SetLeveledLogger(discardLogger{})
	_, err = builder.AddProducerFunc()
	require.NoError(t, err)
	require.NoError(t, builder.AddOutputYAML(out), "generated mqtt config must be a valid Bento output")
	_, err = builder.Build()
	require.NoError(t, err, "the stream with the generated mqtt output must build")
}

// TestSelectiveRegistration is the supply-chain guard: only the shipped connector outputs
// (plus pure plumbing) are registered. An un-shipped output — here cassandra, which lives
// only under public/components/all or its own un-imported component — must be UNKNOWN. If
// someone imports public/components/all, cassandra registers and this test fails, catching
// the dep-tree/supply-chain regression the ADR-060 red line forbids.
func TestSelectiveRegistration(t *testing.T) {
	builder := service.NewStreamBuilder()
	builder.SetLeveledLogger(discardLogger{})
	if _, err := builder.AddProducerFunc(); err != nil {
		t.Fatalf("producer func: %v", err)
	}
	err := builder.AddOutputYAML("cassandra: {}")
	assert.Error(t, err, "cassandra must NOT be registered — only the shipped outputs may be")

	// mqtt (shipped) MUST be registered — a sanity anchor so the test can't pass merely by
	// everything being unknown.
	fresh := service.NewStreamBuilder()
	fresh.SetLeveledLogger(discardLogger{})
	_, _ = fresh.AddProducerFunc()
	assert.NoError(t, fresh.AddOutputYAML(`mqtt: {urls: ["tcp://b:1883"], topic: t}`),
		"mqtt must be registered")
}
