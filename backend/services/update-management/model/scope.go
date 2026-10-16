// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"

	"github.com/devicechain-io/dc-microservice/auth"
)

// A member limited to part of the tenant (ADR-081) reaches this service through one door:
// reading update assignments, which UpdateAssignment.ScopedBy limits to the assignments
// stamped with an anchor in its scope. Artifacts, signers, assigning and cancelling refuse
// it at the resolver.

// joinedReadContext is the context an assignment view's JOINED rows are read under: the
// attempt, the artifact and the pinned signature of assignments that have already been read
// — and therefore already passed the scope predicate. For an unscoped caller it is ctx. For a
// scoped member it is ctx with the claims removed and the tenant kept, because those rows
// carry no anchors of their own: they are visible exactly when their assignment is, and the
// assignment read has decided that.
//
// 🔴 IT IS FOR THOSE JOINS AND NOTHING ELSE. Used for a read keyed on anything but rows the
// scoped read returned, it would hand a member the tenant's rows. Derive it at the join;
// never hand it onward.
func joinedReadContext(ctx context.Context) context.Context {
	if _, scoped := auth.ScopeFromContext(ctx); !scoped {
		return ctx
	}
	return auth.WithClaims(ctx, nil)
}
