// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	k8stesting "k8s.io/client-go/testing"
)

var testNow = time.Date(2026, 7, 28, 14, 5, 6, 0, time.UTC)

// ---------------------------------------------------------------------------
// The archive path must not move
// ---------------------------------------------------------------------------

// A RESTORE IS A ONE-SHOT FLAG; THE ARCHIVE PATH IT FORCES IS PERMANENT
// CONFIGURATION. That mismatch is the whole reason resolveArchivePaths reads the
// live cluster instead of deriving from argv.
//
// The failure it prevents is quiet and delayed. An operator recovers with
// --restore-rdb-from, the restored cluster takes a fresh archive path, and weeks
// later an ordinary `dcctl bootstrap` re-run — no restore flag, nothing unusual —
// would re-derive the path from a flag that is now empty and hand OpenTofu the
// default. The helm upgrade retargets the LIVE cluster's WAL archiver at a
// different path, one that holds no base backup, so the instance has nothing to
// restore from until the next scheduled backup lands. Green apply, no warning.
//
// This is the same shape as the credential rotation A8 closed, and it is closed
// the same way: the live value wins by construction.
func TestArchivePathSurvivesAFlaglessRerun(t *testing.T) {
	restored := liveArchiveState{
		Rdb:  clusterArchiveState{Exists: true, Path: "dc-rdb-restored-20260728T140506Z"},
		Tsdb: clusterArchiveState{Exists: true, Path: "dc-tsdb-restored-20260728T140506Z"},
	}

	// The re-run: no restore flags at all.
	got := resolveArchivePaths(restored, RestorePlan{}, testNow.Add(72*time.Hour))

	if got.Rdb != restored.Rdb.Path {
		t.Errorf("a flagless re-run moved the relational store's archive path:\n"+
			"  was %q\n  now %q\n"+
			"This retargets a LIVE cluster's WAL archiver at a path with no base backup in\n"+
			"it, on a run that reports success. The path must come from the cluster, not\n"+
			"from this run's flags.", restored.Rdb.Path, got.Rdb)
	}
	if got.Tsdb != restored.Tsdb.Path {
		t.Errorf("a flagless re-run moved the event store's archive path: was %q, now %q",
			restored.Tsdb.Path, got.Tsdb)
	}
	if len(got.AlreadyLive) != 0 {
		t.Errorf("nothing was being restored, so no store should be reported as an ineffective "+
			"restore; got %v", got.AlreadyLive)
	}
}

// An ordinary install archives under its Cluster's own name and renders no
// explicit serverName, so the live path is "". That must stay "" — emitting a
// derived path for an instance that never restored anything would move an archive
// nobody asked to move.
func TestOrdinaryInstallKeepsTheDefaultArchivePath(t *testing.T) {
	live := liveArchiveState{
		Rdb:  clusterArchiveState{Exists: true},
		Tsdb: clusterArchiveState{Exists: true},
	}
	got := resolveArchivePaths(live, RestorePlan{}, testNow)
	if got.Rdb != "" || got.Tsdb != "" {
		t.Fatalf("an ordinary install must archive under the Cluster's own name (the OpenTofu "+
			"default, emitted as no var at all); got rdb=%q tsdb=%q", got.Rdb, got.Tsdb)
	}
}

// 🔴 THE DEFECT THIS BRANCH SHIPPED WITH, kept as a test because it is the exact
// harm the design claims to prevent and it survived a mutation round.
//
// An ordinary install renders no serverName, so the live path is "" while the
// cluster is very much alive and archiving. The first version of resolveArchivePaths
// keyed on `live.Path != ""` where it meant `live.Exists`, so this — a healthy
// instance plus `--restore-rdb-from dc-rdb`, the obvious wrong guess a
// half-followed runbook produces — fell through to the derived branch.
//
// The restore itself correctly does nothing (spec.bootstrap is CREATE-only) and is
// warned about. The archive path move is not: the helm upgrade rewrites a RUNNING
// Cluster's archiver serverName, the prefix holding the base backup stops receiving
// WAL, and the prefix now receiving WAL has no base backup until the next scheduled
// one. Up to a day in which the database is restorable to no point at all, on a
// green apply, on the run that was trying to recover.
func TestARestoreAimedAtALiveClusterNeverMovesItsArchive(t *testing.T) {
	for name, live := range map[string]clusterArchiveState{
		"archiving under its own name": {Exists: true, Path: ""},
		"archiving under a set path":   {Exists: true, Path: "dc-rdb-2026"},
	} {
		t.Run(name, func(t *testing.T) {
			got := resolveArchivePaths(
				liveArchiveState{Rdb: live}, RestorePlan{RdbFrom: RdbClusterName}, testNow)
			if got.Rdb != live.Path {
				t.Fatalf("a restore aimed at a LIVE cluster moved its archive path from %q to %q.\n"+
					"  The restore cannot run (spec.bootstrap is read at CREATE only), so the only\n"+
					"  thing this achieves is retargeting a running archiver at a prefix with no\n"+
					"  base backup in it. Key this branch on Exists, not on Path.", live.Path, got.Rdb)
			}
			if !slices.Contains(got.AlreadyLive, RdbClusterName) {
				t.Errorf("the ineffective restore was not reported: %v", got.AlreadyLive)
			}
		})
	}
}

// The wedge, in the case it can actually happen: a restore into a store that is
// NOT there, which is the only state in which a recovery bootstrap runs at all.
// Leaving the path empty would send the recovered cluster back over the archive it
// just read, and CloudNativePG does not fail that cleanly — it hangs in `Setting up
// primary` logging `Expected empty archive`.
func TestRestoreIntoADefaultInstallTakesAFreshPath(t *testing.T) {
	got := resolveArchivePaths(liveArchiveState{}, RestorePlan{RdbFrom: RdbClusterName}, testNow)

	if got.Rdb == "" {
		t.Fatal("restoring into a store with no explicit archive path kept the default, which " +
			"means the recovered cluster archives back over the path it recovered FROM. " +
			"CloudNativePG hangs in `Setting up primary` on that, it does not fail.")
	}
	if got.Rdb == RdbClusterName {
		t.Fatalf("the recovered cluster's archive path equals the source %q", RdbClusterName)
	}
	if !strings.HasPrefix(got.Rdb, RdbClusterName+"-restored-") {
		t.Errorf("archive path %q is not recognisable as a restore of %q", got.Rdb, RdbClusterName)
	}
}

// Recovering a store from the path it USED to own — the instance is gone, its
// archive is not — must not reuse that path, or the rebuilt cluster archives
// straight back over the WAL it is recovering from.
func TestRestoreFromAnArchiveTheDeadInstanceOwnedTakesAFreshPath(t *testing.T) {
	const owned = "dc-rdb-2026"
	got := resolveArchivePaths(liveArchiveState{}, RestorePlan{RdbFrom: owned}, testNow)
	if got.Rdb == owned {
		t.Fatalf("recovering from %q kept it as the archive path, so the restored cluster "+
			"archives back over the WAL it recovered from — `Setting up primary`, forever", owned)
	}
}

// Re-running the SAME restore (the recovery did not take, the operator tries
// again) must not invent a second path. The Cluster is already there carrying the
// path it took on the first attempt; keeping it is what makes the retry idempotent.
func TestRerunningARestoreKeepsThePathItAlreadyTook(t *testing.T) {
	const taken = "dc-rdb-restored-20260728T140506Z"
	live := liveArchiveState{Rdb: clusterArchiveState{Exists: true, Path: taken}}

	got := resolveArchivePaths(live, RestorePlan{RdbFrom: RdbClusterName}, testNow.Add(time.Hour))
	if got.Rdb != taken {
		t.Fatalf("re-running the same restore moved the archive path from %q to %q", taken, got.Rdb)
	}
	if !slices.Contains(got.AlreadyLive, RdbClusterName) {
		t.Errorf("the Cluster already exists, so this restore will NOT run (spec.bootstrap is "+
			"read at CREATE only) — that has to be reported, and %v does not name it", got.AlreadyLive)
	}
}

// A SECOND disaster restored from the SAME source archive. A plain
// "<source>-restored" would collide with the first restore's path — which is not
// empty, because that cluster archived into it — and wedge exactly the run that is
// trying to recover.
func TestTwoRestoresFromOneSourceTakeDifferentPaths(t *testing.T) {
	fresh := liveArchiveState{} // both stores gone: this is the disaster case
	plan := RestorePlan{RdbFrom: RdbClusterName}

	first := resolveArchivePaths(fresh, plan, testNow)
	second := resolveArchivePaths(fresh, plan, testNow.Add(time.Second))

	if first.Rdb == second.Rdb {
		t.Fatalf("two restores from %q both took archive path %q. The first cluster archived "+
			"into it, so the second recovers and then hangs in `Setting up primary` — during "+
			"a recovery.", RdbClusterName, first.Rdb)
	}
}

// The stores are restored independently, because their timelines are independent:
// rewinding telemetry to yesterday does not mean the control plane should be
// rewound with it. Restoring one must not disturb the other's archive path.
func TestRestoringOneStoreLeavesTheOtherAlone(t *testing.T) {
	live := liveArchiveState{
		Rdb:  clusterArchiveState{Exists: true, Path: "dc-rdb-owned"},
		Tsdb: clusterArchiveState{Exists: true, Path: "dc-tsdb-owned"},
	}
	got := resolveArchivePaths(live, RestorePlan{TsdbFrom: "dc-tsdb-old"}, testNow)

	if got.Rdb != "dc-rdb-owned" {
		t.Errorf("restoring the event store moved the relational store's archive path to %q", got.Rdb)
	}
	if got.Tsdb != "dc-tsdb-owned" {
		t.Errorf("the event store already owns a path that is not the source; it should be kept, got %q", got.Tsdb)
	}
}

// A fresh ordinary install — nothing there, nothing being restored — must settle on
// "", which infraVars then omits entirely.
//
// The empty string is not cosmetic here. Unset, the chart renders NO serverName
// parameter and CloudNativePG defaults it to the Cluster's own name; the chart's
// restore guard keys on exactly that emptiness. Emitting the name explicitly reaches
// the same archive prefix by a path the guard can no longer see.
func TestAFreshOrdinaryInstallSettlesOnNoArchivePath(t *testing.T) {
	got := resolveArchivePaths(liveArchiveState{}, RestorePlan{}, testNow)
	if got.Rdb != "" || got.Tsdb != "" {
		t.Fatalf("a fresh install with no restore must take the root's default (no var at all); "+
			"got rdb=%q tsdb=%q", got.Rdb, got.Tsdb)
	}
	if len(got.AlreadyLive) != 0 {
		t.Errorf("no store exists and none is being restored; got %v", got.AlreadyLive)
	}
}

// 🔴 THE REAL RECOVERY MUST NOT BE TOLD IT WILL NOT RUN. AlreadyLive drives a
// warning that says the restore is a no-op and the operator should destroy the
// instance and rebuild it. Firing that on a restore into an EMPTY cluster — the one
// case where the restore genuinely does run — hands an operator mid-incident a
// destroy instruction for the data they have just recovered.
func TestARestoreIntoNothingIsNotReportedAsIneffective(t *testing.T) {
	got := resolveArchivePaths(liveArchiveState{},
		RestorePlan{RdbFrom: RdbClusterName, TsdbFrom: TsdbClusterName}, testNow)
	if len(got.AlreadyLive) != 0 {
		t.Fatalf("neither Cluster exists, so both restores WILL run; reporting %v tells the "+
			"operator to destroy and rebuild the instance they are in the middle of recovering",
			got.AlreadyLive)
	}
}

// The stamp claims UTC — the format literal ends in Z — so it has to BE UTC. An
// operator reads these paths against backup timestamps while deciding which archive
// to recover from, and a local-time stamp wearing a Z is off by the offset in the
// direction of "the wrong archive looks like the right one".
func TestTheRestoreStampIsUTCNotLocalTime(t *testing.T) {
	// 14:05Z is the previous DAY in this zone, so the mutation is visible in the
	// date as well as the time.
	zone := time.FixedZone("UTC-16", -16*60*60)
	got := RestoredArchivePath(RdbClusterName, testNow.In(zone))

	if want := RestoredArchivePath(RdbClusterName, testNow); got != want {
		t.Fatalf("the same instant produced %q in %s and %q in UTC — the stamp is formatted in "+
			"the local zone while claiming Z", got, zone, want)
	}
	if !strings.HasSuffix(got, "-20260728T140506Z") {
		t.Errorf("archive path %q does not carry the UTC instant", got)
	}
}

// ---------------------------------------------------------------------------
// Reading the live path
// ---------------------------------------------------------------------------

// fakeDyn builds a dynamic client holding the given Clusters, mirroring
// reactingClient in cnpgadmission_test.go.
func fakeDyn(objs ...runtime.Object) *dynamicfake.FakeDynamicClient {
	return dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
		map[schema.GroupVersionResource]string{clusterGVR: "ClusterList"}, objs...)
}

// cnpgCluster builds a Cluster carrying the given spec.plugins entries.
func cnpgCluster(name string, plugins ...any) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": clusterGVR.GroupVersion().String(),
		"kind":       "Cluster",
		"metadata":   map[string]any{"name": name, "namespace": infraNamespace},
		"spec":       map[string]any{"plugins": plugins},
	}}
}

// The archive path must come from the entry that ARCHIVES, not from whichever
// barman entry happens to be first.
//
// 🔴 THE FIXTURE HERE IS DELIBERATELY NOT WHAT THE CHART RENDERS. A restored
// Cluster's recovery source lands under spec.externalClusters[].plugin — a
// different field the reader never touches — so spec.plugins holds exactly one
// barman entry today, and a reader matching on plugin name alone would pass every
// test written against the real render. That is the whole problem: the check would
// be worth nothing until the day the shape changed, which is the day it is needed.
//
// CNPG permits several plugin entries with only one WAL archiver among them, so
// this builds the shape the API allows rather than the one the chart happens to
// emit. Reading the wrong entry hands back an archive this cluster does not own,
// and the next re-run emits it as backup_server_name — pointing a live archiver at
// a dead instance's WAL.
func TestArchivePathReadsTheArchiverNotTheRecoverySource(t *testing.T) {
	cl := cnpgCluster(RdbClusterName,
		// Deliberately FIRST, and deliberately without isWALArchiver: this is the
		// entry an implementation that matched on plugin name alone would return.
		map[string]any{
			"name":       barmanPluginName,
			"parameters": map[string]any{"serverName": "dc-rdb", "barmanObjectName": "dc-rdb-backup"},
		},
		map[string]any{
			"name":          barmanPluginName,
			"isWALArchiver": true,
			"parameters":    map[string]any{"serverName": "dc-rdb-restored-20260728T140506Z"},
		},
	)

	got, err := clusterArchivePath(context.Background(), fakeDyn(cl), infraNamespace, RdbClusterName)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Exists {
		t.Fatal("the Cluster is there; Exists must say so")
	}
	if got.Path != "dc-rdb-restored-20260728T140506Z" {
		t.Fatalf("read archive path %q — that is the archive this cluster RECOVERED FROM, not "+
			"the one it owns. Emitting it would point a live archiver at the dead instance's "+
			"WAL. Match on isWALArchiver, not on the plugin name.", got.Path)
	}
}

// An ordinary install renders no serverName parameter at all (the chart emits it
// only `with .Values.backup.serverName`). That is "archiving under its own name",
// not "not there" — and the difference decides whether a restore derives a fresh
// path or is refused.
func TestArchivePathOfAnOrdinaryClusterIsPresentButEmpty(t *testing.T) {
	cl := cnpgCluster(RdbClusterName, map[string]any{
		"name":          barmanPluginName,
		"isWALArchiver": true,
		"parameters":    map[string]any{"barmanObjectName": "dc-rdb-backup"},
	})

	got, err := clusterArchivePath(context.Background(), fakeDyn(cl), infraNamespace, RdbClusterName)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Exists || got.Path != "" {
		t.Fatalf("want Exists=true Path=\"\" for a cluster archiving under its own name, got %+v", got)
	}
}

// A cluster that is not there is the disaster case, and it must read as such
// rather than as an error — this is the path a rebuild takes.
func TestArchivePathOfAMissingClusterIsNotAnError(t *testing.T) {
	got, err := clusterArchivePath(context.Background(), fakeDyn(), infraNamespace, RdbClusterName)
	if err != nil {
		t.Fatalf("a missing Cluster is a fresh install, not a failure: %v", err)
	}
	if got.Exists || got.Path != "" {
		t.Fatalf("want the zero state for a missing Cluster, got %+v", got)
	}
}

// 🔴 THE COUNTERWEIGHT. Every branch above turns some error into "" — so the check
// is only worth anything while a REAL failure still fails. "We could not tell"
// read as "there is nothing there" is exactly the destructive direction: it emits
// the default path against a cluster that is alive and archiving elsewhere.
func TestArchivePathFailsWhenTheClusterCannotBeRead(t *testing.T) {
	dyn := fakeDyn()
	dyn.PrependReactor("get", "clusters", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewServiceUnavailable("the apiserver is having a moment")
	})

	_, err := clusterArchivePath(context.Background(), dyn, infraNamespace, RdbClusterName)
	if err == nil {
		t.Fatal("an unreadable Cluster was reported as absent. That makes dcctl emit the DEFAULT " +
			"archive path against a cluster that may be alive and archiving somewhere else.")
	}
	if !strings.Contains(err.Error(), "Refusing to continue") {
		t.Errorf("the error should say why it refuses rather than guesses; got: %v", err)
	}
}

// 🔴 THE WIRING NO OTHER TEST IN THIS PACKAGE CAN REACH. readLiveArchiveState is
// the seam every stepRenderConfig test stubs, so which Cluster's state lands in
// which field was checked by nothing — and the fixtures above use one store, which
// is exactly the shape a swap survives.
//
// A swap is not a mislabel. Each store would be handed the OTHER's archive path and
// emit it as its own backup_server_name, so the next apply retargets the relational
// archiver at the event store's WAL prefix and vice versa. Both clusters go on
// archiving, to prefixes holding no base backup of the database writing to them:
// two stores restorable to nothing, on a green apply.
func TestReadArchiveStateKeepsTheTwoStoresApart(t *testing.T) {
	archiver := func(serverName string) map[string]any {
		return map[string]any{
			"name":          barmanPluginName,
			"isWALArchiver": true,
			"parameters":    map[string]any{"serverName": serverName},
		}
	}
	dyn := fakeDyn(
		cnpgCluster(RdbClusterName, archiver("rdb-owns-this")),
		cnpgCluster(TsdbClusterName, archiver("tsdb-owns-this")),
	)

	got, err := readArchiveState(context.Background(), dyn)
	if err != nil {
		t.Fatal(err)
	}
	if got.Rdb.Path != "rdb-owns-this" || got.Tsdb.Path != "tsdb-owns-this" {
		t.Fatalf("the stores were read into each other's slot: rdb=%q tsdb=%q.\n"+
			"  Each store then emits the other's archive path, and the next apply points\n"+
			"  both WAL archivers at prefixes with no base backup of the database writing\n"+
			"  to them — silently, on a green apply.", got.Rdb.Path, got.Tsdb.Path)
	}
}

// Before CloudNativePG is installed — a first bootstrap, or any --no-cnpg run —
// the Cluster KIND does not exist, and the API server answers with a no-match
// rather than a not-found. That is "nothing is archiving yet", not a failure: it is
// the state every fresh install starts in, so reading it as an error would refuse
// the ordinary path.
func TestArchivePathTreatsAMissingCNPGCRDAsNothingThere(t *testing.T) {
	dyn := fakeDyn()
	dyn.PrependReactor("get", "clusters", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, &meta.NoResourceMatchError{PartialResource: clusterGVR}
	})

	got, err := clusterArchivePath(context.Background(), dyn, infraNamespace, RdbClusterName)
	if err != nil {
		t.Fatalf("a cluster with no CloudNativePG CRD installed is a fresh install, not a "+
			"failure — this refuses every first bootstrap and every --no-cnpg run: %v", err)
	}
	if got.Exists || got.Path != "" {
		t.Fatalf("want the zero state where the kind does not exist, got %+v", got)
	}
}

// 🔴 MALFORMED IS NOT ABSENT, and this pair is the counterweight to the two tests
// above: "not there" reads as empty, everything ELSE has to fail.
//
// The guard for the plugin list used to be `if err != nil || len(plugins) == 0`,
// which looks fail-closed and is not — NestedSlice returns a nil slice on error, so
// the error branch was unreachable and both cases returned the same "exists,
// archiving under its own name". That answer is the destructive one: the next
// re-run emits no serverName and retargets a live archiver at a prefix with no base
// backup in it.
// ALL FIVE READS, not the two that were guarded first. Guarding some of them is
// worse than guarding none: the comments claimed the spec was checked while
// `name`, `isWALArchiver` and the entry's own type were still read with `_, _` and
// SKIPPED on error — reaching the same destructive default by a route the guards
// did not cover, under a comment saying they did.
func TestArchivePathFailsOnAnUnreadableArchiverSpec(t *testing.T) {
	archiver := func(over map[string]any) map[string]any {
		plug := map[string]any{
			"name":          barmanPluginName,
			"isWALArchiver": true,
			"parameters":    map[string]any{"serverName": "dc-rdb-owned"},
		}
		for k, v := range over {
			plug[k] = v
		}
		return plug
	}
	for name, spec := range map[string]map[string]any{
		"spec.plugins is not a list":     {"plugins": "barman-cloud.cloudnative-pg.io"},
		"an entry is not an object":      {"plugins": []any{"barman-cloud.cloudnative-pg.io"}},
		"name is not a string":           {"plugins": []any{archiver(map[string]any{"name": int64(7)})}},
		"isWALArchiver is not a boolean": {"plugins": []any{archiver(map[string]any{"isWALArchiver": "true"})}},
		"serverName is not a string": {"plugins": []any{archiver(
			map[string]any{"parameters": map[string]any{"serverName": int64(2026)}})}},
	} {
		t.Run(name, func(t *testing.T) {
			cl := &unstructured.Unstructured{Object: map[string]any{
				"apiVersion": clusterGVR.GroupVersion().String(),
				"kind":       "Cluster",
				"metadata":   map[string]any{"name": RdbClusterName, "namespace": infraNamespace},
				"spec":       spec,
			}}

			got, err := clusterArchivePath(
				context.Background(), fakeDyn(cl), infraNamespace, RdbClusterName)
			if err == nil {
				t.Fatalf("an unreadable archiver spec was reported as %+v. Empty means "+
					"'archiving under its own name', so this retargets a LIVE archiver at a "+
					"prefix with no base backup — the one direction this reader must never "+
					"guess in.", got)
			}
			if !strings.Contains(err.Error(), "Refusing to continue") {
				t.Errorf("the error should say why it refuses rather than guesses; got: %v", err)
			}
		})
	}
}

// stepRenderConfig asks the cluster TWO questions — what the instance is running,
// and what its databases are archiving under — and a test that stubs only the
// first reaches a real API server for the second.
//
// That is not hypothetical: every stepRenderConfig test in this package did
// exactly that when the archive lookup was added. They stayed green on a
// developer's desk, where a kubeconfig exists and the Clusters do not, and failed
// in CI where building the config is the first thing that errors. Worse than the
// CI failure is the green run: it was answering from whatever cluster the
// developer's context happened to point at.
//
// So withDeployedInstance stubs both, and this is the check that it still does.
func TestDeployedInstanceStubCoversEveryOutsideRead(t *testing.T) {
	archiveBefore := reflect.ValueOf(readLiveArchiveState).Pointer()
	hashesBefore := reflect.ValueOf(lookupDeployedBrokerHashes).Pointer()
	recordReadBefore := reflect.ValueOf(readDeployedBrokerRecord).Pointer()
	recordStoreBefore := reflect.ValueOf(storeBrokerRecord).Pointer()
	withDeployedInstance(t, nil, nil)
	if reflect.ValueOf(readLiveArchiveState).Pointer() == archiveBefore {
		t.Fatal("withDeployedInstance no longer stubs readLiveArchiveState, so every test that " +
			"drives stepRenderConfig through it now reads the database archive state from a " +
			"REAL cluster — the developer's own, if they have one. Restore the withArchiveState " +
			"call in withDeployedInstance.")
	}
	// The third one, added with the broker-hash reuse. Same failure mode as the
	// above and the same reason it is worth a standing check rather than a comment:
	// unstubbed it reads a ConfigMap from whatever cluster the developer's kubeconfig
	// points at, and because it answers "nothing" for every error it would do so
	// SILENTLY — green on a desk with no cluster, green on a desk with the wrong one,
	// and quietly reading a real instance's broker config on a desk with the right one.
	if reflect.ValueOf(lookupDeployedBrokerHashes).Pointer() == hashesBefore {
		t.Fatal("withDeployedInstance no longer stubs lookupDeployedBrokerHashes, so every test " +
			"driving stepRenderConfig now reads the broker's ConfigMap from a REAL cluster. " +
			"Restore the withDeployedBrokerHashes call in withDeployedInstance.")
	}
	// 🔴 THE FOURTH AND FIFTH ARE A FILE, WHICH IS WHY THE TEST'S NAME NO LONGER SAYS
	// "CLUSTER". Every test here runs with Instance "prod", so unstubbed these resolve
	// ~/.devicechain/prod on whoever runs the suite. The write is the dangerous half:
	// measured before the stub existed, one `go test ./...` created that directory and
	// left a real credential record in it, which a later test in the same run then
	// reused. On a machine that has a prod instance, the suite would have overwritten
	// the bridge copy that instance depends on.
	if reflect.ValueOf(readDeployedBrokerRecord).Pointer() == recordReadBefore {
		t.Fatal("withDeployedInstance no longer stubs readDeployedBrokerRecord, so every test " +
			"driving stepRenderConfig now reads ~/.devicechain/<instance> on the machine running " +
			"the suite. Restore the withBrokerRecord call in withDeployedInstance.")
	}
	if reflect.ValueOf(storeBrokerRecord).Pointer() == recordStoreBefore {
		t.Fatal("withDeployedInstance no longer stubs storeBrokerRecord, so every test driving " +
			"stepRenderConfig now WRITES a credential record into ~/.devicechain/<instance> on " +
			"the machine running the suite. Restore the withBrokerRecord call in " +
			"withDeployedInstance.")
	}
}

// EVERY TEST ABOVE CALLS resolveArchivePaths DIRECTLY, so all of them stay green
// on a pipeline that never calls it. This is the one that drives the real step and
// reads the values the OpenTofu apply will actually consume.
//
// It is the same shape as the flagless-rerun test, run through the whole of
// stepRenderConfig: a live instance archiving under a restored path, re-bootstrapped
// with no restore flags.
func TestRenderConfigKeepsTheLiveArchivePath(t *testing.T) {
	const owned = "dc-rdb-restored-20260728T140506Z"
	withExistingInstance(t, "3q2+796tvu/erb7v3q2+796tvu/erb7v3q0=", nil)
	withArchiveState(t, liveArchiveState{
		Rdb:  clusterArchiveState{Exists: true, Path: owned},
		Tsdb: clusterArchiveState{Exists: true},
	}, nil)

	st := &State{Instance: "prod", BuildImages: true, Values: map[string]string{}}
	if err := stepRenderConfig(t.Context(), st); err != nil {
		t.Fatal(err)
	}
	if got := st.Values["backupServerNameRdb"]; got != owned {
		t.Fatalf("stepRenderConfig settled the relational archive path as %q, want %q — a "+
			"flagless re-run must not move it", got, owned)
	}
	if got := st.Values["backupServerNameTsdb"]; got != "" {
		t.Fatalf("the event store archives under its own name; want no explicit path, got %q", got)
	}
	if slices.Contains(infraVars(st), "backup_server_name_tsdb=") {
		t.Error("an empty archive path must be omitted, not passed as an empty var")
	}
	if !slices.Contains(infraVars(st), "backup_server_name_rdb="+owned) {
		t.Errorf("the settled path never reached OpenTofu: %v", infraVars(st))
	}
}

// The disaster path through the real step: no instance, no Clusters, a restore
// requested. It has to end up with a path that is NOT the source, or the recovered
// cluster archives back over the archive it just read and hangs.
func TestRenderConfigDerivesAPathForARestoreIntoNothing(t *testing.T) {
	withExistingInstance(t, "", nil) // also stubs the archive lookup to "nothing there"

	st := &State{
		Instance:    "prod",
		BuildImages: true,
		Restore:     RestorePlan{RdbFrom: RdbClusterName, RdbTargetTime: "2026-07-28T12:00:00Z"},
		Values:      map[string]string{},
	}
	if err := stepRenderConfig(t.Context(), st); err != nil {
		t.Fatal(err)
	}
	got := st.Values["backupServerNameRdb"]
	if got == "" || got == RdbClusterName {
		t.Fatalf("recovering into an empty cluster settled on archive path %q; it must differ "+
			"from the source %q", got, RdbClusterName)
	}
	vars := infraVars(st)
	for _, want := range []string{
		"restore_rdb_from=" + RdbClusterName,
		"restore_rdb_target_time=2026-07-28T12:00:00Z",
		"backup_server_name_rdb=" + got,
	} {
		if !slices.Contains(vars, want) {
			t.Errorf("the restore never reached OpenTofu: %q missing from %v", want, vars)
		}
	}
}

// ---------------------------------------------------------------------------
// Flag validation
// ---------------------------------------------------------------------------

func TestResolveRestorePlanRefusesATargetWithNoSource(t *testing.T) {
	for name, f := range map[string]RestoreFlags{
		"rdb":  {RdbTargetTime: "2026-07-28T12:00:00Z", BackupsEnabled: true},
		"tsdb": {TsdbTargetTime: "2026-07-28T12:00:00Z", BackupsEnabled: true},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := ResolveRestorePlan(f); err == nil {
				t.Fatal("a recovery target with nothing to recover was accepted. The whole point " +
					"of the target is to stop replay before a known-bad moment; ignoring it " +
					"silently gives a full-archive restore and says nothing.")
			}
		})
	}
}

// The plugin that WRITES the archive is the plugin that READS it, so the flags
// that switch the backup destination off switch restore off with it. Refused here
// rather than at OpenTofu plan time, where the message tells the operator to set a
// variable dcctl does not expose.
func TestResolveRestorePlanRefusesARestoreWithNoBackupPlugin(t *testing.T) {
	_, err := ResolveRestorePlan(RestoreFlags{RdbFrom: "dc-rdb", BackupsEnabled: false})
	if err == nil {
		t.Fatal("a restore was accepted on a run with no backup plugin")
	}
	for _, want := range []string{"--no-cnpg", "--compact --no-tls"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal should name the flag that caused it; %q missing from: %v", want, err)
		}
	}
}

// 🔴 THE SILENT ONE. Everything else on this path fails loudly; a target with no
// offset SUCCEEDS and is wrong.
//
// PostgreSQL accepts "2026-07-27 13:59:00" and reads it in the recovering server's
// own TimeZone, so a target meant as UTC becomes a different instant, recovery
// stops there, and the operator gets a green restore of a database rewound to the
// wrong moment — the exact failure point-in-time recovery exists to avoid.
func TestResolveRestorePlanRefusesAnAmbiguousRecoveryTarget(t *testing.T) {
	for _, target := range []string{
		"2026-07-27 13:59:00",       // the PostgreSQL form, no offset: silently wrong
		"2026-07-27T13:59:00",       // RFC3339-shaped, offset missing
		"yesterday",                 // fails inside the recovering pod, minutes in
		"2026-07-27T13:59:00Z junk", // trailing rubbish
	} {
		t.Run(target, func(t *testing.T) {
			_, err := ResolveRestorePlan(RestoreFlags{
				RdbFrom: "dc-rdb", RdbTargetTime: target,
				BackupsEnabled: true, RootKeyRestored: true,
			})
			if err == nil {
				t.Fatalf("--restore-rdb-at %q was accepted; without an explicit offset the "+
					"recovery stops at a different moment than the operator named, and says "+
					"nothing", target)
			}
		})
	}
}

// The counterweight: refusing ambiguous targets is only safe while unambiguous
// ones still pass. Both offset forms have to work — an operator reading a
// timestamp off a log in local time should not have to convert it by hand.
func TestResolveRestorePlanAcceptsAnUnambiguousRecoveryTarget(t *testing.T) {
	for _, target := range []string{"2026-07-27T13:59:00Z", "2026-07-27T09:59:00-04:00"} {
		if _, err := ResolveRestorePlan(RestoreFlags{
			RdbFrom: "dc-rdb", RdbTargetTime: target,
			BackupsEnabled: true, RootKeyRestored: true,
		}); err != nil {
			t.Errorf("%s was rejected: %v", target, err)
		}
	}
}

// 🔴 A RESTORED RELATIONAL STORE UNDER A FRESH ROOT KEY IS PERMANENT DATA LOSS.
//
// Every stored secret is a DEK wrapped by the instance's secret-store root key.
// The ciphertext comes back with the database; the key lives in etcd, which is in
// no backup the platform takes. Recovering without --restore-root-key mints a new
// key and makes every one of those secrets permanently unreadable — on a bootstrap
// that reports success, surfacing later as connectors and notifications failing at
// first use, with nothing pointing back at the flags.
//
// The flag help said "pair with --restore-root-key". Help text is not a guard.
func TestResolveRestorePlanRefusesARelationalRestoreWithNoRootKey(t *testing.T) {
	_, err := ResolveRestorePlan(RestoreFlags{
		RdbFrom: "dc-rdb", BackupsEnabled: true, RootKeyRestored: false,
	})
	if err == nil {
		t.Fatal("the relational store was restored without its root key. Every secret it " +
			"holds is now unreadable, and the run reported success.")
	}
	if !strings.Contains(err.Error(), "--restore-root-key") {
		t.Errorf("the refusal must name the flag that fixes it; got: %v", err)
	}
}

// The EVENT store carries telemetry, not wrapped secrets, so recovering it alone
// under a fresh key loses nothing. Refusing it too would block the one restore an
// operator can safely do without an escrow artifact.
func TestAnEventStoreRestoreNeedsNoRootKey(t *testing.T) {
	if _, err := ResolveRestorePlan(RestoreFlags{
		TsdbFrom: "dc-tsdb", BackupsEnabled: true, RootKeyRestored: false,
	}); err != nil {
		t.Fatalf("restoring only the event store was refused: %v", err)
	}
}

func TestResolveRestorePlanPassesAValidRestoreThrough(t *testing.T) {
	f := RestoreFlags{
		RdbFrom: "dc-rdb", RdbTargetTime: "2026-07-28T12:00:00Z",
		TsdbFrom: "dc-tsdb", BackupsEnabled: true, RootKeyRestored: true,
	}
	plan, err := ResolveRestorePlan(f)
	if err != nil {
		t.Fatal(err)
	}
	if plan != (RestorePlan{RdbFrom: "dc-rdb", RdbTargetTime: "2026-07-28T12:00:00Z", TsdbFrom: "dc-tsdb"}) {
		t.Fatalf("the plan did not carry the flags through: %+v", plan)
	}
	if !plan.Active() {
		t.Error("a plan with a source is a restore")
	}
	if (RestorePlan{}).Active() {
		t.Error("the zero plan is an ordinary install")
	}
}

// ---------------------------------------------------------------------------
// The backups-enabled derivation, against the emitter
// ---------------------------------------------------------------------------

// DatabaseBackupsEnabled is a SECOND statement of something infraVars already
// decides, and the restore refusal above is built on it. Two statements of one
// fact drift; this walks the whole flag matrix and pins them together.
//
// It matters in one direction in particular. If this function says "backups are
// on" where the emitter says off, dcctl accepts a restore it cannot perform and
// the operator finds out during the rebuild.
func TestDatabaseBackupsEnabledMatchesWhatIsEmitted(t *testing.T) {
	const off = "enable_database_backups=false"
	agreed := 0
	for _, noCNPG := range []bool{false, true} {
		for _, compact := range []bool{false, true} {
			for _, noTLS := range []bool{false, true} {
				st := compactState(false)
				st.NoCNPG, st.Compact, st.NoTLS = noCNPG, compact, noTLS
				emittedOn := !slices.Contains(infraVars(st), off)
				if got := DatabaseBackupsEnabled(noCNPG, compact, noTLS); got != emittedOn {
					t.Errorf("--no-cnpg=%v --compact=%v --no-tls=%v: DatabaseBackupsEnabled says %v, "+
						"infraVars emits backups=%v.\n"+
						"  These must agree: ResolveRestorePlan refuses a restore on the first, and\n"+
						"  OpenTofu provisions the destination on the second. Where they disagree,\n"+
						"  dcctl either accepts a restore with no plugin to perform it or refuses one\n"+
						"  it could have done.", noCNPG, compact, noTLS, got, emittedOn)
					continue
				}
				agreed++
			}
		}
	}
	// The check that cannot fail: if compactState or infraVars ever stops producing
	// a comparable state, the loop above passes by comparing nothing.
	if agreed != 8 {
		t.Fatalf("the matrix compared %d of 8 combinations; the rest were never checked", agreed)
	}
}

// ---------------------------------------------------------------------------
// Emission
// ---------------------------------------------------------------------------

func TestInfraVarsCarriesTheRestoreAndTheArchivePaths(t *testing.T) {
	st := compactState(false)
	st.Restore = RestorePlan{
		RdbFrom: "dc-rdb", RdbTargetTime: "2026-07-28T12:00:00Z",
		TsdbFrom: "dc-tsdb", TsdbTargetTime: "2026-07-28T13:00:00Z",
	}
	st.Values["backupServerNameRdb"] = "dc-rdb-restored-20260728T140506Z"
	st.Values["backupServerNameTsdb"] = "dc-tsdb-restored-20260728T140506Z"

	vars := infraVars(st)
	for _, want := range []string{
		"restore_rdb_from=dc-rdb",
		"restore_rdb_target_time=2026-07-28T12:00:00Z",
		"restore_tsdb_from=dc-tsdb",
		"restore_tsdb_target_time=2026-07-28T13:00:00Z",
		"backup_server_name_rdb=dc-rdb-restored-20260728T140506Z",
		"backup_server_name_tsdb=dc-tsdb-restored-20260728T140506Z",
	} {
		if !slices.Contains(vars, want) {
			t.Errorf("infraVars did not emit %q; got %v", want, vars)
		}
	}
}

// An ordinary install must pass NO restore or archive-path vars: every one of them
// defaults to "" in the root, and passing an empty value where the default already
// is empty only adds a way for the two to disagree later.
func TestInfraVarsOfAnOrdinaryInstallCarriesNoRestore(t *testing.T) {
	vars := infraVars(compactState(false))
	for _, v := range vars {
		for _, prefix := range []string{"restore_", "backup_server_name_"} {
			if strings.HasPrefix(v, prefix) {
				t.Errorf("an ordinary install emitted %q; it should take the root's default", v)
			}
		}
	}
}
