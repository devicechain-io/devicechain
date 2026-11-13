// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package identity

import (
	"context"
	"errors"
	"slices"

	"github.com/devicechain-io/dc-microservice/auth"
	"gorm.io/gorm"
)

// PrincipalStanding is a person's current membership and authority standing in a tenant.
type PrincipalStanding struct {
	Principal string
	Qualifies bool
}

// PrincipalStanding reads current records, returning each distinct principal in first-seen
// order. A missing identity does not qualify; a store failure aborts the entire answer.
func (m *Manager) PrincipalStanding(ctx context.Context, tenant string, principals []string, authorities []auth.Authority) ([]PrincipalStanding, error) {
	out := make([]PrincipalStanding, 0, len(principals))
	seen := make(map[string]bool, len(principals))
	for _, principal := range principals {
		if seen[principal] {
			continue
		}
		seen[principal] = true
		standing := PrincipalStanding{Principal: principal}
		id, err := m.iam.IdentityByEmail(ctx, principal)
		switch {
		case errors.Is(err, gorm.ErrRecordNotFound):
		case err != nil:
			return nil, err
		case id.Enabled:
			mem := findMembership(id.Memberships, tenant)
			if mem != nil && mem.Enabled && !mem.IsScoped() {
				held := mem.TenantAuthorities()
				standing.Qualifies = true
				if !slices.Contains(held, string(auth.AuthorityAll)) {
					for _, authority := range authorities {
						if !slices.Contains(held, string(authority)) {
							standing.Qualifies = false
							break
						}
					}
				}
			}
		}
		out = append(out, standing)
	}
	return out, nil
}
