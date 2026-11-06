// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import "fmt"

// OptionalRevocationMode carries a CertificateRevocationMode enum input field through the packer
// with its present/absent distinction intact, the way core's OptionalString carries a String. It
// lives here because the enum belongs to this service, and ImplementsGraphQLType is handed that
// name verbatim (see OptionalLocationDeclaration).
type OptionalRevocationMode struct {
	// Set is true only when the field was PRESENT in the request, whether null or a value.
	Set bool
	// Value is nil when the field was present and explicitly null.
	Value *string
}

func (OptionalRevocationMode) ImplementsGraphQLType(name string) bool {
	return name == "CertificateRevocationMode"
}

// Nullable marks this as a type that can accept an explicit null. REQUIRED, as for every Optional*.
func (OptionalRevocationMode) Nullable() {}

func (o *OptionalRevocationMode) UnmarshalGraphQL(input any) error {
	o.Set = true
	if input == nil {
		o.Value = nil
		return nil
	}
	s, ok := input.(string)
	if !ok {
		return fmt.Errorf("expected a CertificateRevocationMode, got %T", input)
	}
	o.Value = &s
	return nil
}
