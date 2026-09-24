// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package governance

import "fmt"

// CodeUnknownTenant is the GraphQL extensions.code user-management sets when
// tenantGovernance is asked about a tenant that does not exist.
const CodeUnknownTenant = "UNKNOWN_TENANT"

// UnknownTenantError is what user-management returns from tenantGovernance for a tenant
// that does not exist; its extension code is how a caller tells "no such tenant" from
// "could not ask". It lives here, beside the reader that classifies on it, so the code
// the server sets and the code the client matches are one constant.
type UnknownTenantError struct{ Tenant string }

func (e *UnknownTenantError) Error() string {
	return fmt.Sprintf("unknown tenant %q", e.Tenant)
}

// Extensions is read by the GraphQL server and served as the error's extensions.
func (e *UnknownTenantError) Extensions() map[string]any {
	return map[string]any{"code": CodeUnknownTenant}
}
