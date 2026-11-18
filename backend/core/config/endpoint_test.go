// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package config

import "testing"

func TestGraphQLURLGolden(t *testing.T) {
	for _, tc := range []struct {
		host string
		port uint32
		want string
	}{
		{"peer", 8080, "http://peer:8080/graphql"},
		{"::1", 80, "http://::1:80/graphql"},
		{"", 0, "http://:0/graphql"},
	} {
		if got := (ServiceEndpoint{Hostname: tc.host, Port: tc.port}).GraphQLURL(); got != tc.want {
			t.Fatalf("got %q, want %q", got, tc.want)
		}
	}
	for _, e := range []ServiceEndpoint{{Hostname: "peer"}, {Port: 80}, {}} {
		if e.Configured() {
			t.Fatalf("partial endpoint considered configured: %+v", e)
		}
	}
	if !(ServiceEndpoint{Hostname: "peer", Port: 80}).Configured() {
		t.Fatal("complete endpoint considered unconfigured")
	}
	um := UserManagementConfiguration{Hostname: "um", Port: 8080, InternalPort: 8082}
	if um.Endpoint().GraphQLURL() != "http://um:8080/graphql" || um.InternalAddr() != "um:8082" {
		t.Fatal("public/internal port changed")
	}
	for _, e := range []ServiceEndpoint{
		um.Endpoint(), (DeviceManagementConfiguration{}).Endpoint(), (EventProcessingConfiguration{}).Endpoint(),
		(CommandDeliveryConfiguration{}).Endpoint(), (DeviceStateConfiguration{}).Endpoint(), (AiInferenceConfiguration{}).Endpoint(),
	} {
		if e.Key == "" {
			t.Fatal("missing config key")
		}
	}
}
