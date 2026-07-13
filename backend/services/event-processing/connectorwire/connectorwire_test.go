// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package connectorwire

import (
	"testing"
	"time"
)

// TestConnectorDispatchRoundTrip proves the wire contract marshals and decodes losslessly for both
// discriminated variants — the producer (event-processing) and the consumer (outbound-connectors,
// slice C3) must agree on the JSON shape.
func TestConnectorDispatchRoundTrip(t *testing.T) {
	occurred := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)
	cases := []*ConnectorDispatchRequest{
		{
			Kind: ConnectorKindHTTPCall, Tenant: "acme", DeviceToken: "device-1", RuleID: "acme/p@1/r1",
			Edge: "raised", OccurredTime: occurred, IdempotencyKey: "tok1", Payload: `{"a":1}`,
			HTTPCall: &HTTPCallDispatch{URL: "https://x/y", Method: "POST",
				Headers: map[string]string{"X-Env": "prod"}, SecretRef: "h/acme/auth", TimeoutMs: 5000},
		},
		{
			Kind: ConnectorKindPublish, Tenant: "acme", DeviceToken: "device-1", RuleID: "acme/p@1/r1",
			Edge: "raised", OccurredTime: occurred, IdempotencyKey: "tok2",
			Publish: &PublishDispatch{ConnectorRef: "kafka-main", TimeoutMs: 2000},
		},
	}
	for _, in := range cases {
		b, err := MarshalConnectorDispatchRequest(in)
		if err != nil {
			t.Fatalf("marshal %s: %v", in.Kind, err)
		}
		out, err := UnmarshalConnectorDispatchRequest(b)
		if err != nil {
			t.Fatalf("unmarshal %s: %v", in.Kind, err)
		}
		if out.Kind != in.Kind || out.Tenant != in.Tenant || out.IdempotencyKey != in.IdempotencyKey ||
			out.Payload != in.Payload || !out.OccurredTime.Equal(in.OccurredTime) {
			t.Fatalf("%s: envelope round-trip mismatch: %+v", in.Kind, out)
		}
		switch in.Kind {
		case ConnectorKindHTTPCall:
			if out.HTTPCall == nil || out.Publish != nil {
				t.Fatalf("httpCall variant lost: %+v", out)
			}
			if out.HTTPCall.URL != in.HTTPCall.URL || out.HTTPCall.SecretRef != in.HTTPCall.SecretRef ||
				out.HTTPCall.Headers["X-Env"] != "prod" || out.HTTPCall.TimeoutMs != 5000 {
				t.Fatalf("httpCall config round-trip mismatch: %+v", out.HTTPCall)
			}
		case ConnectorKindPublish:
			if out.Publish == nil || out.HTTPCall != nil {
				t.Fatalf("publish variant lost: %+v", out)
			}
			if out.Publish.ConnectorRef != in.Publish.ConnectorRef || out.Publish.TimeoutMs != 2000 {
				t.Fatalf("publish config round-trip mismatch: %+v", out.Publish)
			}
		}
	}
}
