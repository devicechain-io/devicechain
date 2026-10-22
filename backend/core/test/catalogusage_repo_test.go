// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package test

import (
	"fmt"
	"path/filepath"
	"sort"
	"testing"

	"github.com/devicechain-io/dc-microservice/kv"
	"github.com/devicechain-io/dc-microservice/streams"
)

// dynamicCatalogUses are the call sites whose stream is chosen at run time, so the scan
// cannot read it off the call. Each is named by file and method and says what it opens. A
// call the scan cannot resolve and that is not here FAILS the test, so a new run-time choice
// cannot go unchecked; and an entry whose call no longer exists fails it too (below), so the
// table cannot outlive what it describes.
var dynamicCatalogUses = map[string][]string{
	// The store forwarder: one writer per forwarded kind, built in a loop over a table.
	"event-sources/mqtt_failclosed.go#NewWriter": {streams.CommandResponses, streams.DeviceReports},
	// DETECT's replay, live-gap fill and rule preview read the stream their configuration
	// names, and event-processing's main sets it to resolved-events (main.go, Suffix:).
	"event-processing/processor/ResolvedEventsProcessor.go#NewReplayReader": {streams.ResolvedEvents},
	"event-processing/processor/live_gap.go#NewRangeReader":                 {streams.ResolvedEvents},
	"event-processing/internal/preview/preview.go#NewReplayReaderFromTime":  {streams.ResolvedEvents},
}

// catalogAliases name the constants that stand for a bucket in another package.
var catalogAliases = map[string]string{
	"identity.RefreshBucket":  kv.BucketRefreshTokens,
	"identity.AuthCodeBucket": kv.BucketOAuthCodes,
}

// indirectCatalogUses are declared roles that a service's OWN source never asks for, because
// code in core/messaging does it on the service's behalf with the manager it was given. They
// are the reason a declared role nothing calls is not always a stale one, and each says where.
var indirectCatalogUses = []catalogUse{
	{Area: "event-sources", Role: useBucket, Name: kv.BucketLocks,
		Pos: "core/messaging/mqtt_failclosed.go: the fail-closed store loop takes the shared lock itself"},
}

func usedBy(uses []catalogUse) map[catalogUse]bool {
	m := map[catalogUse]bool{}
	for _, u := range uses {
		m[catalogUse{Area: u.Area, Role: u.Role, Name: u.Name}] = true
	}
	return m
}

func declaredFor(u catalogUse) bool {
	switch u.Role {
	case useWrite:
		return streams.Writes(u.Name, u.Area)
	case useRead:
		return streams.ReadsAs(u.Name, u.Area, streams.ReadDurable) || streams.ReadsAs(u.Name, u.Area, streams.ReadAckFloor)
	case useReplay:
		return streams.ReadsAs(u.Name, u.Area, streams.ReadEphemeral)
	case useLive:
		return streams.ReadsAs(u.Name, u.Area, streams.ReadLive)
	case useBucket:
		return kv.Administers(u.Name, u.Area)
	}
	return false
}

// Every stream, bucket and consumer a service asks for is one its area declares, and every
// role the catalog declares for a service is one its code actually uses.
//
// The first half is what the manager enforces at start (messaging, catalog.go), checked here
// without starting anything. Each service's TestStartsUnderItsDeclaredCatalog
// (catalog_boot_test.go) starts that service's real wiring under a deployed manager, but only
// on the paths a test reaches; this scan also sees a call on a path no test reaches, which
// would otherwise be found by the pod that crash-loops. It is blind to the reader MODE
// (durable and ack-floor both count as "read"); only the boot tests hold that. The second
// half is the one the manager CANNOT see: a declared writer that never writes is a grant to nothing, and
// the first thing generated from the declaration would hand it out.
func TestEveryServiceUseIsDeclaredAndEveryDeclaredRoleIsUsed(t *testing.T) {
	root := workspaceRoot(t)
	uses, unresolved, usedDynamic, err := scanCatalogUses(
		filepath.Join(root, "backend", "services"),
		filepath.Join(root, "backend", "core", "streams", "streams.go"),
		filepath.Join(root, "backend", "core", "kv", "buckets.go"),
		dynamicCatalogUses, catalogAliases)
	if err != nil {
		t.Fatal(err)
	}
	if len(uses) == 0 {
		t.Fatal("the scan found no uses at all; it is reading nothing")
	}
	for _, u := range unresolved {
		t.Errorf("%s: the stream or bucket is chosen at run time and the scan cannot read it. "+
			"Name what it opens in dynamicCatalogUses (file#Method), so the declaration can be "+
			"checked against it", u)
	}

	// Forward: nothing used that is not declared.
	for _, u := range uses {
		if !streams.IsPlatformArea(u.Area) {
			t.Errorf("%s: %s is not a platform area (a services/ directory is an area)", u.Pos, u.Area)
		}
		if !declaredFor(u) {
			t.Errorf("%s: area %s uses %s %q, which core/streams and core/kv do not declare for it. "+
				"A deployed manager refuses this at start: declare the role, or the service is "+
				"reaching into something that is not its own", u.Pos, u.Area, u.Role, u.Name)
		}
	}

	// Backward: nothing declared that is not used.
	used := usedBy(append(append([]catalogUse{}, uses...), indirectCatalogUses...))
	var missing []string
	for _, s := range streams.All {
		for _, a := range s.Writers {
			if !used[catalogUse{Area: a, Role: useWrite, Name: s.Suffix}] {
				missing = append(missing, fmt.Sprintf("%s writes %s", a, s.Suffix))
			}
		}
		for _, r := range s.Readers {
			role := map[streams.ReadMode]string{
				streams.ReadDurable: useRead, streams.ReadAckFloor: useRead,
				streams.ReadEphemeral: useReplay, streams.ReadLive: useLive,
			}[r.Mode]
			if !used[catalogUse{Area: r.Area, Role: role, Name: s.Suffix}] {
				missing = append(missing, fmt.Sprintf("%s reads %s (%s)", r.Area, s.Suffix, r.Mode))
			}
		}
	}
	for _, b := range kv.All {
		for _, a := range b.Admins() {
			if !used[catalogUse{Area: a, Role: useBucket, Name: b.Name}] {
				missing = append(missing, fmt.Sprintf("%s opens bucket %s", a, b.Name))
			}
		}
	}
	sort.Strings(missing)
	for _, m := range missing {
		t.Errorf("declared but never used by that service's code: %s. Delete the declaration (it is a "+
			"grant to nothing), or, if core/messaging does the call on the service's behalf, "+
			"say so in indirectCatalogUses", m)
	}

	// The tables describe the tree: an entry that matches no call is stale.
	for _, key := range sortedKeys(dynamicCatalogUses) {
		if !usedDynamic[key] {
			t.Errorf("dynamicCatalogUses lists %q but the scan finds no such call: delete the entry", key)
		}
	}
}
