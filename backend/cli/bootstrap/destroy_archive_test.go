// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"helm.sh/helm/v3/pkg/release"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

// ---------------------------------------------------------------------------
// An in-memory object store
// ---------------------------------------------------------------------------

type fakeArchiveStore struct {
	mu      sync.Mutex
	objects map[string]map[string]int64 // bucket → key → size
	// deleteNoop makes Delete report success and remove nothing.
	deleteNoop bool
	deleteErr  error
	deletes    [][]string
	onDelete   func(keys []string)
}

func newFakeArchiveStore(buckets map[string][]string) *fakeArchiveStore {
	s := &fakeArchiveStore{objects: map[string]map[string]int64{}}
	for b, keys := range buckets {
		s.objects[b] = map[string]int64{}
		for _, k := range keys {
			s.objects[b][k] = 1 << 20
		}
	}
	return s
}

func (s *fakeArchiveStore) keys(bucket string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for k := range s.objects[bucket] {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func (s *fakeArchiveStore) List(_ context.Context, bucket, prefix string) ([]archiveObject, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []archiveObject
	for k, n := range s.objects[bucket] {
		if strings.HasPrefix(k, prefix) {
			out = append(out, archiveObject{Key: k, Size: n})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out, nil
}

func (s *fakeArchiveStore) ListPrefixes(_ context.Context, bucket, prefix string) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	seen := map[string]bool{}
	for k := range s.objects[bucket] {
		if rest, ok := strings.CutPrefix(k, prefix); ok {
			if i := strings.Index(rest, "/"); i >= 0 {
				seen[prefix+rest[:i+1]] = true
			}
		}
	}
	var out []string
	for p := range seen {
		out = append(out, p)
	}
	sort.Strings(out)
	return out, nil
}

func (s *fakeArchiveStore) Delete(_ context.Context, bucket string, keys []string) error {
	if s.onDelete != nil {
		s.onDelete(keys)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.deletes = append(s.deletes, keys)
	if s.deleteErr != nil {
		return s.deleteErr
	}
	if !s.deleteNoop {
		for _, k := range keys {
			delete(s.objects[bucket], k)
		}
	}
	return nil
}

// withArchiveStore hands destroy the given store (or openErr) and counts the opens.
func withArchiveStore(t *testing.T, store archiveStore, openErr error) *int {
	t.Helper()
	opens := 0
	orig := openInClusterArchiveStore
	t.Cleanup(func() { openInClusterArchiveStore = orig })
	openInClusterArchiveStore = func(context.Context, string, kubernetes.Interface, archivePlan) (archiveStore, func(), error) {
		opens++
		if openErr != nil {
			return nil, nil, openErr
		}
		return store, func() {}, nil
	}
	return &opens
}

// acmeArchive is a bucket holding instance acme's archive and every path around it that
// must survive acme's destroy.
func acmeArchive() *fakeArchiveStore {
	return newFakeArchiveStore(map[string][]string{
		testArchiveBucket: {
			testArchivePath + "/base/20260928T030000/data.tar.gz",
			testArchivePath + "/wals/0000000100000000/000000010000000000000001.gz",
			testArchivePath + "/wals/0000000100000000/000000010000000000000002.gz",
			// A later restore of this very instance: a prefix of it WITHOUT the slash.
			testArchivePath + "-restored-20260928T000000Z/wals/x",
			// Another instance whose name starts with this one's.
			"dc-tsdb-acme2-9f9f9f9f/wals/x",
			// An earlier generation of acme, left by an earlier dcctl.
			"dc-tsdb-acme-0badc0de/wals/x",
		},
		// The relational store's archive: the cluster's, never an instance's.
		"devicechain-rdb": {"dc-rdb/wals/x"},
	})
}

func acmeArchiveSurvivors() []string {
	return []string{"dc-tsdb-acme-0badc0de/wals/x", testArchivePath + "-restored-20260928T000000Z/wals/x",
		"dc-tsdb-acme2-9f9f9f9f/wals/x"}
}

const inClusterEndpoint = "http://dc-object-store.dc-system:9000"

func (r *teardownRig) destroyOpts(t *testing.T, opts DestroyOptions) (string, error) {
	t.Helper()
	p := &fakeProvider{name: "local", present: map[string]bool{"c": true}}
	opts.Instance, opts.AssumeYes = "acme", true
	var err error
	out := captureOutput(t, func() { err = Destroy(context.Background(), p, opts) })
	return out, err
}

// archiveRig is a teardown rig whose cluster was installed with in-cluster backups and
// whose instance acme is archiving under testArchivePath.
func archiveRig(t *testing.T, serverName string) *teardownRig {
	t.Helper()
	r := newTeardownRig(t, []*release.Release{deviceChainRelease(helmReleaseNameFor("acme"), "acme")},
		[]string{"acme"}, "acme")
	r.withInstallRecord(t, false)
	r.withEventStore(t, archivingEventStore(serverName, inClusterEndpoint)...)
	return r
}

// ---------------------------------------------------------------------------
// Through Destroy
// ---------------------------------------------------------------------------

// 🔴 EXACTLY THE INSTANCE'S PATH GOES, AND NOTHING NEXT TO IT. Every path in this bucket
// that is not acme's live archive is a negative control: a later restore of acme (its path
// STARTS with acme's), an instance whose name starts with acme's, an earlier generation
// of acme, and the relational store's archive in its own bucket.
func TestDestroyRemovesOnlyTheInstancesArchive(t *testing.T) {
	r := archiveRig(t, testArchivePath)
	store := acmeArchive()
	opens := withArchiveStore(t, store, nil)

	out, err := r.destroy(t, false)
	if err != nil {
		t.Fatalf("destroy failed: %v\n%s", err, out)
	}
	if *opens != 1 {
		t.Fatalf("the store was opened %d times, want 1\n%s", *opens, out)
	}
	if got, want := store.keys(testArchiveBucket), acmeArchiveSurvivors(); !slices.Equal(got, want) {
		t.Errorf("left in %s:\n  %s\nwant exactly:\n  %s", testArchiveBucket, strings.Join(got, "\n  "), strings.Join(want, "\n  "))
	}
	if got := store.keys("devicechain-rdb"); !slices.Equal(got, []string{"dc-rdb/wals/x"}) {
		t.Errorf("the relational store's archive was touched: %q", got)
	}
	if !strings.Contains(out, "removed 3 object(s) (3.0 MiB) from s3://"+testArchiveBucket+"/"+testArchivePath+"/") {
		t.Errorf("the transcript does not say what was removed:\n%s", out)
	}
	// Earlier generations are LISTED, not removed — and only paths of this name.
	for _, p := range []string{"dc-tsdb-acme-0badc0de/", testArchivePath + "-restored-20260928T000000Z/"} {
		if !strings.Contains(out, "s3://"+testArchiveBucket+"/"+p) {
			t.Errorf("the earlier archive %s was not named as left in place:\n%s", p, out)
		}
	}
	if strings.Contains(out, "acme2") {
		t.Errorf("another instance's archive was named as one of acme's:\n%s", out)
	}
	if !strings.Contains(out, `Instance "acme" destroyed;`) {
		t.Errorf("a destroy that removed everything did not close as destroyed:\n%s", out)
	}
}

// 🔴 THE ARCHIVE PATH IS READ BEFORE THE FIRST CHANGE, AND REMOVED ONLY AFTER THE
// NAMESPACE IS GONE AND BEFORE THE LOCAL STATE IS. Read later, the event store is already
// deleted and the path is unknowable; removed earlier, the event store may still be
// archiving into it; removed after the local state, a failure there would lose the record
// a re-run resumes from.
func TestTheArchiveIsReadFirstAndRemovedAfterTheNamespace(t *testing.T) {
	r := archiveRig(t, testArchivePath)
	dyn := r.withEventStore(t, archivingEventStore(testArchivePath, inClusterEndpoint)...)
	dyn.PrependReactor("get", "clusters", func(k8stesting.Action) (bool, runtime.Object, error) {
		r.calls = append(r.calls, "read event store")
		return false, nil, nil
	})
	store := acmeArchive()
	stateAtDelete := true
	store.onDelete = func(keys []string) {
		r.calls = append(r.calls, "remove archive")
		stateAtDelete = !r.stateRemoved()
	}
	withArchiveStore(t, store, nil)

	out, err := r.destroy(t, false)
	if err != nil {
		t.Fatalf("destroy failed: %v\n%s", err, out)
	}
	want := []string{"read event store", "mark destroying acme", "tofu destroy kind-c acme",
		"delete namespace " + InstanceNamespace("acme"), "wait for namespace " + InstanceNamespace("acme"), "remove archive"}
	if !inOrder(r.calls, want) {
		t.Errorf("steps ran as:\n  %s\nwant, in order:\n  %s", strings.Join(r.calls, "\n  "), strings.Join(want, "\n  "))
	}
	if !stateAtDelete {
		t.Error("the local state was removed before the archive")
	}
}

// 🔴 AN OBJECT STORE THE OPERATOR SUPPLIED IS NEVER OPENED. What is in it is the copy that
// outlives the cluster; destroy says where it is and leaves it.
func TestBackupsInAnExternalStoreAreNeverTouched(t *testing.T) {
	r := newTeardownRig(t, []*release.Release{deviceChainRelease(helmReleaseNameFor("acme"), "acme")},
		[]string{"acme"}, "acme")
	r.withInstallRecord(t, true)
	r.withEventStore(t, archivingEventStore(testArchivePath, "https://s3.example.com")...)
	opens := withArchiveStore(t, acmeArchive(), nil)

	out, err := r.destroy(t, false)
	if err != nil {
		t.Fatalf("destroy failed: %v\n%s", err, out)
	}
	if *opens != 0 {
		t.Fatalf("an operator-owned object store was opened %d time(s)\n%s", *opens, out)
	}
	if !strings.Contains(out, "event-store backups kept: s3://"+testArchiveBucket+"/"+testArchivePath+
		"/ at https://s3.example.com is in an object store you own") {
		t.Errorf("the transcript does not say where the kept backups are:\n%s", out)
	}
	if !strings.Contains(out, `Instance "acme" destroyed;`) {
		t.Errorf("keeping an operator's backups is not something left behind, yet the line is not green:\n%s", out)
	}
}

func TestKeepBackupsTouchesNothing(t *testing.T) {
	r := archiveRig(t, testArchivePath)
	opens := withArchiveStore(t, acmeArchive(), nil)

	out, err := r.destroyOpts(t, DestroyOptions{KeepBackups: true})
	if err != nil {
		t.Fatalf("destroy failed: %v\n%s", err, out)
	}
	if *opens != 0 {
		t.Fatalf("--keep-backups opened the store %d time(s)\n%s", *opens, out)
	}
	if !strings.Contains(out, "event-store backups kept (--keep-backups): s3://"+testArchiveBucket+"/"+testArchivePath+"/") {
		t.Errorf("the transcript does not say what was kept:\n%s", out)
	}
	if strings.Contains(out, "will be REMOVED") {
		t.Errorf("--keep-backups still announced a removal:\n%s", out)
	}
}

// 🔴 AN UNREACHABLE STORE DOES NOT FAIL THE DESTROY. Everything else of the instance is
// gone; failing here would keep a local state describing nothing. It says what it left,
// and does not close green.
func TestAnUnreachableStoreDoesNotFailTheDestroy(t *testing.T) {
	r := archiveRig(t, testArchivePath)
	withArchiveStore(t, nil, errors.New("connection refused"))

	out, err := r.destroy(t, false)
	if err != nil {
		t.Fatalf("an unreachable object store failed the destroy: %v\n%s", err, out)
	}
	if !r.stateRemoved() {
		t.Error("an unreachable object store kept the local state")
	}
	want := "Its event-store backups were LEFT in the in-cluster object store at s3://" + testArchiveBucket + "/" +
		testArchivePath + "/: could not reach the in-cluster object store: connection refused"
	if !strings.Contains(out, want) {
		t.Errorf("the closing line does not name the archive and why it was left; want %q in:\n%s", want, out)
	}
}

func TestADeleteThatDidNotTakeIsReported(t *testing.T) {
	r := archiveRig(t, testArchivePath)
	store := acmeArchive()
	store.deleteNoop = true
	withArchiveStore(t, store, nil)

	out, err := r.destroy(t, false)
	if err != nil {
		t.Fatalf("destroy failed: %v\n%s", err, out)
	}
	if !strings.Contains(out, "it still holds 3 object(s) after the delete") {
		t.Errorf("a delete that removed nothing was not caught by the listing after it:\n%s", out)
	}
	if strings.Contains(out, `Instance "acme" destroyed;`) {
		t.Errorf("closed green over an archive that is still there:\n%s", out)
	}
}

func TestAFailedDeleteIsReported(t *testing.T) {
	r := archiveRig(t, testArchivePath)
	store := acmeArchive()
	store.deleteErr = errors.New("deleting " + testArchivePath + "/wals/x: AccessDenied")
	withArchiveStore(t, store, nil)

	out, err := r.destroy(t, false)
	if err != nil {
		t.Fatalf("destroy failed: %v\n%s", err, out)
	}
	if !strings.Contains(out, "deleting it failed: deleting "+testArchivePath+"/wals/x: AccessDenied") {
		t.Errorf("a failed delete was not reported:\n%s", out)
	}
	if strings.Contains(out, `Instance "acme" destroyed;`) {
		t.Errorf("closed green over a failed delete:\n%s", out)
	}
}

// Idempotent: a path already empty is said to be, and nothing is deleted.
func TestAnAlreadyEmptyArchiveIsNothingToRemove(t *testing.T) {
	r := archiveRig(t, testArchivePath)
	store := newFakeArchiveStore(map[string][]string{testArchiveBucket: {"dc-tsdb-other-12345678/wals/x"}})
	withArchiveStore(t, store, nil)

	out, err := r.destroy(t, false)
	if err != nil {
		t.Fatalf("destroy failed: %v\n%s", err, out)
	}
	if len(store.deletes) != 0 {
		t.Errorf("an empty path was deleted from: %q", store.deletes)
	}
	if !strings.Contains(out, "nothing left under s3://"+testArchiveBucket+"/"+testArchivePath+"/") {
		t.Errorf("the transcript does not say the path was already empty:\n%s", out)
	}
	if !strings.Contains(out, `Instance "acme" destroyed;`) {
		t.Errorf("an already-empty archive is not something left behind:\n%s", out)
	}
}

// 🔴 THE OWN-NAME PATH IS NEVER REMOVED: `dc-tsdb/` is what EVERY event store archiving
// under its own name on this cluster would share.
func TestTheOwnNamePathIsNeverRemoved(t *testing.T) {
	r := archiveRig(t, "")
	opens := withArchiveStore(t, acmeArchive(), nil)

	out, err := r.destroy(t, false)
	if err != nil {
		t.Fatalf("destroy failed: %v\n%s", err, out)
	}
	if *opens != 0 {
		t.Fatalf("the store was opened for the shared own-name path\n%s", out)
	}
	if !strings.Contains(out, "archives under its own name (dc-tsdb/)") {
		t.Errorf("the refusal is not said:\n%s", out)
	}
	if strings.Contains(out, `Instance "acme" destroyed;`) {
		t.Errorf("closed green over backups it did not remove:\n%s", out)
	}
}

// 🔴 A NAMESPACE THE DESTROY LEFT ALONE STILL HOLDS A LIVE EVENT STORE. The namespace step
// leaves a namespace that is not labelled as this instance's and reports done, and
// --without-state skips the tofu destroy that would have removed the event store — so the
// steps having run proves nothing about the archive being idle. Its removal is refused.
func TestAnArchiveIsNotRemovedWhileItsEventStoreIsStillThere(t *testing.T) {
	r := newTeardownRig(t, []*release.Release{deviceChainRelease(helmReleaseNameFor("acme"), "acme")}, nil, "acme")
	if err := r.typed.Tracker().Add(&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: InstanceNamespace("acme")}}); err != nil {
		t.Fatal(err)
	}
	r.withInstallRecord(t, false)
	r.withEventStore(t, archivingEventStore(testArchivePath, inClusterEndpoint)...)
	store := acmeArchive()
	opens := withArchiveStore(t, store, nil)

	out, err := r.destroy(t, true)
	if err != nil {
		t.Fatalf("destroy failed: %v\n%s", err, out)
	}
	if !strings.Contains(out, "will be REMOVED once the instance is gone") {
		t.Fatalf("the removal was never planned, so this test proves nothing:\n%s", out)
	}
	if *opens != 0 || len(store.deletes) != 0 {
		t.Fatalf("the archive of a live event store was deleted from (opens %d, deletes %q)\n%s", *opens, store.deletes, out)
	}
	if !strings.Contains(out, "the event store is still there") {
		t.Errorf("the transcript does not say why the archive was left:\n%s", out)
	}
}

// 🔴 A DESTROY THAT DIES AFTER THE EVENT STORE IS GONE STILL FINISHES ON A RE-RUN. The
// path is read from the live event store, which the destroy deletes well before it
// reaches the archive; a re-run would have nothing left to read it from. The namespace
// wait timing out is the ordinary way to die there.
func TestAResumedDestroyRemovesTheArchiveTheFirstRunSettled(t *testing.T) {
	r := archiveRig(t, testArchivePath)
	dyn := r.withEventStore(t, archivingEventStore(testArchivePath, inClusterEndpoint)...)
	stuck := true
	r.typed.PrependReactor("delete", "namespaces", func(a k8stesting.Action) (bool, runtime.Object, error) {
		if !stuck || a.(k8stesting.DeleteAction).GetName() != InstanceNamespace("acme") {
			return false, nil, nil
		}
		// The event store goes; the namespace hangs on a finalizer.
		_ = dyn.Resource(clusterGVR).Namespace(InstanceNamespace("acme")).Delete(context.Background(), TsdbClusterName, metav1.DeleteOptions{})
		return true, nil, nil
	})
	origTimeout, origPoll := namespaceGoneTimeout, namespaceGonePollEach
	t.Cleanup(func() { namespaceGoneTimeout, namespaceGonePollEach = origTimeout, origPoll })
	namespaceGoneTimeout, namespaceGonePollEach = 50*time.Millisecond, 10*time.Millisecond
	store := acmeArchive()
	withArchiveStore(t, store, nil)

	out, err := r.destroy(t, false)
	if err == nil {
		t.Fatalf("the first run was meant to die on the namespace wait:\n%s", out)
	}
	if len(store.deletes) != 0 {
		t.Fatalf("the first run deleted from the store before its namespace was gone\n%s", out)
	}

	stuck = false
	out, err = r.destroy(t, false)
	if err != nil {
		t.Fatalf("the resumed destroy failed: %v\n%s", err, out)
	}
	if got, want := store.keys(testArchiveBucket), acmeArchiveSurvivors(); !slices.Equal(got, want) {
		t.Errorf("the resumed destroy left:\n  %s\nwant exactly:\n  %s\n%s", strings.Join(got, "\n  "), strings.Join(want, "\n  "), out)
	}
}

// ---------------------------------------------------------------------------
// The decision, alone
// ---------------------------------------------------------------------------

func TestPlanArchiveRemoval(t *testing.T) {
	inCluster := func() *InstallRecord {
		rec := aCompleteInstall()
		rec.Phase = installPhaseInstalled
		return &rec
	}
	external := func() *InstallRecord {
		rec := inCluster()
		rec.Settings.BackupsExternal = true
		rec.Outputs.Archive.EndpointURL = "https://s3.example.com"
		return rec
	}
	noBackups := func() *InstallRecord {
		rec := inCluster()
		rec.Settings.DatabaseBackups = false
		return rec
	}
	live := func(path string) clusterArchiveState {
		return clusterArchiveState{Exists: true, Path: path, ObjectStore: "dc-tsdb-backup"}
	}
	store := liveObjectStore{Bucket: testArchiveBucket, Endpoint: inClusterEndpoint}
	recorded := &recordedArchive{Instance: "acme", Bucket: testArchiveBucket, Prefix: testArchivePath + "/", Endpoint: inClusterEndpoint}

	for _, tc := range []struct {
		name   string
		f      archiveFacts
		action archiveAction
		prefix string
		reason string
	}{
		{"the ordinary case", archiveFacts{Record: inCluster(), Live: live(testArchivePath), Store: store},
			archiveRemove, testArchivePath + "/", ""},
		{"a restored instance's own path", archiveFacts{Record: inCluster(),
			Live: live("dc-tsdb-old-0badc0de-restored-20260928T000000Z"), Store: store},
			archiveRemove, "dc-tsdb-old-0badc0de-restored-20260928T000000Z/", ""},
		{"a path from before the UID half", archiveFacts{Record: inCluster(), Live: live("dc-tsdb-acme"), Store: store},
			archiveRemove, "dc-tsdb-acme/", ""},
		{"resumed, from what the first run recorded", archiveFacts{Record: inCluster(), Recorded: recorded},
			archiveRemove, testArchivePath + "/", ""},
		{"--keep-backups", archiveFacts{Keep: true, Record: inCluster(), Live: live(testArchivePath), Store: store},
			archiveKeep, "", "kept (--keep-backups): s3://devicechain-tsdb/dc-tsdb-acme-1a2b3c4d/"},
		{"--keep-backups wins over an unreadable record", archiveFacts{Keep: true, RecordErr: errors.New("x")},
			archiveKeep, "", "kept (--keep-backups)"},
		{"backups off", archiveFacts{Record: noBackups(), Live: live(testArchivePath), Store: store}, archiveNone, "", ""},
		{"external", archiveFacts{Record: external(), Live: live(testArchivePath),
			Store: liveObjectStore{Bucket: testArchiveBucket, Endpoint: "https://s3.example.com"}},
			archiveExternal, "", "in an object store you own"},
		{"external, even with the path unreadable", archiveFacts{Record: external(), LiveErr: errors.New("x")},
			archiveExternal, "", "bucket devicechain-tsdb at https://s3.example.com is in an object store you own"},
		{"the record unreadable (a re-install still applying reads this way)",
			archiveFacts{RecordErr: fmt.Errorf("%w: an install started and did not finish", ErrNotInstalled),
				Live: live(testArchivePath), Store: store},
			archiveUnknown, "", "install record cannot be read"},
		{"the event store unreadable", archiveFacts{Record: inCluster(), LiveErr: errors.New("forbidden")},
			archiveUnknown, "", "could not be read (forbidden)"},
		{"the event store gone and nothing recorded", archiveFacts{Record: inCluster()},
			archiveUnknown, "", "already gone"},
		{"the event store gone and the record corrupt", archiveFacts{Record: inCluster(), RecordedErr: errors.New("bad json")},
			archiveUnknown, "", "cannot be read (bad json)"},
		{"the own-name path", archiveFacts{Record: inCluster(), Live: live(""), Store: store},
			archiveUnknown, "", "its own name"},
		{"a path with a slash", archiveFacts{Record: inCluster(), Live: live("dc-tsdb-acme-1a2b3c4d/.."), Store: store},
			archiveUnknown, "", "not a single path segment"},
		{"another instance's path", archiveFacts{Record: inCluster(), Live: live("dc-tsdb-other-12345678"), Store: store},
			archiveUnknown, "", "not a path dcctl names for instance"},
		{"the ObjectStore unreadable", archiveFacts{Record: inCluster(), Live: live(testArchivePath), StoreErr: errors.New("gone")},
			archiveUnknown, "", "ObjectStore could not be read (gone)"},
		{"a different store than the record's", archiveFacts{Record: inCluster(), Live: live(testArchivePath),
			Store: liveObjectStore{Bucket: testArchiveBucket, Endpoint: "http://minio.elsewhere:9000"}},
			archiveUnknown, "", "the two do not agree"},
		{"a different bucket than the record's", archiveFacts{Record: inCluster(), Live: live(testArchivePath),
			Store: liveObjectStore{Bucket: "devicechain-rdb", Endpoint: inClusterEndpoint}},
			archiveUnknown, "", "the two do not agree"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := planArchiveRemoval("acme", tc.f)
			if p.Action != tc.action {
				t.Fatalf("action = %d, want %d (%s)", p.Action, tc.action, p.Reason)
			}
			if p.Prefix != tc.prefix {
				t.Errorf("prefix = %q, want %q", p.Prefix, tc.prefix)
			}
			if tc.action == archiveRemove && (p.Bucket != testArchiveBucket || p.Endpoint != inClusterEndpoint) {
				t.Errorf("removes from %s at %s", p.Bucket, p.Endpoint)
			}
			if !strings.Contains(p.Reason, tc.reason) {
				t.Errorf("reason %q does not contain %q", p.Reason, tc.reason)
			}
		})
	}
}

func TestOnlyAServiceInTheInfrastructureNamespaceIsTheInClusterStore(t *testing.T) {
	for endpoint, want := range map[string]string{
		"http://dc-object-store.dc-system:9000":                   "dc-object-store:9000",
		"http://dc-object-store.dc-system.svc:9000":               "dc-object-store:9000",
		"http://dc-object-store.dc-system.svc.cluster.local:9000": "dc-object-store:9000",
		"https://dc-object-store.dc-system":                       "dc-object-store:443",
		"http://dc-object-store.other:9000":                       "",
		"https://s3.amazonaws.com":                                "",
		"http://dc-object-store:9000":                             "",
		"ftp://dc-object-store.dc-system:9000":                    "",
	} {
		svc, port, err := parseInClusterEndpoint(endpoint)
		got := ""
		if err == nil {
			got = fmt.Sprintf("%s:%d", svc, port)
		}
		if got != want {
			t.Errorf("%s → %q (%v), want %q", endpoint, got, err, want)
		}
	}
}

// The live reads behind the plan: the ObjectStore the archiver names decides the store,
// and a destination with a sub-path is refused rather than read as a bare bucket.
func TestReadArchivePlanReadsTheStoreTheArchiverWritesTo(t *testing.T) {
	fakeHome(t)
	typed := fake.NewSimpleClientset(kubeSystem(testClusterUID))
	rec := aCompleteInstall()
	if err := writeInstalled(context.Background(), typed, rec, installClock); err != nil {
		t.Fatal(err)
	}
	objs := archivingEventStore(testArchivePath, inClusterEndpoint)
	r := &teardownRig{typed: fake.NewSimpleClientset()}
	dyn := r.withEventStore(t, objs...)

	p := readArchivePlan(context.Background(), dyn, typed, "acme", false)
	if p.Action != archiveRemove || p.location() != "s3://"+testArchiveBucket+"/"+testArchivePath+"/" || p.ClusterUID != testClusterUID {
		t.Fatalf("plan = %+v", p)
	}

	content := objs[1].(interface{ UnstructuredContent() map[string]any }).UnstructuredContent()
	content["spec"].(map[string]any)["configuration"].(map[string]any)["destinationPath"] = "s3://" + testArchiveBucket + "/sub/"
	dyn = r.withEventStore(t, objs...)
	if p := readArchivePlan(context.Background(), dyn, typed, "acme", false); p.Action != archiveUnknown ||
		!strings.Contains(p.Reason, "not a bare s3://<bucket>/") {
		t.Fatalf("a destination with a sub-path was not refused: %+v", p)
	}
}

func TestACorruptRecordOfTheArchiveIsNotReadAsNone(t *testing.T) {
	home := fakeHome(t)
	dir := filepath.Join(home, ".devicechain", "instances", "acme")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, recordedArchiveFile), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if r, err := readRecordedArchive("acme"); err == nil {
		t.Fatalf("a corrupt record read as %+v", r)
	}
	if looksLikeEscrow(recordedArchiveFile) {
		t.Fatal("the archive record would be spared as escrow and outlive the destroy")
	}
}

func TestTheRestoreRemedyNamesKeepBackups(t *testing.T) {
	if note := alreadyLiveRestoreNote(TsdbClusterName); !strings.Contains(note, "--keep-backups") {
		t.Errorf("the remedy for an ineffective restore sends the operator to a destroy that deletes the archive: %q", note)
	}
}
