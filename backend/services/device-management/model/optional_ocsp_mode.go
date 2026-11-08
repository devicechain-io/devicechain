// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import "fmt"

// OptionalOcspMode carries a CertificateOcspMode enum input field through the packer with its
// present/absent distinction intact (see OptionalRevocationMode).
type OptionalOcspMode struct {
	// Set is true only when the field was PRESENT in the request, whether null or a value.
	Set bool
	// Value is nil when the field was present and explicitly null.
	Value *string
}

func (OptionalOcspMode) ImplementsGraphQLType(name string) bool { return name == "CertificateOcspMode" }

// Nullable marks this as a type that can accept an explicit null. REQUIRED, as for every Optional*.
func (OptionalOcspMode) Nullable() {}

func (o *OptionalOcspMode) UnmarshalGraphQL(input any) error {
	o.Set = true
	if input == nil {
		o.Value = nil
		return nil
	}
	s, ok := input.(string)
	if !ok {
		return fmt.Errorf("expected a CertificateOcspMode, got %T", input)
	}
	o.Value = &s
	return nil
}
