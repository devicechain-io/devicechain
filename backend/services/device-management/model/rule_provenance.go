// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"encoding/json"
	"fmt"
	"sort"

	"gorm.io/datatypes"
)

// AuthoredVia identifies the door a draft rule was saved through. AGENT_PROPOSAL
// is reserved for the later rule-changeset slice and is deliberately not accepted.
type AuthoredVia string

func (AuthoredVia) ImplementsGraphQLType(name string) bool { return name == "AuthoredVia" }

func (v AuthoredVia) valid() bool {
	switch v {
	case "FORM", "CANVAS", "DESCRIBE", "PRODUCT_AS_CODE", "API", "UNKNOWN":
		return true
	}
	return false
}

func (v *AuthoredVia) UnmarshalGraphQL(input any) error {
	s, ok := input.(string)
	if !ok || !AuthoredVia(s).valid() {
		return fmt.Errorf("unknown authoredVia %v", input)
	}
	*v = AuthoredVia(s)
	return nil
}

// OptionalAuthoredVia preserves the update surface's three input states. Both
// absence and null mean API on a save; an otherwise empty absent request is a no-op.
type OptionalAuthoredVia struct {
	Set   bool
	Value *AuthoredVia
}

func (OptionalAuthoredVia) ImplementsGraphQLType(name string) bool { return name == "AuthoredVia" }

func (OptionalAuthoredVia) Nullable() {}

func (o *OptionalAuthoredVia) UnmarshalGraphQL(input any) error {
	o.Set = true
	o.Value = nil
	if input == nil {
		return nil
	}
	var value AuthoredVia
	if err := value.UnmarshalGraphQL(input); err != nil {
		return err
	}
	o.Value = &value
	return nil
}

func ruleSaveDoor(input *AuthoredVia) (AuthoredVia, error) {
	if input == nil {
		return "API", nil
	}
	if !input.valid() {
		return "", fmt.Errorf("unknown authoredVia %q", *input)
	}
	return *input, nil
}

// AuthoredViaValues reads nullable legacy columns and absent stored snapshot keys
// as UNKNOWN. Corrupt stored data fails loudly instead of claiming a known origin.
func AuthoredViaValues(raw datatypes.JSON) ([]AuthoredVia, error) {
	var values []AuthoredVia
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &values); err != nil {
			return nil, fmt.Errorf("invalid stored authoredVia: %w", err)
		}
	}
	if len(values) == 0 {
		return []AuthoredVia{"UNKNOWN"}, nil
	}
	for _, value := range values {
		if !value.valid() {
			return nil, fmt.Errorf("invalid stored authoredVia %q", value)
		}
	}
	return values, nil
}

// mergeAuthoredVia accumulates a canonical set. A later FORM/API save cannot erase
// DESCRIBE, or the fact that some of a legacy rule's history is UNKNOWN.
func mergeAuthoredVia(groups ...datatypes.JSON) (datatypes.JSON, error) {
	set := map[AuthoredVia]bool{}
	for _, group := range groups {
		values, err := AuthoredViaValues(group)
		if err != nil {
			return nil, err
		}
		for _, value := range values {
			set[value] = true
		}
	}
	values := make([]AuthoredVia, 0, len(set))
	for value := range set {
		values = append(values, value)
	}
	if len(values) == 0 {
		values = append(values, "UNKNOWN")
	}
	sort.Slice(values, func(i, j int) bool { return values[i] < values[j] })
	raw, err := json.Marshal(values)
	return datatypes.JSON(raw), err
}

func ruleDoorJSON(door AuthoredVia) datatypes.JSON {
	// Every accepted value is a fixed enum literal without JSON metacharacters.
	return datatypes.JSON(`["` + string(door) + `"]`)
}

// snapshotAuthoredVia uses precisely the frozen rules, including disabled rules;
// a second draft query could attribute different content during a concurrent edit.
func snapshotAuthoredVia(snapshot *ProfileSnapshot) (datatypes.JSON, error) {
	groups := make([]datatypes.JSON, 0, len(snapshot.Rules))
	for _, rule := range snapshot.Rules {
		groups = append(groups, rule.AuthoredVia)
	}
	return mergeAuthoredVia(groups...)
}
