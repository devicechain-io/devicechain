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

// TestGeneratedConfigsAcceptedByBento is the cross-check that connectorspec's Bento field
// mappings are actually valid: each config it generates must parse + lint + build as a real
// Bento output. It needs no broker — Build() constructs the output without dialing. If a
// field name drifts from Bento's schema, AddOutputYAML/Build fails here.
func TestGeneratedConfigsAcceptedByBento(t *testing.T) {
	cases := []struct {
		typ, config string
	}{
		{"mqtt", `{"urls":["tcp://b:1883"],"topic":"alerts","qos":1,"clientId":"dc","username":"u"}`},
		{"kafka", `{"addresses":["b:9092"],"topic":"t","clientId":"c","tls":true,"sasl":{"mechanism":"PLAIN","username":"u"}}`},
		{"aws_sns", `{"region":"us-east-1","topicArn":"arn:aws:sns:us-east-1:1:t","accessKeyId":"AKIA"}`},
		{"aws_sqs", `{"region":"us-east-1","url":"https://sqs.example/q","accessKeyId":"AKIA"}`},
	}
	for _, tc := range cases {
		out, err := connectorspec.BuildOutput(tc.typ, []byte(tc.config), "s3cret")
		require.NoError(t, err, "%s: build output", tc.typ)

		builder := service.NewStreamBuilder()
		builder.SetLeveledLogger(discardLogger{})
		_, err = builder.AddProducerFunc()
		require.NoError(t, err)
		require.NoError(t, builder.AddOutputYAML(out), "%s: generated config must be a valid Bento output", tc.typ)
		_, err = builder.Build()
		require.NoError(t, err, "%s: the stream with the generated output must build", tc.typ)
	}
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
