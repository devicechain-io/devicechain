// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package test

import (
	"path/filepath"
	"testing"
)

// sanctionedSystemContexts is every production call to core.WithSystemContext in the
// workspace, keyed by file and holding the enclosing function names.
//
// 🔴 WHY THIS LIST EXISTS. core/core/system.go says of WithSystemContext: "this is the
// one sanctioned bypass of the otherwise un-skippable tenant isolation (ADR-015) ...
// Every call site is a security review point." The first half was enforced — the
// callbacks really do skip both the predicate and the fail-closed check. The second
// half was a policy with no instrument behind it. Nothing enumerated the call sites, so
// a new bypass reached main looking like ordinary context plumbing, and the promised
// review happened only when a reviewer already knew to watch for this one identifier.
// A guard that makes the sentence true costs one line per bypass.
//
// 🔴 IT IS ENFORCED IN BOTH DIRECTIONS, which is what keeps it from becoming the usual
// rotting suppression file. A site found but not listed fails, because that is a new
// bypass of tenant isolation and it is supposed to be hard to add. A site listed but
// not found fails too, because the entry is stale and the next reader takes this list
// as an accurate account of where isolation is off.
//
// 🔴 IT IS NOT A LIST OF DEBT. Every entry here is believed CORRECT. That is the
// difference between this and knownUnpacedReadLoops next door, and it changes how a
// failure should be read: finding a new site is not "someone added a known-bad thing",
// it is "someone turned tenant isolation off somewhere, and this is the review". The
// justification belongs at the call site, in a comment a reader meets when they open
// the file — not here, where thirty-two paragraphs would be skimmed as one.
//
// Names, not counts or line numbers. A line number turns every unrelated edit above it
// into a failure here, which teaches people to re-run and paste rather than to read;
// and a bare count cannot say WHICH bypass appeared, which is the only thing worth
// knowing when the number moves. Methods are qualified by receiver, because two types
// in one file routinely carry methods of the same name.
var sanctionedSystemContexts = map[string][]string{
	"backend/core/rdb/audit.go":                  {"RdbManager.RecordAuthEvent"},
	"backend/core/rdb/rdb.go":                    {"RdbManager.ExecuteInitialize"},
	"backend/core/secrets/selftest.go":           {"SelfTest"},
	"backend/core/secrets/store.go":              {"gormStore.ctxDB"},
	"backend/services/ai-inference/model/api.go": {"Api.AIProviders", "Api.sys"},
	"backend/services/command-delivery/processor/CommandDeliveryProcessor.go": {
		"CommandDeliveryProcessor.deliverPendingCommands", "CommandDeliveryProcessor.sweepLocked",
	},
	"backend/services/command-delivery/processor/HoldReconciler.go":            {"CommandDeliveryProcessor.reconcileOnePage"},
	"backend/services/command-delivery/processor/StrandedReconciler.go":        {"CommandDeliveryProcessor.reconcileStrandedPage"},
	"backend/services/device-management/model/api_provisioning.go":             {"Api.ProvisionDeviceBootstrap"},
	"backend/services/device-state/processor/StateProcessor.go":                {"StateProcessor.runInactivityMonitor"},
	"backend/services/event-management/model/analytics.go":                     {"ReconcileAnalyticsSurface"},
	"backend/services/event-management/model/lifecycle.go":                     {"ApplyDataLifecyclePolicies"},
	"backend/services/event-management/processor/AnchorReconciliationSweep.go": {"AnchorReconciliationSweep.runOnce"},
	// The DETECT engine is a per-Instance singleton. At startup it holds no tenant and
	// must rebuild its rule set, dead-man arming and dynamic-threshold view from every
	// tenant's rows, so each of these four reads the whole projection. They are the only
	// reads in that service that do; everything else there is scoped from the context
	// like the rest of the tree.
	"backend/services/event-processing/model/detect_rule_store.go":               {"DetectRuleStore.LoadAll"},
	"backend/services/event-processing/model/device_attribute_store.go":          {"DeviceAttributeStore.LoadAll"},
	"backend/services/event-processing/model/device_roster_store.go":             {"DeviceRosterStore.LoadAll"},
	"backend/services/event-processing/model/profile_active_store.go":            {"ProfileActiveStore.LoadAll"},
	"backend/services/notification-management/processor/RetentionSweeper.go":     {"RetentionSweeper.runOnce"},
	"backend/services/notification-management/processor/escalation_scheduler.go": {"EscalationScheduler.runOnce"},
	"backend/services/user-management/deadletters/context.go":                    {"systemCtx"},
	"backend/services/user-management/iam/purge.go":                              {"Store.PurgeRecords"},
	"backend/services/user-management/iam/store.go":                              {"Store.AuditEvents", "Store.sys"},
	"backend/services/user-management/identity/keys.go": {
		"Manager.activeKeyAge", "Manager.loadSigningKeysLocked", "Manager.rotateSigningKeyLocked",
	},
	"backend/services/user-management/purge/coordinator.go": {"Coordinator.PurgeTenant", "Coordinator.pass"},
	"backend/services/user-management/purge/relational.go":  {"Relational.handle"},
	"backend/services/user-management/settings/store.go":    {"Store.sys"},
}

// Every bypass of tenant isolation in the workspace is declared.
//
// The scanner's own behaviour — that it follows the import PATH rather than the
// identifier, that it finds a call inside a func literal and at package level, and that
// it stays quiet on a same-named method belonging to anything else — is pinned in
// systemcontext_test.go. That file is the one that shows this check can fail; this one
// only runs it over the repository.
func TestEveryTenantIsolationBypassIsDeclared(t *testing.T) {
	root := workspaceRoot(t)
	found, visited, err := systemContextSitesUnder(root)
	if err != nil {
		t.Fatalf("scanning %s: %v", root, err)
	}
	for _, dir := range workspaceModuleDirs(t, root) {
		abs, err := filepath.Abs(dir)
		if err != nil {
			t.Fatalf("resolving %s: %v", dir, err)
		}
		if !visited[abs] {
			t.Errorf("the scan of %s never descended into %s, so whatever it reports about "+
				"that tree it did not look at. A clean scan and a scan that reached nothing "+
				"are the same answer, which is why this is checked separately", root, abs)
		}
	}

	byFile := map[string]map[string]bool{}
	for _, s := range found {
		rel, err := filepath.Rel(root, s.File)
		if err != nil {
			rel = s.File
		}
		rel = filepath.ToSlash(rel)
		if byFile[rel] == nil {
			byFile[rel] = map[string]bool{}
		}
		byFile[rel][s.Function] = true
	}

	for _, file := range sortedKeys(byFile) {
		declared := map[string]bool{}
		for _, name := range sanctionedSystemContexts[file] {
			declared[name] = true
		}
		for _, fn := range sortedKeys(byFile[file]) {
			if declared[fn] {
				continue
			}
			t.Errorf("%s: %s calls core.WithSystemContext, which turns OFF tenant isolation "+
				"for every statement made with that context — no tenant predicate is "+
				"injected and the fail-closed ErrNoTenant check is skipped. It is not "+
				"declared in sanctionedSystemContexts. If the bypass is right, say why at "+
				"the call site and add the line here, which is the security review the "+
				"doc on WithSystemContext promises. If what you wanted was a statement "+
				"scoped to one tenant, put the tenant in the context with core.WithTenant "+
				"instead and the predicate is injected for you",
				file, fn)
		}
	}

	for _, file := range sortedKeys(sanctionedSystemContexts) {
		for _, name := range sanctionedSystemContexts[file] {
			if byFile[file][name] {
				continue
			}
			t.Errorf("sanctionedSystemContexts lists %s in %s but the scan does not find it. "+
				"The entry is stale: delete the line, and the file's whole entry if that "+
				"empties it. An over-stated list of bypasses is read by the next person as "+
				"where isolation is off, and it is worth nothing if it names places it is not",
				name, file)
		}
		if len(sanctionedSystemContexts[file]) == 0 {
			t.Errorf("sanctionedSystemContexts has an empty entry for %s; delete the line "+
				"rather than leaving a file listed with no bypass in it", file)
		}
	}
}

// A scan that parsed nothing reports no call sites, which reads exactly like a tree
// with no bypasses in it. systemContextSitesUnder refuses rather than returning that
// answer, and this is the test that the refusal works.
func TestASystemContextScanThatParsesNothingRefusesRatherThanReportingClean(t *testing.T) {
	found, _, err := systemContextSitesUnder(t.TempDir())
	if err == nil {
		t.Fatalf("scanning an empty directory returned %d sites and no error; a scan that read "+
			"no files must say so, because silence is this guard's failure mode", len(found))
	}
}
