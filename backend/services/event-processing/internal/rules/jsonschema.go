// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package rules

import (
	"reflect"

	"github.com/devicechain-io/dc-microservice/wirecontract"
)

// JSONSchema describes the rule authoring structure. Compile remains the authority
// for type-dependent fields, durations, expressions, and action semantics.
func JSONSchema() (map[string]any, error) {
	s, err := wirecontract.Generate(reflect.TypeFor[Rule](), map[reflect.Type]map[string]any{
		reflect.TypeFor[Duration](): {"type": "string", "description": "Go duration string; validated by the rule compiler."},
		reflect.TypeFor[RuleType](): {"type": "string", "enum": []RuleType{TypeThreshold, TypeDeltaRate, TypeRepeating, TypeDuration, TypeAbsence, TypeAggregate, TypeCorrelation, TypeConnectivity}},
	})
	if err != nil {
		return nil, err
	}
	// A leaf is not required for absence, connectivity, or match-every-event rules.
	s["required"] = []string{"name", "type"}
	s["title"] = "DeviceChain detection rule"
	s["description"] = "Authoring structure only. Submit to validateDetectionRules before publishing for semantic validation."
	s["properties"].(map[string]any)["actions"].(map[string]any)["maxItems"] = MaxActionsPerRule
	return s, nil
}
