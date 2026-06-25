// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"testing"
)

// AttributeScope.Valid accepts only the known scope vocabulary (ADR-012).
func TestAttributeScopeValid(t *testing.T) {
	valid := []AttributeScope{AttributeScopeClient, AttributeScopeServer, AttributeScopeShared}
	for _, s := range valid {
		if !s.Valid() {
			t.Errorf("expected scope %q to be valid", s)
		}
	}

	invalid := []AttributeScope{"", "client", "PUBLIC", "SHARED ", "OTHER"}
	for _, s := range invalid {
		if s.Valid() {
			t.Errorf("expected scope %q to be invalid", s)
		}
	}
}

// AttributeValueType.Valid accepts only the known value-type vocabulary.
func TestAttributeValueTypeValid(t *testing.T) {
	valid := []AttributeValueType{
		AttributeValueString, AttributeValueLong, AttributeValueDouble,
		AttributeValueBoolean, AttributeValueJson,
	}
	for _, vt := range valid {
		if !vt.Valid() {
			t.Errorf("expected value type %q to be valid", vt)
		}
	}

	invalid := []AttributeValueType{"", "string", "INT", "FLOAT", "OBJECT"}
	for _, vt := range invalid {
		if vt.Valid() {
			t.Errorf("expected value type %q to be invalid", vt)
		}
	}
}
