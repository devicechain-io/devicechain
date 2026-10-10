// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package graphql is the update-management data plane, which is explicitly
// UNIMPLEMENTED in this build.
package graphql

import (
	"context"
	_ "embed"
)

//go:embed schema.graphql
var SchemaContent string

// NotImplementedCode is the GraphQL error-extension code every operation on this plane
// answers with until the update API ships.
const NotImplementedCode = "NOT_IMPLEMENTED"

// ErrNotImplemented is what every resolver on this plane returns.
//
// 🔴 UNIMPLEMENTED MUST FAIL LOUDLY. A resolver that answered an empty string, an empty
// list or a zero-valued object would be indistinguishable from a working one at every
// call site, and the gap would surface as a client believing it had no artifacts rather
// than as a missing feature. So the plane answers every operation with an error carrying
// a machine-readable code, and the one field it declares is non-null, so the error
// propagates to `data: null` instead of leaving a plausible null in its place.
var ErrNotImplemented error = &notImplementedError{}

type notImplementedError struct{}

func (*notImplementedError) Error() string {
	return "update-management: the update API is not implemented in this build"
}

// Extensions gives the error its code, so a client can tell "this build has no update
// API" from every other failure without parsing the message.
func (*notImplementedError) Extensions() map[string]any {
	return map[string]any{"code": NotImplementedCode}
}

// SchemaResolver is the root resolver of the update-management data plane.
type SchemaResolver struct{}

// UpdateManagementInfo is the plane's only query. It exists because the GraphQL runtime
// requires a non-empty Query type, and it never answers with data.
func (r *SchemaResolver) UpdateManagementInfo(context.Context) (string, error) {
	return "", ErrNotImplemented
}
