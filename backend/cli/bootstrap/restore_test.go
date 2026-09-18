// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"errors"
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
// --restore-tsdb-from, the restored cluster takes a fresh archive path, and weeks
// later an ordinary `dcctl bootstrap` re-run — no restore flag, nothing unusual —
// would re-derive the path from a flag that is now empty and hand OpenTofu the
// default. The helm upgrade retargets the LIVE cluster's WAL archiver at a
// different path, one that holds no base backup, so the instance has nothing to
// restore from until the next scheduled backup lands. Green apply, no warning.
//
// This is the same shape as the credential rotation A8 closed, and it is closed
// the same way: the live value wins by construction.
func TestArchivePathSurvivesAFlaglessRerun(t *testing.T) {
	restored := clusterArchiveState{Exists: true, Path: "dc-tsdb-restored-20260728T140506Z"}

	// The re-run: no restore flags at all.
	got := resolveArchivePaths(restored, RestorePlan{}, "", testNow.Add(72*time.Hour))

	if got.Tsdb != restored.Path {
		t.Errorf("a flagless re-run moved the event store's archive path:\n"+
			"  was %q\n  now %q\n"+
			"This retargets a LIVE cluster's WAL archiver at a path with no base backup in\n"+
			"it, on a run that reports success. The path must come from the cluster, not\n"+
			"from this run's flags.", restored.Path, got.Tsdb)
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
	got := resolveArchivePaths(clusterArchiveState{Exists: true}, RestorePlan{}, "dc-tsdb-fresh", testNow)
	if got.Tsdb != "" {
		t.Fatalf("an ordinary install must archive under the Cluster's own name (the OpenTofu "+
			"default, emitted as no var at all); got %q", got.Tsdb)
	}
}

// 🔴 THE DEFECT THIS BRANCH SHIPPED WITH, kept as a test because it is the exact
// harm the design claims to prevent and it survived a mutation round.
//
// An ordinary install renders no serverName, so the live path is "" while the
// cluster is very much alive and archiving. The first version of resolveArchivePaths
// keyed on `live.Path != ""` where it meant `live.Exists`, so this — a healthy
// instance plus `--restore-tsdb-from dc-tsdb`, the obvious wrong guess a
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
		"archiving under a set path":   {Exists: true, Path: "dc-tsdb-2026"},
	} {
		t.Run(name, func(t *testing.T) {
			got := resolveArchivePaths(live, RestorePlan{TsdbFrom: TsdbClusterName}, "", testNow)
			if got.Tsdb != live.Path {
				t.Fatalf("a restore aimed at a LIVE cluster moved its archive path from %q to %q.\n"+
					"  The restore cannot run (spec.bootstrap is read at CREATE only), so the only\n"+
					"  thing this achieves is retargeting a running archiver at a prefix with no\n"+
					"  base backup in it. Key this branch on Exists, not on Path.", live.Path, got.Tsdb)
			}
			if !slices.Contains(got.AlreadyLive, TsdbClusterName) {
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
	got := resolveArchivePaths(clusterArchiveState{}, RestorePlan{TsdbFrom: TsdbClusterName}, "", testNow)

	if got.Tsdb == "" {
		t.Fatal("restoring into a store with no explicit archive path kept the default, which " +
			"means the recovered cluster archives back over the path it recovered FROM. " +
			"CloudNativePG hangs in `Setting up primary` on that, it does not fail.")
	}
	if got.Tsdb == TsdbClusterName {
		t.Fatalf("the recovered cluster's archive path equals the source %q", TsdbClusterName)
	}
	if !strings.HasPrefix(got.Tsdb, TsdbClusterName+"-restored-") {
		t.Errorf("archive path %q is not recognisable as a restore of %q", got.Tsdb, TsdbClusterName)
	}
}

// Recovering a store from the path it USED to own — the instance is gone, its
// archive is not — must not reuse that path, or the rebuilt cluster archives
// straight back over the WAL it is recovering from.
func TestRestoreFromAnArchiveTheDeadInstanceOwnedTakesAFreshPath(t *testing.T) {
	const owned = "dc-tsdb-2026"
	got := resolveArchivePaths(clusterArchiveState{}, RestorePlan{TsdbFrom: owned}, "", testNow)
	if got.Tsdb == owned {
		t.Fatalf("recovering from %q kept it as the archive path, so the restored cluster "+
			"archives back over the WAL it recovered from — `Setting up primary`, forever", owned)
	}
}

// Re-running the SAME restore (the recovery did not take, the operator tries
// again) must not invent a second path. The Cluster is already there carrying the
// path it took on the first attempt; keeping it is what makes the retry idempotent.
func TestRerunningARestoreKeepsThePathItAlreadyTook(t *testing.T) {
	const taken = "dc-tsdb-restored-20260728T140506Z"
	live := clusterArchiveState{Exists: true, Path: taken}

	got := resolveArchivePaths(live, RestorePlan{TsdbFrom: TsdbClusterName}, "", testNow.Add(time.Hour))
	if got.Tsdb != taken {
		t.Fatalf("re-running the same restore moved the archive path from %q to %q", taken, got.Tsdb)
	}
	if !slices.Contains(got.AlreadyLive, TsdbClusterName) {
		t.Errorf("the Cluster already exists, so this restore will NOT run (spec.bootstrap is "+
			"read at CREATE only) — that has to be reported, and %v does not name it", got.AlreadyLive)
	}
}

// A SECOND disaster restored from the SAME source archive. A plain
// "<source>-restored" would collide with the first restore's path — which is not
// empty, because that cluster archived into it — and wedge exactly the run that is
// trying to recover.
func TestTwoRestoresFromOneSourceTakeDifferentPaths(t *testing.T) {
	gone := clusterArchiveState{} // the store is gone: this is the disaster case
	plan := RestorePlan{TsdbFrom: TsdbClusterName}

	first := resolveArchivePaths(gone, plan, "", testNow)
	second := resolveArchivePaths(gone, plan, "", testNow.Add(time.Second))

	if first.Tsdb == second.Tsdb {
		t.Fatalf("two restores from %q both took archive path %q. The first cluster archived "+
			"into it, so the second recovers and then hangs in `Setting up primary` — during "+
			"a recovery.", TsdbClusterName, first.Tsdb)
	}
}

// A fresh ordinary install — nothing there, nothing being restored — settles on the
// fresh, instance-derived path it was handed.
func TestAFreshOrdinaryInstallSettlesOnTheFreshPaths(t *testing.T) {
	got := resolveArchivePaths(clusterArchiveState{}, RestorePlan{}, "dc-tsdb-planted", testNow)
	if got.Tsdb != "dc-tsdb-planted" {
		t.Fatalf("a fresh install with no restore must take the fresh path; got %q", got.Tsdb)
	}
	if len(got.AlreadyLive) != 0 {
		t.Errorf("no store exists and none is being restored; got %v", got.AlreadyLive)
	}
}

// 🔴 A NEW EVENT STORE MUST NOT ARCHIVE WHERE ANOTHER ONE DID. Two instances share the
// bucket, and a rebuilt instance meets the archive its previous generation left behind;
// either one makes CloudNativePG wait forever on "Expected empty archive".
func TestAFreshEventStoreArchivesUnderItsInstanceAndGeneration(t *testing.T) {
	a := freshTsdbArchivePath("alpha", "11111111-aaaa-4000-8000-000000000000")
	b := freshTsdbArchivePath("beta", "11111111-aaaa-4000-8000-000000000000")
	rebuilt := freshTsdbArchivePath("alpha", "22222222-bbbb-4000-8000-000000000000")
	if a != "dc-tsdb-alpha-11111111" {
		t.Errorf("got %q, want dc-tsdb-alpha-11111111", a)
	}
	if a == b || a == rebuilt {
		t.Errorf("paths must differ by instance and by generation: %q %q %q", a, b, rebuilt)
	}
	if got := freshTsdbArchivePath("alpha", ""); got != "dc-tsdb-alpha" {
		t.Errorf("with no declaration (dry run) got %q, want dc-tsdb-alpha", got)
	}
}

// 🔴 THE REAL RECOVERY MUST NOT BE TOLD IT WILL NOT RUN. AlreadyLive drives a
// warning that says the restore is a no-op and the operator should destroy the
// instance and rebuild it. Firing that on a restore into an EMPTY cluster — the one
// case where the restore genuinely does run — hands an operator mid-incident a
// destroy instruction for the data they have just recovered.
func TestARestoreIntoNothingIsNotReportedAsIneffective(t *testing.T) {
	got := resolveArchivePaths(clusterArchiveState{},
		RestorePlan{TsdbFrom: TsdbClusterName}, "", testNow)
	if len(got.AlreadyLive) != 0 {
		t.Fatalf("the Cluster does not exist, so the restore WILL run; reporting %v tells the "+
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
// the seam every stepRenderConfig test stubs, so which Cluster it reads was checked by
// nothing.
//
// Reading the wrong one is not a mislabel. The instance would be handed that store's
// archive path and emit it as its own backup_server_name, so the next apply retargets
// its archiver at a WAL prefix holding no base backup of the database writing to it:
// a store restorable to nothing, on a green apply.
func TestReadArchiveStateReadsTheInstancesEventStore(t *testing.T) {
	archiver := func(serverName string) map[string]any {
		return map[string]any{
			"name":          barmanPluginName,
			"isWALArchiver": true,
			"parameters":    map[string]any{"serverName": serverName},
		}
	}
	// The event store in the instance's namespace, beside the cluster's relational store
	// and a DECOY event store left in the cluster namespace, where it lived before
	// instances had namespaces. Reading the wrong namespace or name reads a decoy.
	tsdb := cnpgCluster(TsdbClusterName, archiver("tsdb-owns-this"))
	tsdb.SetNamespace(InstanceNamespace("acme"))
	dyn := fakeDyn(
		cnpgCluster(RdbClusterName, archiver("rdb-owns-this")),
		tsdb,
		cnpgCluster(TsdbClusterName, archiver("a-decoy-in-the-cluster-namespace")),
	)

	got, err := readArchiveState(context.Background(), dyn, "acme")
	if err != nil {
		t.Fatal(err)
	}
	if !got.Exists || got.Path != "tsdb-owns-this" {
		t.Fatalf("the instance's event store was read as %+v, want its own archive path.\n"+
			"  The instance then emits another store's archive path, and the next apply\n"+
			"  points its WAL archiver at a prefix with no base backup of it — silently,\n"+
			"  on a green apply.", got)
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
	credentialsBefore := reflect.ValueOf(settleCredentials).Pointer()
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
	// ~/.devicechain/instances/prod on whoever runs the suite. The write is the dangerous half:
	// measured before the stub existed, one `go test ./...` created that directory and
	// left a real credential record in it, which a later test in the same run then
	// reused. On a machine that has a prod instance, the suite would have overwritten
	// the bridge copy that instance depends on.
	if reflect.ValueOf(readDeployedBrokerRecord).Pointer() == recordReadBefore {
		t.Fatal("withDeployedInstance no longer stubs readDeployedBrokerRecord, so every test " +
			"driving stepRenderConfig now reads ~/.devicechain/instances/<instance> on the machine running " +
			"the suite. Restore the withBrokerRecord call in withDeployedInstance.")
	}
	if reflect.ValueOf(storeBrokerRecord).Pointer() == recordStoreBefore {
		t.Fatal("withDeployedInstance no longer stubs storeBrokerRecord, so every test driving " +
			"stepRenderConfig now WRITES a credential record into ~/.devicechain/instances/<instance> on " +
			"the machine running the suite. Restore the withBrokerRecord call in " +
			"withDeployedInstance.")
	}
	// 🔴 THE SIXTH FAILS LOUDLY RATHER THAN QUIETLY, AND IS STILL WORTH PINNING.
	// Settling the credentials builds a kube client, so an unstubbed one does not
	// read the wrong cluster — it fails to find any, which at least cannot be
	// mistaken for an answer. It is here because the NEXT person to add a read may
	// not be so lucky, and because a helper that covers five of six reads teaches
	// that the helper is the place to look.
	if reflect.ValueOf(settleCredentials).Pointer() == credentialsBefore {
		t.Fatal("withDeployedInstance no longer stubs settleCredentials, so every test driving " +
			"stepRenderConfig now needs a reachable cluster to settle this instance's " +
			"credentials. Restore the withSettledCredentials call in withDeployedInstance.")
	}
}

// EVERY TEST ABOVE CALLS resolveArchivePaths DIRECTLY, so all of them stay green
// on a pipeline that never calls it. This is the one that drives the real step and
// reads the values the OpenTofu apply will actually consume.
//
// It is the same shape as the flagless-rerun test, run through the whole of
// stepRenderConfig: a live instance whose event store archives under a restored path,
// re-bootstrapped with no restore flags.
//
// 🔑 ONLY THE EVENT STORE'S PATH IS A BOOTSTRAP'S. The relational store is the
// cluster's, and `dcctl install` settles its path from the live store — a bootstrap
// applies no cluster root to hand one to.
func TestRenderConfigKeepsTheLiveArchivePath(t *testing.T) {
	const owned = "dc-tsdb-restored-20260728T140506Z"
	withExistingInstance(t, "3q2+796tvu/erb7v3q2+796tvu/erb7v3q0=", nil)
	withArchiveState(t, clusterArchiveState{Exists: true, Path: owned}, nil)

	st := &State{Instance: "prod", InstanceUID: "4f979c6f-0000-4000-8000-000000000000",
		BuildImages: true, Values: map[string]string{}}
	if err := stepRenderConfig(t.Context(), st); err != nil {
		t.Fatal(err)
	}
	if got := st.Values["backupServerNameTsdb"]; got != owned {
		t.Fatalf("stepRenderConfig settled the event store's archive path as %q, want %q — a "+
			"flagless re-run must not move it", got, owned)
	}
	if !slices.Contains(infraVars(st), "backup_server_name_tsdb="+owned) {
		t.Errorf("the settled path never reached OpenTofu: %v", infraVars(st))
	}
}

// 🔴 AND A FRESH EVENT STORE, THROUGH THE REAL STEP, ARCHIVES UNDER ITS INSTANCE AND
// GENERATION. Every instance's event store shares one bucket; a step that stopped
// handing resolveArchivePaths the fresh path would put them all under `dc-tsdb`, and
// the second instance — or a rebuild — would wait forever on "Expected empty archive".
func TestRenderConfigGivesAFreshEventStoreAPathOfItsOwn(t *testing.T) {
	withExistingInstance(t, "3q2+796tvu/erb7v3q2+796tvu/erb7v3q0=", nil)
	withArchiveState(t, clusterArchiveState{}, nil)

	st := &State{Instance: "prod", InstanceUID: "4f979c6f-0000-4000-8000-000000000000",
		BuildImages: true, Values: map[string]string{}}
	if err := stepRenderConfig(t.Context(), st); err != nil {
		t.Fatal(err)
	}
	if got := st.Values["backupServerNameTsdb"]; got != "dc-tsdb-prod-4f979c6f" {
		t.Errorf("a fresh event store archives under %q, want dc-tsdb-prod-4f979c6f", got)
	}
	if got := st.Values["backupServerNameRdb"]; got != "" {
		t.Errorf("the relational store is the cluster's and archives under its own name; got %q", got)
	}
	if !slices.Contains(infraVars(st), "backup_server_name_tsdb=dc-tsdb-prod-4f979c6f") {
		t.Errorf("the fresh event-store path never reached OpenTofu: %v", infraVars(st))
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
		Restore:     RestorePlan{TsdbFrom: TsdbClusterName, TsdbTargetTime: "2026-07-28T12:00:00Z"},
		Values:      map[string]string{},
	}
	if err := stepRenderConfig(t.Context(), st); err != nil {
		t.Fatal(err)
	}
	got := st.Values["backupServerNameTsdb"]
	if got == "" || got == TsdbClusterName {
		t.Fatalf("recovering into an empty cluster settled on archive path %q; it must differ "+
			"from the source %q", got, TsdbClusterName)
	}
	vars := infraVars(st)
	for _, want := range []string{
		"restore_tsdb_from=" + TsdbClusterName,
		"restore_tsdb_target_time=2026-07-28T12:00:00Z",
		"backup_server_name_tsdb=" + got,
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
	f := RestoreFlags{TsdbTargetTime: "2026-07-28T12:00:00Z", BackupsEnabled: true}
	if _, err := ResolveRestorePlan(f); err == nil {
		t.Fatal("a recovery target with nothing to recover was accepted. The whole point " +
			"of the target is to stop replay before a known-bad moment; ignoring it " +
			"silently gives a full-archive restore and says nothing.")
	}
}

// The plugin that WRITES the archive is the plugin that READS it, so the flags
// that switch the backup destination off switch restore off with it. Refused here
// rather than at OpenTofu plan time, where the message tells the operator to set a
// variable dcctl does not expose.
func TestResolveRestorePlanRefusesARestoreWithNoBackupPlugin(t *testing.T) {
	_, err := ResolveRestorePlan(RestoreFlags{TsdbFrom: "dc-tsdb", BackupsEnabled: false})
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
				TsdbFrom: "dc-tsdb", TsdbTargetTime: target, BackupsEnabled: true,
			})
			if err == nil {
				t.Fatalf("--restore-tsdb-at %q was accepted; without an explicit offset the "+
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
			TsdbFrom: "dc-tsdb", TsdbTargetTime: target, BackupsEnabled: true,
		}); err != nil {
			t.Errorf("%s was rejected: %v", target, err)
		}
	}
}

func TestResolveRestorePlanPassesAValidRestoreThrough(t *testing.T) {
	f := RestoreFlags{
		TsdbFrom: "dc-tsdb", TsdbTargetTime: "2026-07-28T12:00:00Z", BackupsEnabled: true,
	}
	plan, err := ResolveRestorePlan(f)
	if err != nil {
		t.Fatal(err)
	}
	if plan != (RestorePlan{TsdbFrom: "dc-tsdb", TsdbTargetTime: "2026-07-28T12:00:00Z"}) {
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

// The restore refusal is decided from BackupsEnabledFor, and OpenTofu provisions the
// plugin from what infraVars emits. Where they disagree, dcctl either accepts a
// restore with no plugin to perform it — and the operator finds out during the
// rebuild — or refuses one it could have done.
//
// 🔴 ON A BOOTSTRAP BOTH MUST FOLLOW THE INSTALL RECORD, so every record here says the
// opposite of what this State's own flags would. An answer read from the flags fails.
func TestDatabaseBackupsEnabledMatchesWhatIsEmitted(t *testing.T) {
	const off = "enable_database_backups=false"
	agreed := 0
	for _, noCNPG := range []bool{false, true} {
		for _, compact := range []bool{false, true} {
			for _, noTLS := range []bool{false, true} {
				st := compactState(false)
				st.NoCNPG, st.Compact, st.NoTLS = noCNPG, compact, noTLS
				recorded := !DatabaseBackupsEnabled(noCNPG, compact, noTLS)
				st.Install = &InstallRecord{Settings: InstallSettings{DatabaseBackups: recorded}}

				emittedOn := !slices.Contains(infraVars(st), off)
				_, err := ResolveRestorePlan(RestoreFlags{TsdbFrom: "dc-tsdb", BackupsEnabled: BackupsEnabledFor(st)})
				if emittedOn != recorded || (err == nil) != recorded {
					t.Errorf("--no-cnpg=%v --compact=%v --no-tls=%v with backups recorded %v: infraVars "+
						"emits backups=%v and the restore refusal is %v; both must follow the record",
						noCNPG, compact, noTLS, recorded, emittedOn, err)
					continue
				}
				agreed++
			}
		}
	}
	if agreed != 8 {
		t.Fatalf("the matrix agreed on %d of 8 combinations", agreed)
	}
}

// ---------------------------------------------------------------------------
// Emission
// ---------------------------------------------------------------------------

func TestInfraVarsCarriesTheRestoreAndTheArchivePaths(t *testing.T) {
	st := compactState(false)
	st.Restore = RestorePlan{TsdbFrom: "dc-tsdb", TsdbTargetTime: "2026-07-28T13:00:00Z"}
	st.Values["backupServerNameRdb"] = "dc-rdb-restored-20260728T140506Z"
	st.Values["backupServerNameTsdb"] = "dc-tsdb-restored-20260728T140506Z"

	vars := infraVars(st)
	for _, want := range []string{
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

// ---------------------------------------------------------------------------
// The RELATIONAL store — `dcctl install`'s half
// ---------------------------------------------------------------------------

// The disaster the relational half exists for: the cluster is gone, its archive is
// not. The recovered store must archive somewhere OTHER than the path it read, or
// CloudNativePG stops it on `Expected empty archive` and it sits in `Setting up
// primary` forever — during a recovery, with nothing failing.
//
// 🔴 THIS IS WHAT MAKES terraform_data.restore_guard UNREACHABLE FROM dcctl. That
// precondition refuses backup_server_name_rdb equal to (or unset alongside)
// restore_rdb_from. dcctl must never ask for that combination in the first place; the
// guard is a net under someone driving OpenTofu by hand, not a step in this path.
func TestRestoringTheRelationalStoreIntoNothingTakesAFreshPath(t *testing.T) {
	plan := RestorePlan{RdbFrom: RdbClusterName}

	got, ineffective := resolveRdbArchivePath(clusterArchiveState{}, plan, testNow)

	if got == "" {
		t.Fatal("the recovered relational store kept the OpenTofu default, which means it " +
			"archives back over the archive it recovered FROM. CloudNativePG hangs in " +
			"`Setting up primary` on that, it does not fail — and the root's restore_guard " +
			"would refuse the apply, which is a refusal dcctl should never provoke.")
	}
	if got == RdbClusterName {
		t.Fatalf("the recovered store's archive path equals the source %q", RdbClusterName)
	}
	if !strings.HasPrefix(got, RdbClusterName+"-restored-") {
		t.Errorf("archive path %q is not recognisable as a restore of %q", got, RdbClusterName)
	}
	if ineffective {
		t.Error("the relational store does not exist, so this restore WILL run; reporting it " +
			"as ineffective tells an operator mid-incident that the recovery they are " +
			"performing is a no-op")
	}
}

// A second disaster recovered from the SAME source. A plain "<source>-restored" would
// collide with the first recovery's path — not empty, because that cluster archived
// into it — and wedge the very run that is trying to recover.
func TestTwoRelationalRestoresFromOneSourceTakeDifferentPaths(t *testing.T) {
	gone := clusterArchiveState{}
	plan := RestorePlan{RdbFrom: RdbClusterName}

	first, _ := resolveRdbArchivePath(gone, plan, testNow)
	second, _ := resolveRdbArchivePath(gone, plan, testNow.Add(time.Second))

	if first == second {
		t.Fatalf("two recoveries from %q both took archive path %q; the second one wedges",
			RdbClusterName, first)
	}
}

// 🔴 THE SAME DEFECT THE EVENT HALF SHIPPED WITH, asserted for the relational half
// before it can ship it too.
//
// An ordinary install renders no serverName, so the live path is "" while the store is
// very much alive and archiving under its own name. A restore naming that very archive
// — the obvious wrong guess a half-followed runbook produces — must NOT be allowed to
// derive a fresh path: the restore cannot run anyway (spec.bootstrap is CREATE-only),
// so the only thing a new path could achieve is retargeting a LIVE archiver at a
// prefix with no base backup in it.
func TestARelationalRestoreAimedAtALiveStoreNeverMovesItsArchive(t *testing.T) {
	for name, live := range map[string]clusterArchiveState{
		"archiving under its own name": {Exists: true, Path: ""},
		"archiving under a set path":   {Exists: true, Path: "dc-rdb-2026"},
	} {
		t.Run(name, func(t *testing.T) {
			got, ineffective := resolveRdbArchivePath(live, RestorePlan{RdbFrom: RdbClusterName}, testNow)
			if got != live.Path {
				t.Fatalf("a restore aimed at a LIVE relational store moved its archive path "+
					"from %q to %q. Every instance on the cluster writes to that store; the "+
					"prefix holding its base backup would stop receiving WAL, on a green apply.",
					live.Path, got)
			}
			if !ineffective {
				t.Error("the store already exists, so the restore will NOT run — that has to " +
					"be reported, because a green apply that restored nothing and a green " +
					"apply that restored everything look identical")
			}
		})
	}
}

// A re-install that carries the same restore flags — the legitimate retry after an
// install died part-way — must not invent a second archive path for a store that
// already took one.
func TestRerunningARelationalRestoreKeepsThePathItAlreadyTook(t *testing.T) {
	const taken = "dc-rdb-restored-20260728T140506Z"
	got, _ := resolveRdbArchivePath(clusterArchiveState{Exists: true, Path: taken},
		RestorePlan{RdbFrom: RdbClusterName}, testNow.Add(time.Hour))
	if got != taken {
		t.Fatalf("a retry moved the relational archive path from %q to %q", taken, got)
	}
}

// 🔴 THE FRESH PATH IS THE OPENTOFU DEFAULT, AND THAT IS NOT AN OVERSIGHT. Empty means
// "archive under the Cluster's own name", which is unambiguous for the relational store
// because a cluster has exactly one, in a bucket of its own. Deriving an instance-shaped
// path here would move the archive of every ordinary install that has ever run.
func TestAFreshRelationalStoreArchivesUnderItsOwnName(t *testing.T) {
	got, ineffective := resolveRdbArchivePath(clusterArchiveState{}, RestorePlan{}, testNow)
	if got != "" {
		t.Fatalf("a fresh install must leave the relational archive path at the root's "+
			"default; got %q, which infraVars would emit as backup_server_name_rdb", got)
	}
	if ineffective {
		t.Error("nothing is being restored; nothing can be an ineffective restore")
	}
}

// ---------------------------------------------------------------------------
// What the operator is told
// ---------------------------------------------------------------------------

// 🔴 THE HAZARD THIS FUNCTION EXISTS TO CLOSE. A rehearsal that reads back from an
// apply it never runs describes the zero value and calls it the answer — this project
// shipped a --dry-run printing "Backups: NONE" for a cluster that archives. Both of
// describeRelationalRestore's inputs are available before anything is applied, so the
// sentence a rehearsal prints is the sentence the real run prints.
func TestTheRestoreDescriptionNamesTheSourceAndTheFreshPath(t *testing.T) {
	plan := RestorePlan{RdbFrom: "dc-rdb", RdbTargetTime: "2026-07-28T12:00:00Z"}
	lines := describeRelationalRestore(plan, clusterArchiveState{}, testNow)
	if len(lines) == 0 {
		t.Fatal("a restore about to be performed was described by nothing at all")
	}
	path, _ := resolveRdbArchivePath(clusterArchiveState{}, plan, testNow)
	for _, want := range []string{"dc-rdb", path, "2026-07-28T12:00:00Z"} {
		if !strings.Contains(lines[0], want) {
			t.Errorf("the description does not mention %q, so a rehearsal cannot be checked "+
				"against it: %q", want, lines[0])
		}
	}
	if path == "" || path == plan.RdbFrom {
		t.Fatalf("the described archive path %q is the source or empty — the description is "+
			"accurate about a plan that would wedge", path)
	}

	// 🔴 AND THE HALF THE INSTALL CANNOT ENFORCE. Before the install/bootstrap split
	// dcctl REFUSED a relational restore without --restore-root-key, because the
	// recovered rows hold secrets sealed by a key no database backup contains. An
	// install has no instance and no artifact to check against, so the refusal is
	// gone; saying it is what is left, and this is the moment the operator is
	// thinking about the recovery.
	joined := strings.Join(lines, "\n")
	for _, want := range []string{"--restore-root-key", "permanently unreadable"} {
		if !strings.Contains(joined, want) {
			t.Errorf("an operator recovering the relational store is never told about %q. "+
				"An instance rebuilt without it comes up CLEAN and every recovered secret "+
				"is gone: %s", want, joined)
		}
	}
}

// An ordinary install must say nothing about recovery at all. A line that appears
// unconditionally is a line nobody reads when it matters.
func TestAnOrdinaryInstallIsToldNothingAboutRestores(t *testing.T) {
	if lines := describeRelationalRestore(RestorePlan{}, clusterArchiveState{}, testNow); lines != nil {
		t.Fatalf("an ordinary install was told about a restore it is not performing: %v", lines)
	}
	// ...including one aimed at the event store, which this command does not own.
	if lines := describeRelationalRestore(RestorePlan{TsdbFrom: "dc-tsdb"}, clusterArchiveState{}, testNow); lines != nil {
		t.Fatalf("dcctl install described an EVENT store restore: %v", lines)
	}
}

// The no-op, said out loud. "It restored" and "it declined to restore" are
// indistinguishable from the outside — green apply, no data moved — so the one case
// where the operator must be interrupted is this one.
func TestALiveRelationalStoreIsReportedAsARestoreThatWillNotRun(t *testing.T) {
	lines := describeRelationalRestore(RestorePlan{RdbFrom: "dc-rdb"},
		clusterArchiveState{Exists: true, Path: "dc-rdb-2026"}, testNow)
	if len(lines) != 1 {
		t.Fatalf("want one line, got %v", lines)
	}
	for _, want := range []string{"will NOT run", "spec.bootstrap", RdbClusterName} {
		if !strings.Contains(lines[0], want) {
			t.Errorf("the warning does not mention %q: %q", want, lines[0])
		}
	}
	// And it must not also claim a fresh archive path, which would read as a plan.
	if strings.Contains(lines[0], "-restored-") {
		t.Errorf("the warning describes an archive path for a restore that will not happen: %q", lines[0])
	}
}

// ---------------------------------------------------------------------------
// Which store a plan is about
// ---------------------------------------------------------------------------

// Active gained a second store, and two of its callers are about an INSTANCE: the
// declaration's `Restored` field and the rebuild carve-out in rebuildRefusalReason.
// A relational recovery is not an answer to either question, which is why they ask
// RestoresEventStore instead.
func TestAPlanKnowsWhichStoreItIsAbout(t *testing.T) {
	for _, tc := range []struct {
		what                      string
		plan                      RestorePlan
		active, event, relational bool
	}{
		{"nothing", RestorePlan{}, false, false, false},
		{"the event store", RestorePlan{TsdbFrom: "dc-tsdb"}, true, true, false},
		{"the relational store", RestorePlan{RdbFrom: "dc-rdb"}, true, false, true},
		{"both", RestorePlan{RdbFrom: "dc-rdb", TsdbFrom: "dc-tsdb"}, true, true, true},
	} {
		t.Run(tc.what, func(t *testing.T) {
			if got := tc.plan.Active(); got != tc.active {
				t.Errorf("Active() = %v, want %v", got, tc.active)
			}
			if got := tc.plan.RestoresEventStore(); got != tc.event {
				t.Errorf("RestoresEventStore() = %v, want %v", got, tc.event)
			}
			if got := tc.plan.RestoresRelationalStore(); got != tc.relational {
				t.Errorf("RestoresRelationalStore() = %v, want %v", got, tc.relational)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Relational flag validation
// ---------------------------------------------------------------------------

// Every rule the event store's flags obey, asserted for the relational store's — the
// checks are walked once per store rather than written twice precisely so this table
// cannot drift away from the other one.
func TestResolveRestorePlanAppliesTheSameRulesToTheRelationalStore(t *testing.T) {
	for _, tc := range []struct {
		what    string
		flags   RestoreFlags
		wantErr string
	}{
		{
			"a recovery target with nothing to recover",
			RestoreFlags{RdbTargetTime: "2026-07-28T12:00:00Z", BackupsEnabled: true},
			"--restore-rdb-from",
		},
		{
			// The one that succeeds and is wrong: PostgreSQL reads an offsetless
			// timestamp in the RECOVERING server's timezone, stops hours from the
			// named moment, and reports success.
			"a target time with no offset",
			RestoreFlags{RdbFrom: "dc-rdb", RdbTargetTime: "2026-07-27 13:59:00", BackupsEnabled: true},
			"RFC3339",
		},
		{
			"a cluster with no backup plugin to read the archive",
			RestoreFlags{RdbFrom: "dc-rdb"},
			"--restore-rdb-from",
		},
	} {
		t.Run(tc.what, func(t *testing.T) {
			_, err := ResolveRestorePlan(tc.flags)
			if err == nil {
				t.Fatalf("%+v was accepted", tc.flags)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("the refusal does not mention %q: %v", tc.wantErr, err)
			}
			// 🔴 AND IT MUST NOT BLAME THE OTHER STORE'S FLAG. An operator running
			// `dcctl install` has no --restore-tsdb-from to drop, so a refusal naming
			// it reads as a bug in dcctl rather than as an answer — during an incident.
			if strings.Contains(err.Error(), "--restore-tsdb") {
				t.Errorf("the refusal blames a flag this command does not have: %v", err)
			}
		})
	}
}

// The counterweight: refusing the wrong combinations is only worth anything while a
// real recovery still passes through untouched.
func TestResolveRestorePlanPassesAValidRelationalRestoreThrough(t *testing.T) {
	plan, err := ResolveRestorePlan(RestoreFlags{
		RdbFrom: "dc-rdb", RdbTargetTime: "2026-07-28T12:00:00Z", BackupsEnabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if plan != (RestorePlan{RdbFrom: "dc-rdb", RdbTargetTime: "2026-07-28T12:00:00Z"}) {
		t.Fatalf("the plan did not carry the flags through, or carried them into the wrong "+
			"store's fields: %+v", plan)
	}
}

// ---------------------------------------------------------------------------
// Emission and routing
// ---------------------------------------------------------------------------

// The relational restore has to reach OpenTofu under the names the CLUSTER root
// declares, and reach the CLUSTER root specifically.
//
// 🔴 ROUTING IS THE HALF A NAMING TEST CANNOT SEE. splitVars sends each variable to
// the root that declares it; a relational restore that reached the INSTANCE apply
// would be refused as an undeclared variable, and one that reached NEITHER would be
// dropped with the apply silently taking the root's default — an empty database
// where a recovery was asked for.
func TestTheRelationalRestoreReachesTheClusterRootOnly(t *testing.T) {
	st := compactState(false)
	st.Restore = RestorePlan{RdbFrom: "dc-rdb", RdbTargetTime: "2026-07-28T13:00:00Z"}
	st.Values["backupServerNameRdb"] = "dc-rdb-restored-20260728T140506Z"

	cluster, instance, err := splitVars(infraVars(st))
	if err != nil {
		t.Fatalf("dcctl passes a variable no OpenTofu root declares: %v", err)
	}
	for _, want := range []string{
		"restore_rdb_from=dc-rdb",
		"restore_rdb_target_time=2026-07-28T13:00:00Z",
		"backup_server_name_rdb=dc-rdb-restored-20260728T140506Z",
	} {
		if !slices.Contains(cluster, want) {
			t.Errorf("%q never reached the cluster root: %v", want, cluster)
		}
		if slices.Contains(instance, want) {
			t.Errorf("%q reached the INSTANCE root, which does not own the relational "+
				"store: %v", want, instance)
		}
	}
}

// The mirror image: the event store's restore is an instance's, and must not be
// applied by the command that owns the cluster.
func TestTheEventRestoreReachesTheInstanceRootOnly(t *testing.T) {
	st := compactState(false)
	st.Restore = RestorePlan{TsdbFrom: "dc-tsdb"}

	cluster, instance, err := splitVars(infraVars(st))
	if err != nil {
		t.Fatal(err)
	}
	const want = "restore_tsdb_from=dc-tsdb"
	if !slices.Contains(instance, want) {
		t.Errorf("%q never reached the instance root: %v", want, instance)
	}
	if slices.Contains(cluster, want) {
		t.Errorf("%q reached the CLUSTER root: %v", want, cluster)
	}
}

// ---------------------------------------------------------------------------
// The rehearsal
// ---------------------------------------------------------------------------

// withRdbArchiveState stubs the seam a rehearsal reads the relational store through.
func withRdbArchiveState(t *testing.T, state clusterArchiveState, err error) *int {
	t.Helper()
	prev := readRdbArchiveState
	t.Cleanup(func() { readRdbArchiveState = prev })
	calls := 0
	readRdbArchiveState = func(context.Context, string) (clusterArchiveState, error) {
		calls++
		return state, err
	}
	return &calls
}

// 🔴 THE HAZARD, NAMED: a rehearsal that reads back from an apply it never runs
// predicts nothing. This project shipped a --dry-run printing "Backups: NONE" for a
// cluster that archives, because the value it read is only set by an apply.
//
// So the rehearsal READS, exactly as the real run does, and the most valuable thing it
// can say is the one that costs the most to learn late: this recovery will do nothing,
// because CloudNativePG reads spec.bootstrap only when it CREATES a cluster.
func TestARehearsalOfARestoreReadsTheLiveStoreAndSaysWhatWillHappen(t *testing.T) {
	for _, tc := range []struct {
		what string
		live clusterArchiveState
		want string
	}{
		{"the store is gone (the recovery runs)", clusterArchiveState{}, "recovering from archive"},
		{"the store is live (the recovery is a no-op)", clusterArchiveState{Exists: true}, "will NOT run"},
	} {
		t.Run(tc.what, func(t *testing.T) {
			calls := withRdbArchiveState(t, tc.live, nil)
			st := &State{KubeContext: "kind-dc", DryRun: true,
				Restore: RestorePlan{RdbFrom: RdbClusterName}, Values: map[string]string{}}

			lines := describeRelationalRestore(st.Restore, dryRunRdbArchiveState(t.Context(), st), testNow)

			if *calls != 1 {
				t.Fatalf("the rehearsal read the live store %d times, want 1. Predicting from "+
					"the flags alone is how a rehearsal ends up describing a different run "+
					"than the one it is rehearsing.", *calls)
			}
			if len(lines) == 0 || !strings.Contains(strings.Join(lines, "\n"), tc.want) {
				t.Fatalf("the rehearsal never said %q: %v", tc.want, lines)
			}
		})
	}
}

// An ordinary rehearsal must not go near the API server for an answer it has no use
// for. A dry run is routinely aimed at a cluster that does not exist yet, and the
// reachable failure is a scary line about the relational store in a plan that never
// mentions one.
func TestARehearsalWithNoRestoreReadsNothing(t *testing.T) {
	calls := withRdbArchiveState(t, clusterArchiveState{Exists: true, Path: "surprise"}, nil)
	st := &State{KubeContext: "kind-dc", DryRun: true, Values: map[string]string{}}

	if got := dryRunRdbArchiveState(t.Context(), st); got != (clusterArchiveState{}) {
		t.Errorf("an ordinary rehearsal read an archive state it has no use for: %+v", got)
	}
	if *calls != 0 {
		t.Errorf("an ordinary rehearsal contacted the cluster %d time(s)", *calls)
	}
}

// 🔴 AND WHEN THE READ FAILS, the rehearsal assumes the store is NOT there — which is
// the opposite of what the real run does, and is right only because nothing is applied.
// The real run fails on this read; acting on a wrong answer there moves a live
// archiver. Saying so is what keeps the two apart.
func TestAnUnreadableStoreLeavesTheRehearsalPredictingAFreshRestore(t *testing.T) {
	withRdbArchiveState(t, clusterArchiveState{}, errors.New("the API server is not answering"))
	st := &State{KubeContext: "kind-dc", DryRun: true,
		Restore: RestorePlan{RdbFrom: RdbClusterName}, Values: map[string]string{}}

	if got := dryRunRdbArchiveState(t.Context(), st); got != (clusterArchiveState{}) {
		t.Fatalf("an unreadable store was not read as absent in a rehearsal: %+v", got)
	}
}
