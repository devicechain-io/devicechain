// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"

	"github.com/devicechain-io/dc-ai-inference/model"
	"github.com/devicechain-io/dc-microservice/auth"
)

// The per-tenant AI-function assignment surface (ADR-065 S5c′): which model a tenant uses
// for each AI function. It is OPERATOR-facing — every resolver gates on ai:admin (a
// system-tier authority, so a tenant access token can never satisfy it) and runs on the
// same /admin/graphql plane as the provider and grant surfaces, in the system context.
//
// The assignment is set by the operator on the tenant's detail page, not by the tenant:
// which model a tenant's spend is directed at is a packaging decision, sold alongside its
// tier, not a tenant self-service toggle. The tenant token is therefore an explicit
// argument here (the admin plane holds no tenant of its own), exactly as the grant surface
// takes it.
//
// WHAT IS DELIBERATELY ABSENT: any resolver that ASKS which model a tenant resolves to.
// model.Api.ResolveModelForFunction answers that, but it refuses the system context by
// design (it reads tenant-scoped assignment rows through the isolation callback, which the
// system context disables) — so it cannot be reached from this plane, and that is correct.
// The operator console composes the answer from the facts these resolvers do expose (the
// tenant's assignments, its tier's grants and marked default, its additive grants), the
// same way the packaging matrix composes its warnings, rather than a system-context
// resolution path re-deriving entitlement with the isolation turned off.

// AiFunctions returns the platform's AI-function vocabulary, for the assignment picker.
// Read-gated like the rest of the admin surface. GA declares exactly one function.
func (r *AdminResolver) AiFunctions(ctx context.Context) ([]*AIFunctionResolver, error) {
	if err := auth.Authorize(ctx, auth.AIAdmin); err != nil {
		return nil, err
	}
	fns := model.AllFunctions()
	out := make([]*AIFunctionResolver, 0, len(fns))
	for i := range fns {
		out = append(out, &AIFunctionResolver{M: fns[i]})
	}
	return out, nil
}

// AiFunctionAssignments returns one tenant's stored per-function choices, each with the
// provider it names. Lists what the tenant CHOSE — including a choice it is no longer
// entitled to — because an operator who cannot see a stale choice cannot fix it.
func (r *AdminResolver) AiFunctionAssignments(ctx context.Context, args struct {
	Tenant string
}) ([]*AIFunctionAssignmentResolver, error) {
	if err := auth.Authorize(ctx, auth.AIAdmin); err != nil {
		return nil, err
	}
	assignments, err := apiFrom(ctx).ListFunctionAssignments(ctx, args.Tenant)
	if err != nil {
		return nil, err
	}
	out := make([]*AIFunctionAssignmentResolver, 0, len(assignments))
	for i := range assignments {
		out = append(out, &AIFunctionAssignmentResolver{M: assignments[i], C: ctx})
	}
	return out, nil
}

// SetAiFunctionModel assigns a tenant's model for one function. Idempotent in effect:
// re-asserting the same choice is a no-op, a different one replaces it.
//
// It does not check the menu — entitlement is applied at resolve time, so an assignment
// survives a temporary revoke (see model.Api.SetFunctionModel and the schema doc). The
// function token is validated against the vocabulary; an unknown provider or function is
// a legible error, not a silent no-op.
func (r *AdminResolver) SetAiFunctionModel(ctx context.Context, args struct {
	Tenant   string
	Function string
	Provider string
}) (bool, error) {
	if err := auth.Authorize(ctx, auth.AIAdmin); err != nil {
		return false, err
	}
	if err := apiFrom(ctx).SetFunctionModel(ctx, args.Tenant, args.Function, args.Provider); err != nil {
		return false, err
	}
	return true, nil
}

// ClearAiFunctionModel removes a tenant's choice for one function, reporting whether a row
// was removed. Idempotent. The tenant falls back to its tier's default afterwards (if the
// tier marked one and the tenant is still entitled to it).
func (r *AdminResolver) ClearAiFunctionModel(ctx context.Context, args struct {
	Tenant   string
	Function string
}) (bool, error) {
	if err := auth.Authorize(ctx, auth.AIAdmin); err != nil {
		return false, err
	}
	return apiFrom(ctx).ClearFunctionModel(ctx, args.Tenant, args.Function)
}
