// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"fmt"
	"time"

	dcgraphql "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-microservice/rdb"
	"gorm.io/datatypes"
)

// AlarmSuppressionKind is the transport enum; storage uses the same vocabulary.
type AlarmSuppressionKind string

func (AlarmSuppressionKind) ImplementsGraphQLType(name string) bool {
	return name == "AlarmSuppressionKind"
}
func (k *AlarmSuppressionKind) UnmarshalGraphQL(input any) error {
	s, ok := input.(string)
	if !ok || (s != SuppressionOneShot && s != SuppressionRecurring) {
		return suppressionInvalid("unknown kind %v", input)
	}
	*k = AlarmSuppressionKind(s)
	return nil
}

// OptionalAlarmSuppressionKind preserves explicit null on a required enum.
type OptionalAlarmSuppressionKind struct {
	Set   bool
	Value *AlarmSuppressionKind
}

func (OptionalAlarmSuppressionKind) ImplementsGraphQLType(name string) bool {
	return name == "AlarmSuppressionKind"
}
func (OptionalAlarmSuppressionKind) Nullable() {}
func (o *OptionalAlarmSuppressionKind) UnmarshalGraphQL(input any) error {
	o.Set = true
	o.Value = nil
	if input == nil {
		return nil
	}
	var k AlarmSuppressionKind
	if err := k.UnmarshalGraphQL(input); err != nil {
		return err
	}
	o.Value = &k
	return nil
}

// AlarmSuppressionTargetInput has no server-owned fields.
type AlarmSuppressionTargetInput struct {
	Type  string
	Token *string
}

func (t AlarmSuppressionTargetInput) Target() AlarmSuppressionTarget {
	out := AlarmSuppressionTarget{Type: t.Type}
	if t.Token != nil {
		out.Token = *t.Token
	}
	return out
}

// OptionalAlarmSuppressionTargets follows core's OptionalStringList packer,
// including singleton list coercion. Core has no optional list-of-input-objects
// type. This wrapper validates every entry rather than JSON-decoding a map and
// silently dropping unknown fields. Null is represented and refused by the model.
type OptionalAlarmSuppressionTargets struct {
	Set   bool
	Value []AlarmSuppressionTarget
}

func (OptionalAlarmSuppressionTargets) ImplementsGraphQLType(name string) bool {
	return name == "[AlarmSuppressionTargetInput!]"
}
func (OptionalAlarmSuppressionTargets) Nullable() {}
func (o *OptionalAlarmSuppressionTargets) UnmarshalGraphQL(input any) error {
	o.Set = true
	o.Value = nil
	if input == nil {
		return nil
	}
	entries, ok := input.([]any)
	if !ok {
		entries = []any{input}
	}
	out := make([]AlarmSuppressionTarget, 0, len(entries))
	for i, entry := range entries {
		m, ok := entry.(map[string]any)
		if !ok {
			return suppressionInvalid("target %d must be an input object", i)
		}
		for key := range m {
			if key != "type" && key != "token" {
				return suppressionInvalid("unknown target field %q", key)
			}
		}
		typ, ok := m["type"].(string)
		if !ok {
			return suppressionInvalid("target %d requires type", i)
		}
		target := AlarmSuppressionTarget{Type: typ}
		if value := m["token"]; value != nil {
			token, ok := value.(string)
			if !ok {
				return suppressionInvalid("target %d token must be a string", i)
			}
			target.Token = token
		}
		out = append(out, target)
	}
	o.Value = out
	return nil
}

// ParseAlarmSuppressionTime rejects malformed instants as INVALID_VALUE.
func ParseAlarmSuppressionTime(field string, value *string) (*time.Time, error) {
	if value == nil {
		return nil, nil
	}
	parsed, err := time.Parse(time.RFC3339, *value)
	if err != nil {
		return nil, suppressionInvalid("%s must be an RFC3339 timestamp", field)
	}
	parsed = parsed.UTC()
	return &parsed, nil
}

func (r *AlarmSuppressionUpdateRequest) apply(w *AlarmSuppressionWindow) error {
	w.Name = r.Name.ApplyToNullString(w.Name)
	w.Description = r.Description.ApplyToNullString(w.Description)
	if r.Metadata.Set {
		metadata, err := rdb.JSONInputOf("metadata", r.Metadata.Value)
		if err != nil {
			return fmt.Errorf("%w: %v", ErrInvalidAlarmSuppression, err)
		}
		w.Metadata = metadata
	}
	if r.Reason.Set {
		if r.Reason.Value == nil {
			return suppressionInvalid("reason cannot be null")
		}
		w.Reason = *r.Reason.Value
	}
	if r.Targets.Set {
		if r.Targets.Value == nil {
			return suppressionInvalid("targets cannot be null")
		}
		w.Targets = append([]AlarmSuppressionTarget{}, r.Targets.Value...)
	}
	w.AlarmKeys = r.AlarmKeys.ApplyTo(w.AlarmKeys)
	w.BypassSeverity = r.BypassSeverity.ApplyToNullString(w.BypassSeverity)
	if r.Kind.Set {
		if r.Kind.Value == nil {
			return suppressionInvalid("kind cannot be null")
		}
		w.Kind = string(*r.Kind.Value)
	}
	for _, f := range []struct {
		name  string
		input dcgraphql.OptionalString
		dest  **time.Time
	}{
		{"startTime", r.StartTime, &w.StartTime}, {"endTime", r.EndTime, &w.EndTime},
		{"validFrom", r.ValidFrom, &w.ValidFrom}, {"validUntil", r.ValidUntil, &w.ValidUntil},
	} {
		if f.input.Set {
			parsed, err := ParseAlarmSuppressionTime(f.name, f.input.Value)
			if err != nil {
				return err
			}
			*f.dest = parsed
		}
	}
	w.Timezone = r.Timezone.ApplyToNullString(w.Timezone)
	if r.Slots.Set {
		w.Slots = nil
		if r.Slots.Value != nil {
			w.Slots = datatypes.JSON(*r.Slots.Value)
		}
	}
	return nil
}
