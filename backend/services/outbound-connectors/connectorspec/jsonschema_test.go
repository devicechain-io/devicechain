// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package connectorspec

import (
	"errors"
	"reflect"
	"testing"
)

func TestJSONSchemaSupportedConfigs(t *testing.T) {
	for kind := range builders {
		s, err := JSONSchema(kind)
		if err != nil {
			t.Fatalf("%s: %v", kind, err)
		}
		if s["additionalProperties"] != false {
			t.Fatalf("%s allows typos", kind)
		}
		p := s["properties"].(map[string]any)
		for _, forbidden := range []string{"password", "secretAccessKey", "apiKey"} {
			if _, ok := p[forbidden]; ok {
				t.Fatalf("%s exposes credential %s", kind, forbidden)
			}
		}
	}
	for _, kind := range []string{"gcp_pubsub", "unknown", ""} {
		if _, err := JSONSchema(kind); !errors.Is(err, ErrUnsupportedType) {
			t.Fatalf("%s: %v", kind, err)
		}
	}
	s, _ := JSONSchema("aws_sns")
	p := s["properties"].(map[string]any)
	if p["accessKeyId"].(map[string]any)["type"] != "string" || p["region"] == nil || p["topicArn"] == nil {
		t.Fatal("embedded AWS config was not flattened", p)
	}
	if !reflect.DeepEqual(s["required"], []string{"region", "accessKeyId", "topicArn"}) {
		t.Fatal(s["required"])
	}
	s, _ = JSONSchema(LoRaWANDownlinkType)
	if !reflect.DeepEqual(s["required"], []string{"networkServer", "baseUrl", "integration"}) {
		t.Fatal("chirpstack must not require applicationId")
	}
	s, _ = JSONSchema("kafka")
	sasl := s["properties"].(map[string]any)["sasl"].(map[string]any)
	if sasl["additionalProperties"] != false {
		t.Fatal("nested SASL config allows typos")
	}
}
