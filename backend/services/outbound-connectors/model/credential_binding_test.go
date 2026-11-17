// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/credbinding"
	dcgraphql "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/stretchr/testify/require"
)

func TestRepointingAConnectorWithoutItsSecretIsRefused(t *testing.T) {
	cases := []struct{ name, kind, before, after string }{
		{"type", "mqtt", mqttConfig, `{"addresses":["other:9092"],"topic":"t"}`},
		{"mqtt urls", "mqtt", mqttConfig, strings.ReplaceAll(mqttConfig, "broker:", "other:")},
		{"kafka addresses", "kafka", `{"addresses":["broker:9092"],"topic":"t"}`, `{"addresses":["other:9092"],"topic":"t"}`},
		{"kafka tls", "kafka", `{"addresses":["broker:9092"],"topic":"t","tls":true}`, `{"addresses":["broker:9092"],"topic":"t","tls":false}`},
		{"kafka sasl", "kafka", `{"addresses":["broker:9092"],"topic":"t","sasl":{"mechanism":"PLAIN","username":"u"}}`, `{"addresses":["broker:9092"],"topic":"t","sasl":{"mechanism":"SCRAM-SHA-256","username":"u"}}`},
		{"lorawan base", "lorawan_downlink", `{"networkServer":"chirpstack_v4","baseUrl":"https://server.example","integration":"in"}`, `{"networkServer":"chirpstack_v4","baseUrl":"https://other.example","integration":"in"}`},
		{"aws endpoint", "aws_sns", `{"region":"us-east-1","accessKeyId":"id","topicArn":"arn:x"}`, `{"region":"us-east-1","accessKeyId":"id","topicArn":"arn:x","endpoint":"https://other.example"}`},
	}
	for _, tc := range cases {
		for _, optimistic := range []bool{false, true} {
			t.Run(tc.name+map[bool]string{true: "/precondition", false: "/unconditional"}[optimistic], func(t *testing.T) {
				api := newTestApi(t)
				ctx := core.WithTenant(context.Background(), "acme")
				row, err := api.CreateConnector(ctx, &ConnectorCreateRequest{Token: "sink", Type: tc.kind, Config: tc.before, Secret: strp("old")})
				require.NoError(t, err)
				var expected *string
				if optimistic {
					expected = dcgraphql.FormatTime(row.UpdatedAt)
				}
				request := &ConnectorUpdateRequest{Config: dcgraphql.OptionalStringOf(tc.after)}
				if tc.name == "type" {
					request.Type = dcgraphql.OptionalStringOf("kafka")
				}
				_, err = api.UpdateConnector(ctx, "sink", request, expected)
				var refusal *credbinding.ResupplyRequiredError
				require.True(t, errors.As(err, &refusal), "%v", err)
				require.Equal(t, credbinding.Code, refusal.Extensions()["code"])
				rows, err := api.ConnectorsByToken(ctx, []string{"sink"})
				require.NoError(t, err)
				require.Equal(t, tc.before, string(rows[0].Config))
				require.Equal(t, tc.kind, rows[0].Type)
				require.True(t, row.UpdatedAt.Equal(rows[0].UpdatedAt))
				require.Equal(t, "old", secretValue(t, api, ctx, "sink"))
			})
		}
	}
}
func TestConnectorBindingControls(t *testing.T) {
	for _, mode := range []string{"neutral", "same", "anonymous", "replace", "clear"} {
		for _, optimistic := range []bool{false, true} {
			t.Run(mode+map[bool]string{false: "/unconditional", true: "/precondition"}[optimistic], func(t *testing.T) {
				api := newTestApi(t)
				ctx := core.WithTenant(context.Background(), "acme")
				secret := strp("old")
				if mode == "anonymous" {
					secret = nil
				}
				row, err := api.CreateConnector(ctx, &ConnectorCreateRequest{Token: "sink", Type: "mqtt", Config: mqttConfig, Secret: secret})
				require.NoError(t, err)
				config := strings.ReplaceAll(mqttConfig, "broker:", "other:")
				req := &ConnectorUpdateRequest{}
				want := "old"
				switch mode {
				case "neutral":
					config = strings.ReplaceAll(mqttConfig, "alerts", "other-topic")
				case "same":
					config = `{"topic":"alerts","qos":1,"urls":["tcp://broker:1883"]}`
				case "replace":
					req.Secret = dcgraphql.OptionalStringOf("new")
					want = "new"
				case "clear":
					req.Secret = dcgraphql.OptionalString{Set: true}
					want = ""
				case "anonymous":
					want = ""
				}
				var expected *string
				if optimistic {
					expected = dcgraphql.FormatTime(row.UpdatedAt)
				}
				req.Config = dcgraphql.OptionalStringOf(config)
				_, err = api.UpdateConnector(ctx, "sink", req, expected)
				require.NoError(t, err)
				require.Equal(t, want, secretValue(t, api, ctx, "sink"))
			})
		}
	}
}
func TestRollbackToADifferentDestinationNeedsTheSecret(t *testing.T) {
	api := newTestApi(t)
	ctx := core.WithTenant(context.Background(), "acme")
	_, err := api.CreateConnector(ctx, &ConnectorCreateRequest{Token: "sink", Type: "mqtt", Config: mqttConfig, Secret: strp("old")})
	require.NoError(t, err)
	_, err = api.PublishConnector(ctx, "sink", nil, nil, "tester", nil)
	require.NoError(t, err)
	_, err = api.RollbackConnector(ctx, "sink", 1)
	require.NoError(t, err)
	after := strings.ReplaceAll(mqttConfig, "broker:", "other:")
	_, err = api.UpdateConnector(ctx, "sink", &ConnectorUpdateRequest{Config: dcgraphql.OptionalStringOf(after), Secret: dcgraphql.OptionalStringOf("new")}, nil)
	require.NoError(t, err)
	_, err = api.RollbackConnector(ctx, "sink", 1)
	var refusal *credbinding.ResupplyRequiredError
	require.True(t, errors.As(err, &refusal))
	require.Equal(t, credbinding.Code, refusal.Extensions()["code"])
	require.Contains(t, err.Error(), "updateConnector")
	rows, err := api.ConnectorsByToken(ctx, []string{"sink"})
	require.NoError(t, err)
	require.Equal(t, after, string(rows[0].Config))
	require.Equal(t, "new", secretValue(t, api, ctx, "sink"))
}

func TestKafkaTopicEditKeepsCredential(t *testing.T) {
	for _, optimistic := range []bool{false, true} {
		t.Run(map[bool]string{false: "unconditional", true: "precondition"}[optimistic], func(t *testing.T) {
			api := newTestApi(t)
			ctx := core.WithTenant(context.Background(), "acme")
			before := `{"addresses":["broker:9092"],"topic":"one","sasl":{"mechanism":"PLAIN","username":"one"}}`
			after := `{"addresses":["broker:9092"],"topic":"two","clientId":"new-client","sasl":{"mechanism":"PLAIN","username":"two"}}`
			row, err := api.CreateConnector(ctx, &ConnectorCreateRequest{Token: "sink", Type: "kafka", Config: before, Secret: strp("old")})
			require.NoError(t, err)
			var expected *string
			if optimistic {
				expected = dcgraphql.FormatTime(row.UpdatedAt)
			}
			updated, err := api.UpdateConnector(ctx, "sink", &ConnectorUpdateRequest{Config: dcgraphql.OptionalStringOf(after)}, expected)
			require.NoError(t, err)
			require.Equal(t, after, string(updated.Config))
			require.Equal(t, "old", secretValue(t, api, ctx, "sink"))
		})
	}
}
