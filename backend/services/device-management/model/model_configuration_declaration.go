// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"encoding/json"
	"fmt"
	"unicode/utf8"

	"github.com/devicechain-io/dc-microservice/core"
	"gorm.io/datatypes"
)

// Limits on a profile's configuration declaration. They are FIXED platform ceilings, not
// tenant settings: the declaration is frozen into every published version and later
// becomes the key set of a document delivered to devices, so an unbounded one is an
// unbounded document.
const (
	// MaxConfigurationKeys bounds how many device-visible keys one profile may declare.
	MaxConfigurationKeys = 256
	// MaxConfigurationDescriptionLen bounds a key's description, in characters.
	MaxConfigurationDescriptionLen = 256
)

// ConfigurationKey declares ONE shared attribute key as device-visible configuration.
//
// A profile version names the SHARED attribute keys a device of that profile is allowed
// to see. SHARED is not "the configuration scope" on its own: classification facets and
// alarm thresholds live there too and must never be pushed to a device wholesale, so
// visibility is an explicit, per-profile-version statement. Nothing declared means the
// device sees an empty document.
//
// Stored as part of the profile's draft (one nullable JSON column) and frozen into
// ProfileSnapshot.Configuration at publish. The json tags are the durable wire format.
type ConfigurationKey struct {
	// Key is the shared attribute key, in the attribute-key grammar (the token grammar).
	Key string `json:"key"`
	// ValueType is how the key's value is typed when the document is built; one of the
	// attribute value types (STRING, LONG, DOUBLE, BOOLEAN, JSON).
	ValueType string `json:"valueType"`
	// Description is an optional human-readable note for the authoring surface.
	Description string `json:"description,omitempty"`
}

// ValidateConfigurationDeclaration is the fail-closed check applied to a declaration
// before it is stored and again before it is frozen at publish. A nil or empty
// declaration is valid: it declares nothing.
//
// There is no registry of SHARED attribute definitions in the model — attributes are
// free-form (entity, scope, key) rows — so keys cannot be checked for existence; they
// are held to the attribute-key grammar instead, and to the ceilings above.
func ValidateConfigurationDeclaration(keys []ConfigurationKey) error {
	if len(keys) > MaxConfigurationKeys {
		return fmt.Errorf("a configuration declaration may name at most %d keys, got %d",
			MaxConfigurationKeys, len(keys))
	}
	seen := make(map[string]struct{}, len(keys))
	for i, k := range keys {
		if err := core.ValidateToken(k.Key); err != nil {
			return fmt.Errorf("configuration key #%d: %w", i+1, err)
		}
		if _, dup := seen[k.Key]; dup {
			return fmt.Errorf("configuration key %q is declared more than once", k.Key)
		}
		seen[k.Key] = struct{}{}
		if !AttributeValueType(k.ValueType).Valid() {
			return fmt.Errorf("configuration key %q has invalid value type %q", k.Key, k.ValueType)
		}
		if utf8.RuneCountInString(k.Description) > MaxConfigurationDescriptionLen {
			return fmt.Errorf("configuration key %q: description exceeds %d characters",
				k.Key, MaxConfigurationDescriptionLen)
		}
	}
	return nil
}

// ConfigurationKeys reads the profile's DRAFT configuration declaration. An empty result
// means nothing is declared.
func (p *DeviceProfile) ConfigurationKeys() ([]ConfigurationKey, error) {
	return decodeConfigurationDeclaration(p.ConfigurationDeclaration)
}

func encodeConfigurationDeclaration(keys []ConfigurationKey) (*datatypes.JSON, error) {
	if len(keys) == 0 {
		return nil, nil
	}
	if err := ValidateConfigurationDeclaration(keys); err != nil {
		return nil, err
	}
	raw, err := json.Marshal(keys)
	if err != nil {
		return nil, err
	}
	encoded := datatypes.JSON(raw)
	return &encoded, nil
}

func decodeConfigurationDeclaration(raw *datatypes.JSON) ([]ConfigurationKey, error) {
	if raw == nil || len(*raw) == 0 || string(*raw) == "null" {
		return nil, nil
	}
	var keys []ConfigurationKey
	if err := json.Unmarshal(*raw, &keys); err != nil {
		return nil, fmt.Errorf("unable to parse device profile configuration declaration: %w", err)
	}
	return keys, nil
}
