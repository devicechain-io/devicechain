// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"fmt"
	"reflect"
	"strconv"
	"strings"

	"github.com/devicechain-io/dc-microservice/eventtime"
)

// requireKeys requires at least one decoder-compatible spelling of each key.
// JSON Schema's required keyword alone would reject encoding/json's casing aliases.
func (b *schemaBuilder) requireKeys(def string, keys ...string) {
	n := b.defs[def].(map[string]any)
	n["type"] = "object"
	var all []any
	for _, key := range keys {
		all = append(all, map[string]any{"not": map[string]any{
			"propertyNames": map[string]any{"not": map[string]any{"pattern": foldedKeyPattern(key)}},
		}})
	}
	n["allOf"] = all
}

func (b *schemaBuilder) field(def, name string) map[string]any {
	return b.defs[def].(map[string]any)["properties"].(map[string]any)[name].(map[string]any)
}

// staticValues overlays deterministic single-field startup rules. Cross-field,
// driver-owned and environment-dependent checks still belong to the loader.
func (b *schemaBuilder) staticValues() {
	b.requireKeys("InstanceConfiguration", "infrastructure")
	b.requireKeys("InfrastructureConfiguration", "nats", "userManagement")
	for _, def := range []string{"NatsConfiguration", "UserManagementConfiguration"} {
		b.requireKeys(def, "hostname", "port")
		host := b.field(def, "hostname")
		host["type"], host["minLength"] = "string", 1
		port := b.field(def, "port")
		port["type"], port["minimum"] = "integer", 1
	}
	for _, def := range []string{"NatsConfiguration", "UserManagementConfiguration", "DeviceManagementConfiguration", "EventProcessingConfiguration", "CommandDeliveryConfiguration", "DeviceStateConfiguration", "AiInferenceConfiguration"} {
		b.field(def, "port")["maximum"] = 65535
	}
	b.field("UserManagementConfiguration", "internalPort")["maximum"] = 65535
	enum := func(def, name string, values ...string) {
		choices := []any{nil, ""} // null and empty strings take startup defaults.
		for _, value := range values {
			choices = append(choices, value)
		}
		b.field(def, name)["enum"] = choices
	}
	enum("LoggingConfiguration", "level", LogLevelNames()...)
	enum("TracingConfiguration", "protocol", tracingProtocols...)
	enum("MessagingConfiguration", "wireFormat", eventtime.WireLegacy.String(), eventtime.WireCompact.String())
	enum("TokenAudienceConfiguration", "enforcement", string(TokenAudienceEnforce))
	enum("TokenAudienceConfiguration", "unscopedClientCredentials", string(TokenAudienceObserve))
	for _, name := range []string{"drainSeconds", "terminationGracePeriodSeconds"} {
		b.field("ShutdownConfiguration", name)["minimum"] = 0
	}
	ratio := b.field("TracingConfiguration", "samplingRatio")
	ratio["minimum"], ratio["maximum"] = 0, 1
	linger := b.field("EnvelopesConfiguration", "inboundLingerMillis")
	linger["minimum"], linger["maximum"] = 0, MaxInboundLingerMillis
	fetch := b.field("NatsFetchConfiguration", "batch")
	fetch["minimum"], fetch["maximum"] = 0, MaxFetchBatch
	b.field("NatsFetchConfiguration", "aheadHoldBudgetMillis")["minimum"] = 0
	b.field("NatsConfiguration", "streamReplicas")["enum"] = []any{nil, 0, 1, 3, StreamMaxReplicas}
	rate := b.field("NatsConfiguration", "streamDesignRate")
	rate["minimum"], rate["maximum"] = 0, MaxStreamDesignRate
	window := b.field("NatsConfiguration", "streamRunwayMinutes")
	window["anyOf"] = []any{map[string]any{"enum": []any{nil, 0}}, map[string]any{"minimum": MinStreamRunwayMinutes, "maximum": MaxStreamRunwayMinutes}}
}

// decimalThrough matches decimal strings in 0..max, including leading zeroes.
// Each alternative first differs from max at a smaller digit, or equals max.
func decimalThrough(max uint64) string {
	limit := strconv.FormatUint(max, 10)
	parts := []string{limit}
	if len(limit) > 1 {
		parts = append(parts, fmt.Sprintf("[0-9]{1,%d}", len(limit)-1))
	}
	for i, digit := range []byte(limit) {
		if digit == '0' {
			continue
		}
		parts = append(parts, fmt.Sprintf("%s[0-%c][0-9]{%d}", limit[:i], digit-1, len(limit)-i-1))
	}
	return "0*(" + strings.Join(parts, "|") + ")"
}

func integerKeyPattern(t reflect.Type) string {
	if t.Kind() >= reflect.Int && t.Kind() <= reflect.Int64 {
		negative := uint64(1) << (t.Bits() - 1)
		return "^(\\+?" + decimalThrough(negative-1) + "|-" + decimalThrough(negative) + ")$"
	}
	return "^" + decimalThrough(^uint64(0)>>(64-t.Bits())) + "$"
}
