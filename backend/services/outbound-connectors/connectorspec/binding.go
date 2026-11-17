// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package connectorspec

import (
	"encoding/json"
	"slices"

	"github.com/devicechain-io/dc-microservice/credbinding"
)

// Binding returns the credential recipient and presentation. Unsupported or invalid configurations are opaque.
func Binding(kind string, config []byte) credbinding.Binding {
	builder, ok := builders[kind]
	if !ok {
		return credbinding.Opaque(kind, config)
	}
	if _, err := builder.parse(config); err != nil {
		return credbinding.Opaque(kind, config)
	}
	if builder.binding == nil {
		return credbinding.Opaque(kind, config)
	}
	return builder.binding(config)
}
func configBinding(kind string, raw []byte, value any, fields map[string]bool) credbinding.Binding {
	if json.Unmarshal(raw, value) != nil {
		return credbinding.Opaque(kind, raw)
	}
	return credbinding.Fields(credbinding.New(kind), value, fields)
}

func canonicalSet(values []string) []string {
	values = slices.Clone(values)
	slices.Sort(values)
	return slices.Compact(values)
}
