// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	_ "embed"

	"github.com/devicechain-io/dc-ai-inference/inference"
)

//go:embed admin_schema.graphql
var AdminSchemaContent string

// AdminResolver is the root resolver for the ai-inference ADMIN plane: the
// instance-scoped, operator-managed AIProvider CRUD, the tier↔provider grants,
// and the operator smoke test. It is served on its own /admin/graphql endpoint,
// which validates IDENTITY-tier tokens and runs in the system context across
// tenants (ADR-033's shape, applied here by ADR-065).
//
// The endpoint is the boundary that matters. Before ADR-065 these fields hung off
// the data-plane root, so the only credential that could reach them was a tenant
// access token — which meant an operator had to enter a tenant to administer
// instance config, and any holder of the seeded `tenant-admin` role ("*") passed
// the ai:admin check. Here, an access token cannot authenticate at all. The
// per-resolver ai:admin checks below are defence in depth, not the only gate.
type AdminResolver struct {
	// Inference runs the operator smoke test (testAiProvider) through the same
	// fail-closed cascade the tenant path uses. Nil disables it (an error), so a
	// partial deployment cannot serve an unwired inference path.
	Inference *inference.Resolver
	// Metrics counts inference outcomes and token spend. Nil-safe. The smoke test is
	// counted like any other call: it spends the same real budget against the same
	// key, so excluding it would understate what the key actually cost.
	Metrics *Metrics
}
