// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package intentseam

import (
	"time"

	"github.com/devicechain-io/dc-microservice/intent"
)

// Test access to the seam's budgets and builders. Production code cannot reach any of it.

// NewWithBudgets is New with the given wait, per-statement timeouts (missing ones keep the
// platform's) and page byte budget.
func NewWithBudgets(busyWait time.Duration, timeouts map[string]time.Duration, pageBytes int) *Seam {
	s := New()
	s.busyWait = busyWait
	for op, d := range timeouts {
		s.timeouts[op] = d
	}
	if pageBytes > 0 {
		s.pageBytes = pageBytes
	}
	return s
}

// SetHeldHook installs a function that runs while a statement holds its slot.
func SetHeldHook(s *Seam, f func(op string)) { s.heldHook = f }

func lowered(tenant, source string) (*frozenTarget, error) {
	frag, args, err := lowerFrozen(tenant, source)
	if err != nil {
		return nil, err
	}
	return &frozenTarget{frag: frag, args: args}, nil
}

// DigestSQL is the digest statement for a selector source.
func DigestSQL(tenant, key, source string) (string, []any, error) {
	t, err := lowered(tenant, source)
	if err != nil {
		return "", nil, err
	}
	q, args := digestSQL(tenant, key, t, intent.MaxDevicesPerIntent)
	return q, args, nil
}

// PageSQL is the page statement for a selector source.
func PageSQL(tenant, key, source string, afterID int64, limit int) (string, []any, error) {
	t, err := lowered(tenant, source)
	if err != nil {
		return "", nil, err
	}
	q, args := pageSQL(tenant, key, t, afterID, limit)
	return q, args, nil
}

// IntersectSQL is the intersection statement for two selector sources.
func IntersectSQL(tenant, a, b string) (string, []any, error) {
	ta, err := lowered(tenant, a)
	if err != nil {
		return "", nil, err
	}
	tb, err := lowered(tenant, b)
	if err != nil {
		return "", nil, err
	}
	q, args := intersectSQL(tenant, ta, tb)
	return q, args, nil
}

// The group lookups resolveTarget runs, for the predicate check.
const GroupSQL, GroupVersionSQL = groupSQL, groupVersionSQL

// The stored vocabulary this package repeats from the model.
const DynamicMode, SharedScope, DeviceMemberType = dynamicMode, sharedScope, deviceMemberType

// SetMaxMembers lowers the member bound a digest refuses above, so the refusal is exercised on a
// small fixture; production holds it at intent.MaxDevicesPerIntent.
func SetMaxMembers(s *Seam, n int) { s.maxMembers = n }

// MaxMembers reports the bound.
func MaxMembers(s *Seam) int { return s.maxMembers }
