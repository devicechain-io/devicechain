// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package ledger

import "time"

// Admission budgets. These are Go constants for the first release of fleet operations: they are
// fixed values, not configuration, so a pod of an older release still starts after the upgrade
// hook has run. Tier ceilings arrive later and may only lower them. A tenant's counts are taken
// under the tenant admission lock and then the instance one, so two replicas cannot both admit
// the last slot.
const (
	// MaxLinesPerOperation bounds one operation's targets. More is refused, never truncated.
	MaxLinesPerOperation = 10_000
	// MaxRequestBytes bounds the request body the planner will normalise.
	MaxRequestBytes = 1 << 20
	// MaxIntentBytes bounds the canonical intent stored on the header.
	MaxIntentBytes = 16 << 10

	TenantDispatchingOperations   = 2
	InstanceDispatchingOperations = 20
	TenantUnstartedOperations     = 20
	InstanceUnstartedOperations   = 200
	TenantUnpostedLines           = 20_000
	InstanceUnpostedLines         = 200_000
	TenantEvidenceOpenLines       = 100_000
	InstanceEvidenceOpenLines     = 1_000_000
	TenantRetainedLines           = 500_000
	InstanceRetainedLines         = 5_000_000
	TenantCreatesPerHour          = 20
	TenantCreatesPerDay           = 100

	TenantPostsPerSecond   = 50
	InstancePostsPerSecond = 500

	// ReadyExpiry is how long a READY operation that was never started waits before it is aborted.
	ReadyExpiry = 7 * 24 * time.Hour
	// PlanningAbandonedAfter is how long a header may sit in PLANNING before it is abandoned.
	PlanningAbandonedAfter = 10 * time.Minute

	// Bounds on the safety contract a creator may ask for.
	DefaultMaxPostsPerSecond     = 10
	MinSettleDeadlineSeconds     = 60
	MaxSettleDeadlineSeconds     = 7 * 24 * 3600
	DefaultSettleDeadlineSeconds = 24 * 3600
)
