// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package connectorwire

import (
	"strings"
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

// TestValidate covers the consumer-side fail-closed structural gate: well-formed requests pass, and
// every malformed shape a forged/corrupt message could take is rejected (so the outbound-connectors
// consumer treats it as terminal poison rather than acting on it).
func TestValidate(t *testing.T) {
	occurred := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)
	good := []*ConnectorDispatchRequest{
		{Kind: ConnectorKindHTTPCall, Tenant: "acme", OccurredTime: occurred,
			HTTPCall: &HTTPCallDispatch{URL: "https://x/y"}},
		{Kind: ConnectorKindPublish, Tenant: "acme", OccurredTime: occurred,
			Publish: &PublishDispatch{ConnectorRef: "kafka-main"}},
	}
	for _, r := range good {
		if err := r.Validate(); err != nil {
			t.Fatalf("expected %s to validate, got %v", r.Kind, err)
		}
	}

	bad := []struct {
		name string
		req  *ConnectorDispatchRequest
		want string
	}{
		{"no tenant", &ConnectorDispatchRequest{Kind: ConnectorKindHTTPCall,
			HTTPCall: &HTTPCallDispatch{URL: "https://x"}}, "no tenant"},
		{"unknown kind", &ConnectorDispatchRequest{Kind: "sendEmail", Tenant: "acme"}, "unknown dispatch kind"},
		{"httpCall nil variant", &ConnectorDispatchRequest{Kind: ConnectorKindHTTPCall, Tenant: "acme"}, "no httpCall config"},
		{"httpCall wrong variant", &ConnectorDispatchRequest{Kind: ConnectorKindHTTPCall, Tenant: "acme",
			HTTPCall: &HTTPCallDispatch{URL: "https://x"}, Publish: &PublishDispatch{ConnectorRef: "c"}}, "must not carry a publish"},
		{"httpCall no url", &ConnectorDispatchRequest{Kind: ConnectorKindHTTPCall, Tenant: "acme",
			HTTPCall: &HTTPCallDispatch{}}, "no url"},
		{"publish nil variant", &ConnectorDispatchRequest{Kind: ConnectorKindPublish, Tenant: "acme"}, "no publish config"},
		{"publish wrong variant", &ConnectorDispatchRequest{Kind: ConnectorKindPublish, Tenant: "acme",
			Publish: &PublishDispatch{ConnectorRef: "c"}, HTTPCall: &HTTPCallDispatch{URL: "https://x"}}, "must not carry an httpCall"},
		{"publish no ref", &ConnectorDispatchRequest{Kind: ConnectorKindPublish, Tenant: "acme",
			Publish: &PublishDispatch{}}, "no connectorRef"},
	}
	for _, tc := range bad {
		err := tc.req.Validate()
		if err == nil {
			t.Fatalf("%s: expected an error", tc.name)
		}
		if !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("%s: error %q does not contain %q", tc.name, err.Error(), tc.want)
		}
	}
}
