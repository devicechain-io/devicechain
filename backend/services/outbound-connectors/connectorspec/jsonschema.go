// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package connectorspec

import (
	"fmt"
	"reflect"

	"github.com/devicechain-io/dc-microservice/wirecontract"
)

// JSONSchema describes a supported connector's config object, never its secrets.
// ValidateConfig remains the authority for destination and cross-field semantics.
func JSONSchema(connType string) (map[string]any, error) {
	types := map[string]reflect.Type{
		"mqtt":              reflect.TypeFor[mqttConfig](),
		"kafka":             reflect.TypeFor[kafkaConfig](),
		"aws_sns":           reflect.TypeFor[snsConfig](),
		"aws_sqs":           reflect.TypeFor[sqsConfig](),
		LoRaWANDownlinkType: reflect.TypeFor[lorawanConfig](),
	}
	t, ok := types[connType]
	if !ok || !Supported(connType) {
		return nil, fmt.Errorf("%w: %q", ErrUnsupportedType, connType)
	}
	s, err := wirecontract.Generate(t, nil)
	if err != nil {
		return nil, err
	}
	if connType == LoRaWANDownlinkType {
		// applicationId is required only by the tts_v3 dialect.
		s["required"] = []string{"networkServer", "baseUrl", "integration"}
	}
	s["title"] = "DeviceChain " + connType + " connector config"
	s["description"] = "Authoring structure only. Validate the connector before publishing for destination and cross-field checks. Credentials belong in the connector secret, never this config."
	return s, nil
}
