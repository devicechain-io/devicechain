// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/integrity"
	"github.com/devicechain-io/dc-user-management/identity"
)

type PrincipalStandingResolver struct{ standing identity.PrincipalStanding }

func (r *PrincipalStandingResolver) Principal() string { return r.standing.Principal }
func (r *PrincipalStandingResolver) Qualifies() bool   { return r.standing.Qualifies }

// PrincipalStanding answers only device-management's own service token.
func (r *SchemaResolver) PrincipalStanding(ctx context.Context, args struct {
	Principals  []string
	Authorities []string
}) ([]*PrincipalStandingResolver, error) {
	if err := auth.AuthorizeService(ctx, auth.TenantRead); err != nil {
		return nil, auth.ErrForbidden
	}
	claims, ok := auth.ClaimsFromContext(ctx)
	if !ok || claims.Subject != "device-management" || auth.IsAgentOrigin(claims) {
		return nil, auth.ErrForbidden
	}
	tenant, ok := core.TenantFromContext(ctx)
	if !ok || tenant == "" {
		return nil, core.ErrNoTenant
	}
	if len(args.Principals) < 1 || len(args.Principals) > 10 || len(args.Authorities) < 1 || len(args.Authorities) > 8 {
		return nil, integrity.NewRefusal(integrity.ClassInvalid, "principals must contain 1 to 10 entries and authorities 1 to 8 entries")
	}
	for _, principal := range args.Principals {
		if principal == "" {
			return nil, integrity.NewRefusal(integrity.ClassInvalid, "principals must be non-empty")
		}
	}
	authorities := make([]auth.Authority, len(args.Authorities))
	for i, authority := range args.Authorities {
		if authority == "" {
			return nil, integrity.NewRefusal(integrity.ClassInvalid, "authorities must be non-empty")
		}
		authorities[i] = auth.Authority(authority)
	}
	standing, err := r.getIdentityManager(ctx).PrincipalStanding(ctx, tenant, args.Principals, authorities)
	if err != nil {
		return nil, err
	}
	out := make([]*PrincipalStandingResolver, 0, len(standing))
	for _, entry := range standing {
		out = append(out, &PrincipalStandingResolver{standing: entry})
	}
	return out, nil
}
