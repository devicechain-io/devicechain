// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package test

import "testing"

// Every remaining raw caller is an injected seam or the per-window backfill lock.
var sanctionedAdvisoryTryLockCallers = map[singletonLockSite]string{
	{"backend/services/command-delivery/model/api.go", "Api.TrySweepLock"}:                            "API seam faked by processor tests",
	{"backend/services/command-delivery/model/api.go", "Api.TryReconcileLock"}:                        "API seam faked by processor tests",
	{"backend/services/command-delivery/model/api_stranded.go", "Api.TryStrandedLock"}:                "API seam faked by processor tests",
	{"backend/services/event-management/processor/backfill/tail.go", "TailSweeper.sweep"}:             "per-window lock inside the loop, not a whole pass",
	{"backend/services/event-management/processor/usage_rollup_sweep.go", "UsageRollupSweep.RunOnce"}: "injected session-lock seam",
	{"backend/services/user-management/purge/coordinator.go", "NewCoordinator"}:                       "injected coordinator lock seam",
}

func TestAdvisoryTryLockCallersAreSanctioned(t *testing.T) {
	sites, err := singletonLockSitesUnder(workspaceRoot(t))
	if err != nil {
		t.Fatal(err)
	}
	for _, issue := range singletonLockLedgerErrors(sites, sanctionedAdvisoryTryLockCallers) {
		t.Error(issue)
	}
}
