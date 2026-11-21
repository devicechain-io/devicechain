// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"database/sql"
	"testing"

	"github.com/devicechain-io/dc-command-delivery/model"
	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/stretchr/testify/require"
)

func TestDetectionCauseDetailsUsesLegacyVisibility(t *testing.T) {
	for _, tc := range []struct {
		name, kind   string
		position     sql.NullBool
		authorities  []string
		scoped, want bool
	}{
		{"alarm reader", "ALARM", sql.NullBool{Valid: true}, []string{"device:read", "alarm:read"}, false, true},
		{"alarm missing device", "ALARM", sql.NullBool{Valid: true}, []string{"alarm:read"}, false, false},
		{"alarm missing alarm", "ALARM", sql.NullBool{Valid: true}, []string{"device:read"}, false, false},
		{"rule reader", "RULE", sql.NullBool{Valid: true}, []string{"device:read"}, false, true},
		{"rule missing device", "RULE", sql.NullBool{Valid: true}, []string{"command:read"}, false, false},
		{"position missing location", "RULE", sql.NullBool{Valid: true, Bool: true}, []string{"device:read"}, false, false},
		{"position reader", "RULE", sql.NullBool{Valid: true, Bool: true}, []string{"device:read", "location:read"}, false, true},
		{"unknown position missing location", "RULE", sql.NullBool{}, []string{"device:read"}, false, false},
		{"unknown position reader", "RULE", sql.NullBool{}, []string{"device:read", "location:read"}, false, true},
		{"scoped", "ALARM", sql.NullBool{Valid: true}, []string{"*"}, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			claims := &auth.Claims{TokenType: auth.TokenTypeAccess, Tenant: "A", Username: "reader", Authorities: tc.authorities}
			if tc.scoped {
				claims.ScopeAnchors = []string{"customer:c"}
			}
			ctx := auth.WithClaims(context.Background(), claims)
			r := &CommandResolver{M: model.Command{CauseKind: sql.NullString{Valid: true, String: tc.kind}, CausePositionDerived: tc.position, CauseRuleId: sql.NullString{Valid: true, String: "A/p@1/r"}, DeviceIncarnation: sql.NullInt64{Valid: true, Int64: 17}}}
			legacy := r.Cause(ctx)
			details := r.CauseDetails(ctx)
			require.Equal(t, tc.want, legacy != nil)
			require.Equal(t, tc.want, details != nil)
			require.Nil(t, r.Proposal(ctx))
			if details != nil {
				require.Equal(t, tc.kind, details.Kind())
				require.Nil(t, details.ProposalId())
				require.Equal(t, legacy, details.Detection())
			}
		})
	}
}

func TestUnknownAndCorruptProposalLinksAreWithheld(t *testing.T) {
	ctx := auth.WithClaims(context.Background(), &auth.Claims{TokenType: auth.TokenTypeAccess, Tenant: "A", Username: "reader", Authorities: []string{"*"}})
	for _, kind := range []sql.NullString{{}, {Valid: true, String: "UNKNOWN"}, {Valid: true, String: "PROPOSAL"}} {
		r := &CommandResolver{M: model.Command{CauseKind: kind}}
		require.Nil(t, r.Cause(ctx))
		require.Nil(t, r.CauseDetails(ctx))
		require.Nil(t, r.Proposal(ctx))
	}
}
