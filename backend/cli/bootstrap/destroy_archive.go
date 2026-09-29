// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/fatih/color"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
)

// A destroyed instance's event-store backups, in the cluster's own object store.
//
// 🔴 WHY DESTROY TOUCHES THE OBJECT STORE AT ALL. Every instance's event store archives
// its WAL and base backups into one shared bucket of the in-cluster store, under a path of
// its own. Barman's retention prunes only a LIVE Cluster's own path, so a destroyed
// instance's archive was pruned by nothing, ever. Measured on a benchmark cluster: one
// destroy left 14 GB of a previous instance behind in a 20 GiB store, the next instance
// filled the rest, WAL archiving failed on a full store, and the event-store primary kept
// every unshipped segment until its own volume filled and PostgreSQL stopped — with no
// failover, because a full disk is not a lost primary. A store that one destroyed
// instance can fill stops archiving for EVERY live instance on the cluster.
//
// 🔴 AND WHY IT NEVER TOUCHES ANY OTHER STORE. An object store the operator supplied
// (`--backup-credentials-file`) is theirs, and what is in it is the disaster-recovery copy
// that outlives the cluster: destroy deletes nothing there and says where it is. The
// relational store's archive is the cluster's, in its own bucket, and is never touched.
// Neither is another instance's path, or an earlier generation of this instance's: only
// the exact path the live event store was archiving under, read before anything changed.

// objectStoreGVR is the barman-cloud plugin's ObjectStore: where an archiver writes.
var objectStoreGVR = schema.GroupVersionResource{Group: "barmancloud.cnpg.io", Version: "v1", Resource: "objectstores"}

// archiveAction is what destroy does with the instance's event-store archive.
type archiveAction int

const (
	// archiveNone: the cluster was installed without backups, so there is no archive.
	archiveNone archiveAction = iota
	// archiveKeep: --keep-backups.
	archiveKeep
	// archiveExternal: in an object store the operator owns. Never deleted from.
	archiveExternal
	// archiveUnknown: which archive is this instance's could not be settled. Nothing is
	// removed, and the destroy says why.
	archiveUnknown
	// archiveRemove: everything under Prefix in Bucket of the in-cluster store.
	archiveRemove
)

// archivePlan is what destroy will do with the instance's event-store archive, settled
// before the first change.
type archivePlan struct {
	Action archiveAction
	Bucket string
	// Prefix is "<serverName>/". 🔴 THE TRAILING SLASH IS LOAD-BEARING: without it,
	// "dc-tsdb-acme-1a2b3c4d" is also a prefix of "dc-tsdb-acme-1a2b3c4d-restored-…", the
	// archive a later restore of this very instance writes to.
	Prefix   string
	Endpoint string
	// Archive and ClusterUID are what the store is opened with: the credential the
	// install record names, checked as this cluster's own.
	Archive    ClusterArchive
	ClusterUID string
	// FromRecorded: the path came from an earlier run of this destroy, because the event
	// store it was read from is already gone.
	FromRecorded bool
	// Reason is the sentence printed for Keep, External and Unknown.
	Reason string
}

// location is the plan's archive as an operator would name it.
func (p archivePlan) location() string {
	return "s3://" + p.Bucket + "/" + p.Prefix
}

// liveObjectStore is what the event store's ObjectStore says it writes to.
type liveObjectStore struct {
	Bucket   string
	Endpoint string
}

// archiveFacts is every input planArchiveRemoval decides from, gathered by
// readArchivePlan so the decision itself needs no cluster.
type archiveFacts struct {
	Keep       bool
	Record     *InstallRecord
	RecordErr  error
	ClusterUID string
	// Live is the event store's archiver, read from the Cluster.
	Live    clusterArchiveState
	LiveErr error
	// Store is the ObjectStore that archiver names. Read only when the Cluster is there.
	Store    liveObjectStore
	StoreErr error
	// Recorded is what an earlier, unfinished run of this destroy settled. Read only
	// when the Cluster is gone.
	Recorded    *recordedArchive
	RecordedErr error
}

// restoredArchiveSuffix is the stamp RestoredArchivePath appends: a path minted for ONE
// restored Cluster, so its own whatever it was restored from.
var restoredArchiveSuffix = regexp.MustCompile(`-restored-[0-9]{8}T[0-9]{6}Z$`)

// archivePathRefusal says why path must not be removed as instance's archive, or "".
//
// 🔴 A DELETE BY PREFIX IS ONLY AS SAFE AS THE PREFIX. The path comes from the live
// Cluster, which dcctl rendered — but a hand-edited serverName is still a string, and
// this is the one place it becomes an argument to a recursive delete.
func archivePathRefusal(instance, path string) string {
	own := TsdbClusterName + "-" + instance
	switch {
	case path == "":
		// The own-name default: every event store archiving that way on this cluster
		// shares the one path, so it is nobody's to remove.
		return fmt.Sprintf("the event store archives under its own name (%s/), a path every event store "+
			"archiving that way on this cluster would share; refusing to remove it", TsdbClusterName)
	case strings.ContainsAny(path, "/\\*?") || path == "." || path == "..":
		return fmt.Sprintf("the event store's archive path %q is not a single path segment; refusing to remove it", path)
	case path == own || strings.HasPrefix(path, own+"-") || restoredArchiveSuffix.MatchString(path):
		return ""
	default:
		return fmt.Sprintf("the event store archives under %q, which is not a path dcctl names for instance %q; "+
			"refusing to remove it", path, instance)
	}
}

// planArchiveRemoval decides what destroy does with the instance's event-store archive.
// Pure: no cluster, no network.
func planArchiveRemoval(instance string, f archiveFacts) archivePlan {
	unknown := func(format string, args ...any) archivePlan {
		return archivePlan{Action: archiveUnknown, Reason: fmt.Sprintf(format, args...)}
	}
	// Best-effort: where the archive is, for the lines that only NAME it.
	named := func() string {
		switch {
		case f.LiveErr == nil && f.Live.Exists && f.Live.Path != "" && f.StoreErr == nil && f.Store.Bucket != "":
			return "s3://" + f.Store.Bucket + "/" + f.Live.Path + "/ at " + f.Store.Endpoint
		case f.Recorded != nil:
			return "s3://" + f.Recorded.Bucket + "/" + f.Recorded.Prefix + " at " + f.Recorded.Endpoint
		}
		return ""
	}

	if f.Keep {
		where := named()
		if where == "" {
			where = "wherever the event store archived them"
		}
		return archivePlan{Action: archiveKeep, Reason: "event-store backups kept (--keep-backups): " + where}
	}
	if f.RecordErr != nil {
		return unknown("the cluster's install record cannot be read (%v), so where this instance's event-store "+
			"backups are is not known; nothing was removed from any object store", f.RecordErr)
	}
	rec := f.Record
	if !rec.Settings.DatabaseBackups {
		return archivePlan{Action: archiveNone}
	}
	if rec.Settings.BackupsExternal {
		// 🔴 BEFORE ANY OTHER ANSWER: nothing below may ever act on an operator's store,
		// and whether the path could be read makes no difference to that.
		where := named()
		if where == "" {
			where = "bucket " + rec.Outputs.Archive.BucketTsdb + " at " + rec.Outputs.Archive.EndpointURL
		}
		return archivePlan{Action: archiveExternal, Reason: fmt.Sprintf("event-store backups kept: %s is in an "+
			"object store you own. It is this instance's disaster-recovery copy; delete it yourself when you "+
			"no longer need it", where)}
	}

	var bucket, path, endpoint string
	fromRecorded := false
	switch {
	case f.LiveErr != nil:
		return unknown("the event store's archive path could not be read (%v); nothing was removed", f.LiveErr)
	case f.Live.Exists:
		if refusal := archivePathRefusal(instance, f.Live.Path); refusal != "" {
			return unknown("%s", refusal)
		}
		if f.StoreErr != nil {
			return unknown("the event store's ObjectStore could not be read (%v), so which store and bucket "+
				"s3://…/%s/ is in is not known; nothing was removed", f.StoreErr, f.Live.Path)
		}
		bucket, path, endpoint = f.Store.Bucket, f.Live.Path, f.Store.Endpoint
	case f.RecordedErr != nil:
		return unknown("the event store is gone and what an earlier run of this destroy recorded about its "+
			"archive cannot be read (%v); nothing was removed", f.RecordedErr)
	case f.Recorded != nil:
		bucket, path, endpoint = f.Recorded.Bucket, strings.TrimSuffix(f.Recorded.Prefix, "/"), f.Recorded.Endpoint
		fromRecorded = true
		if refusal := archivePathRefusal(instance, path); refusal != "" {
			return unknown("%s", refusal)
		}
	default:
		return unknown("the event store is already gone, so the archive path it wrote under cannot be read. "+
			"Archives in bucket %s of the in-cluster object store that belong to no running instance can be "+
			"removed by hand (see Removing an instance in the documentation)", rec.Outputs.Archive.BucketTsdb)
	}

	loc := "s3://" + bucket + "/" + path + "/"
	a := rec.Outputs.Archive
	// 🔴 THE STORE THE ARCHIVER WROTE TO, NOT THE ONE THE RECORD NAMES TODAY. They agree
	// unless the cluster was re-installed onto a different destination after this
	// instance was built — and then deleting "the same path" from the store the record
	// names would act on a store this instance never wrote to.
	if endpoint != a.EndpointURL || bucket != a.BucketTsdb {
		return unknown("the event store archives to %s at %s, but the cluster's install record names bucket %s "+
			"at %s for the in-cluster object store; refusing to remove from a store the two do not agree on",
			loc, endpoint, a.BucketTsdb, a.EndpointURL)
	}
	if _, _, err := parseInClusterEndpoint(endpoint); err != nil {
		return unknown("%v; nothing was removed from %s", err, loc)
	}
	return archivePlan{
		Action: archiveRemove, Bucket: bucket, Prefix: path + "/", Endpoint: endpoint,
		Archive: a, ClusterUID: f.ClusterUID, FromRecorded: fromRecorded,
	}
}

// parseInClusterEndpoint reads the Service and port an in-cluster store endpoint names.
// Anything that is not a Service in the shared infrastructure namespace is refused: the
// only store destroy may delete from is the one `dcctl install` put there.
func parseInClusterEndpoint(endpoint string) (service string, port int, err error) {
	u, err := url.Parse(endpoint)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "", 0, fmt.Errorf("the object store endpoint %q is not an http(s) URL", endpoint)
	}
	parts := strings.Split(u.Hostname(), ".")
	ok := len(parts) >= 2 && parts[0] != "" && parts[1] == infraNamespace
	switch len(parts) {
	case 2:
	case 3:
		ok = ok && parts[2] == "svc"
	case 5:
		ok = ok && parts[2] == "svc" && parts[3] == "cluster" && parts[4] == "local"
	default:
		ok = false
	}
	if !ok {
		return "", 0, fmt.Errorf("the object store endpoint %q does not name a Service in namespace %s, so it is "+
			"not the cluster's in-cluster object store", endpoint, infraNamespace)
	}
	port = 80
	if u.Scheme == "https" {
		port = 443
	}
	if p := u.Port(); p != "" {
		if _, err := fmt.Sscanf(p, "%d", &port); err != nil {
			return "", 0, fmt.Errorf("the object store endpoint %q has an unreadable port", endpoint)
		}
	}
	return parts[0], port, nil
}

// readArchivePlan gathers what planArchiveRemoval decides from. Called by Destroy before
// the prompt and before anything is changed: the event store it reads is deleted by the
// destroy, and its archiver's serverName is the only trustworthy record of the path.
//
// 🔴 NOT THE OPENTOFU OUTPUT. The instance root reports a backup destination, but a
// destroy re-evaluates it without the serverName a bootstrap passes, so at destroy time
// it names `s3://<bucket>/dc-tsdb` — a path this instance never wrote to. Measured on a
// benchmark cluster's destroy transcript.
func readArchivePlan(ctx context.Context, dyn dynamic.Interface, typed kubernetes.Interface, instance string, keep bool) archivePlan {
	f := archiveFacts{Keep: keep}
	f.ClusterUID, f.RecordErr = ClusterUID(ctx, typed)
	if f.RecordErr == nil {
		f.Record, f.RecordErr = readInstallRecord(ctx, typed, f.ClusterUID)
	}
	f.Live, f.LiveErr = readArchiveState(ctx, dyn, instance)
	switch {
	case f.LiveErr != nil:
	case f.Live.Exists && f.Live.ObjectStore == "":
		f.StoreErr = errors.New("its archiver names no ObjectStore")
	case f.Live.Exists:
		f.Store, f.StoreErr = readObjectStore(ctx, dyn, InstanceNamespace(instance), f.Live.ObjectStore)
	default:
		f.Recorded, f.RecordedErr = readRecordedArchive(instance)
	}
	return planArchiveRemoval(instance, f)
}

// readObjectStore reads which bucket, at which endpoint, an ObjectStore writes to.
//
// 🔴 A DESTINATION WITH A SUB-PATH IS REFUSED. The chart renders `s3://<bucket>/`, so the
// archive is at `<bucket>/<serverName>/`. A destination of `s3://<bucket>/x/` would put it
// at `<bucket>/x/<serverName>/`, and deleting `<bucket>/<serverName>/` would delete
// something else.
func readObjectStore(ctx context.Context, dyn dynamic.Interface, namespace, name string) (liveObjectStore, error) {
	obj, err := dyn.Resource(objectStoreGVR).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) || meta.IsNoMatchError(err) {
			return liveObjectStore{}, fmt.Errorf("ObjectStore %s/%s is not there", namespace, name)
		}
		return liveObjectStore{}, fmt.Errorf("reading ObjectStore %s/%s: %w", namespace, name, err)
	}
	dest, _, err := unstructured.NestedString(obj.Object, "spec", "configuration", "destinationPath")
	if err != nil {
		return liveObjectStore{}, fmt.Errorf("reading ObjectStore %s/%s's destinationPath: %w", namespace, name, err)
	}
	endpoint, _, err := unstructured.NestedString(obj.Object, "spec", "configuration", "endpointURL")
	if err != nil {
		return liveObjectStore{}, fmt.Errorf("reading ObjectStore %s/%s's endpointURL: %w", namespace, name, err)
	}
	bucket, ok := strings.CutPrefix(dest, "s3://")
	bucket = strings.TrimSuffix(bucket, "/")
	if !ok || bucket == "" || strings.Contains(bucket, "/") {
		return liveObjectStore{}, fmt.Errorf("ObjectStore %s/%s writes to %q, which is not a bare s3://<bucket>/",
			namespace, name, dest)
	}
	return liveObjectStore{Bucket: bucket, Endpoint: endpoint}, nil
}

// ---------------------------------------------------------------------------
// What an unfinished destroy settled, for the run that resumes it
// ---------------------------------------------------------------------------

// recordedArchiveFile holds the archive a destroy settled on, inside
// ~/.devicechain/instances/<instance>/.
//
// 🔴 WHY IT IS WRITTEN AT ALL. The path is read from the live event store, and the
// destroy deletes the event store long before it reaches the archive — so a destroy that
// dies in between (the namespace wait timing out is the common case) would resume with
// nothing to read the path from, and leave exactly the orphan this exists to prevent. The
// directory survives until the destroy finishes, which is exactly as long as a resume can
// happen, and removeStatePreservingEscrow takes the file with it.
//
// It must not match looksLikeEscrow, for the reason destroyMarkerFile must not.
const recordedArchiveFile = "destroy-archive.json"

// recordedArchive names an archive and the store it is in. No credential: the store is
// opened with the cluster's archive credential, read from the cluster when it is needed.
type recordedArchive struct {
	Instance string `json:"instance"`
	Bucket   string `json:"bucket"`
	Prefix   string `json:"prefix"`
	Endpoint string `json:"endpoint"`
}

func recordArchivePlan(instance string, p archivePlan) error {
	dir, err := instanceRoot(instance)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, stateDirMode); err != nil {
		return err
	}
	body, err := json.MarshalIndent(recordedArchive{Instance: instance, Bucket: p.Bucket, Prefix: p.Prefix,
		Endpoint: p.Endpoint}, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, recordedArchiveFile), body, 0o600)
}

// readRecordedArchive returns what an earlier run recorded, nil when nothing was.
// 🔴 A FILE THAT DOES NOT PARSE IS AN ERROR, NOT AN ABSENCE: absent resolves to "leave the
// archive and say so", and a corrupt record must not be quietly read as that.
func readRecordedArchive(instance string) (*recordedArchive, error) {
	dir, err := instanceRoot(instance)
	if err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(filepath.Join(dir, recordedArchiveFile))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var r recordedArchive
	if err := json.Unmarshal(raw, &r); err != nil {
		return nil, fmt.Errorf("%s does not parse: %w", recordedArchiveFile, err)
	}
	if r.Instance != instance || r.Bucket == "" || !strings.HasSuffix(r.Prefix, "/") || r.Endpoint == "" {
		return nil, fmt.Errorf("%s is incomplete or names another instance", recordedArchiveFile)
	}
	return &r, nil
}

// ---------------------------------------------------------------------------
// Acting on the plan
// ---------------------------------------------------------------------------

// archiveStore is the slice of S3 destroy needs.
type archiveStore interface {
	// List returns every object under prefix, across every page.
	List(ctx context.Context, bucket, prefix string) ([]archiveObject, error)
	// ListPrefixes returns the "directories" directly under prefix (delimiter "/").
	ListPrefixes(ctx context.Context, bucket, prefix string) ([]string, error)
	// Delete removes every key; any key that fails is an error.
	Delete(ctx context.Context, bucket string, keys []string) error
}

type archiveObject struct {
	Key  string
	Size int64
}

// openInClusterArchiveStore reaches the in-cluster store behind a plan's endpoint and
// returns a client and a func that closes the connection. A package var so tests can
// hand destroy a store without a cluster.
var openInClusterArchiveStore = openArchiveStoreThroughPortForward

// settleInstanceArchive carries out the plan, once the instance's namespace is gone. It
// returns why the archive was left, "" when nothing of it was.
//
// 🔴 IT NEVER FAILS THE DESTROY. Everything else of the instance is already gone, and
// failing here would keep a local state describing an instance that no longer exists.
// What was left is said, and the closing line is not green.
func settleInstanceArchive(ctx context.Context, kubeContext string, dyn dynamic.Interface,
	typed kubernetes.Interface, instance string, p archivePlan) (leftArchive string) {
	switch p.Action {
	case archiveNone:
		return ""
	case archiveKeep, archiveExternal:
		fmt.Println(color.WhiteString("  %s", p.Reason))
		return ""
	case archiveUnknown:
		fmt.Println(color.YellowString("  event-store backups NOT removed: %s", p.Reason))
		return "were NOT removed: " + p.Reason
	}

	// 🔴 THE PRECONDITION IS CHECKED, NOT ASSUMED FROM THE STEPS BEFORE IT. The namespace
	// step reports success over a namespace it LEFT ALONE because it is not labelled as
	// this instance's, and --without-state skips the tofu destroy that would have removed
	// the event store — so "the steps above ran" does not mean "nothing is archiving into
	// this path". Deleting a live Cluster's archive leaves it with base backups it cannot
	// restore from, while it keeps writing WAL on top. So: the event store must be gone,
	// and so must its namespace.
	left := func(why string) string {
		fmt.Println(color.YellowString("  event-store backups LEFT at %s: %s", p.location(), why))
		return "were LEFT in the in-cluster object store at " + p.location() + ": " + why
	}
	if st, err := readArchiveState(ctx, dyn, instance); err != nil {
		return left(fmt.Sprintf("could not confirm the event store is gone (%v)", err))
	} else if st.Exists {
		return left("the event store is still there, so it may still be archiving into this path")
	}
	if _, err := typed.CoreV1().Namespaces().Get(ctx, InstanceNamespace(instance), metav1.GetOptions{}); err == nil {
		return left(fmt.Sprintf("namespace %s is still there", InstanceNamespace(instance)))
	} else if !apierrors.IsNotFound(err) {
		return left(fmt.Sprintf("could not confirm namespace %s is gone (%v)", InstanceNamespace(instance), err))
	}
	return removeInstanceArchive(ctx, kubeContext, typed, instance, p, left)
}

// removeInstanceArchive deletes everything under the plan's prefix and checks that
// nothing is left.
func removeInstanceArchive(ctx context.Context, kubeContext string, typed kubernetes.Interface, instance string,
	p archivePlan, left func(string) string) string {
	doing(fmt.Sprintf("removing the instance's event-store backups from the in-cluster object store (%s)", p.location()))
	store, closeStore, err := openInClusterArchiveStore(ctx, kubeContext, typed, p)
	if err != nil {
		fmt.Println()
		return left(fmt.Sprintf("could not reach the in-cluster object store: %v", err))
	}
	defer closeStore()

	objs, err := store.List(ctx, p.Bucket, p.Prefix)
	if err != nil {
		fmt.Println()
		return left(fmt.Sprintf("listing it failed: %v", err))
	}
	var bytes int64
	keys := make([]string, 0, len(objs))
	for _, o := range objs {
		keys = append(keys, o.Key)
		bytes += o.Size
	}
	if len(keys) > 0 {
		if err := store.Delete(ctx, p.Bucket, keys); err != nil {
			fmt.Println()
			return left(fmt.Sprintf("deleting it failed: %v", err))
		}
	}
	// 🔴 CHECKED, NOT TRUSTED. A delete that returned success is not an empty path; the
	// listing is what says so.
	after, err := store.List(ctx, p.Bucket, p.Prefix)
	if err != nil {
		fmt.Println()
		return left(fmt.Sprintf("could not confirm it is empty: %v", err))
	}
	if len(after) > 0 {
		fmt.Println()
		return left(fmt.Sprintf("it still holds %d object(s) after the delete", len(after)))
	}
	done()
	if len(keys) == 0 {
		fmt.Println(color.WhiteString("  nothing left under %s", p.location()))
	} else {
		fmt.Println(color.WhiteString("  removed %d object(s) (%s) from %s", len(keys), mebibytes(bytes), p.location()))
	}
	reportEarlierArchives(ctx, store, instance, p)
	return ""
}

// reportEarlierArchives names — and never deletes — paths in the bucket that look like
// archives of earlier instances of this name.
//
// 🔴 LISTED, NOT REMOVED. Nothing proves which instance wrote them: an instance built
// and destroyed by an earlier dcctl left its archive under the same name, and so did any
// destroy run with --keep-backups — which is precisely an archive someone meant to keep,
// to rebuild from. What can be proved is the one path the live event store was using.
func reportEarlierArchives(ctx context.Context, store archiveStore, instance string, p archivePlan) {
	own := TsdbClusterName + "-" + instance
	earlier := regexp.MustCompile(`^` + regexp.QuoteMeta(own) + `(-[0-9a-f]{8})?(-restored-[0-9]{8}T[0-9]{6}Z)*/$`)
	prefixes, err := store.ListPrefixes(ctx, p.Bucket, own)
	if err != nil {
		fmt.Println(color.HiBlackString("  (could not list other archives in %s: %v)", p.Bucket, err))
		return
	}
	var lines []string
	for _, pre := range prefixes {
		if pre == p.Prefix || !earlier.MatchString(pre) {
			continue
		}
		size := "size unknown"
		if objs, err := store.List(ctx, p.Bucket, pre); err == nil {
			var n int64
			for _, o := range objs {
				n += o.Size
			}
			size = mebibytes(n)
		}
		lines = append(lines, fmt.Sprintf("    s3://%s/%s (%s)", p.Bucket, pre, size))
	}
	if len(lines) == 0 {
		return
	}
	fmt.Println(color.YellowString("  these paths in the same bucket look like archives of earlier instances named %q, "+
		"and were LEFT in place — nothing proves which instance wrote them. Remove the ones nobody will restore from "+
		"by hand (see Removing an instance in the documentation):\n%s", instance, strings.Join(lines, "\n")))
}

func mebibytes(n int64) string {
	return fmt.Sprintf("%.1f MiB", float64(n)/(1<<20))
}

// openArchiveStoreThroughPortForward is the real openInClusterArchiveStore: a port-forward
// to a ready pod behind the store's Service, and an S3 client over it.
//
// 🔴 EVERY READ THROUGH typed COMES BEFORE THE FIRST USE OF kubeContext. The credential,
// the Service and its pods are read through the client the destroy already holds, so a
// store that is not there is found without building a second connection.
func openArchiveStoreThroughPortForward(ctx context.Context, kubeContext string, typed kubernetes.Interface,
	p archivePlan) (archiveStore, func(), error) {
	service, port, err := parseInClusterEndpoint(p.Endpoint)
	if err != nil {
		return nil, nil, err
	}
	creds, err := readArchiveCredentialData(ctx, typed, p.Archive, p.ClusterUID)
	if err != nil {
		return nil, nil, err
	}
	pod, target, err := readyStorePod(ctx, typed, service, port)
	if err != nil {
		return nil, nil, err
	}
	restCfg, err := RestConfig(kubeContext)
	if err != nil {
		return nil, nil, fmt.Errorf("building kube config: %w", err)
	}
	local, stop, err := forwardPort(restCfg, infraNamespace, pod, target)
	if err != nil {
		return nil, nil, err
	}
	scheme := "http"
	if strings.HasPrefix(p.Endpoint, "https://") {
		scheme = "https"
	}
	store := newS3ArchiveStore(scheme+"://"+net.JoinHostPort("127.0.0.1", fmt.Sprint(local)),
		creds[p.Archive.AccessKeyIDKey], creds[p.Archive.SecretAccessKey])
	return store, stop, nil
}

// readyStorePod finds a Running, Ready pod behind the Service and the container port the
// Service's port reaches.
func readyStorePod(ctx context.Context, typed kubernetes.Interface, service string, port int) (string, int, error) {
	svc, err := typed.CoreV1().Services(infraNamespace).Get(ctx, service, metav1.GetOptions{})
	if err != nil {
		return "", 0, fmt.Errorf("reading Service %s/%s: %w", infraNamespace, service, err)
	}
	var sp *corev1.ServicePort
	for i := range svc.Spec.Ports {
		if int(svc.Spec.Ports[i].Port) == port {
			sp = &svc.Spec.Ports[i]
		}
	}
	if sp == nil {
		return "", 0, fmt.Errorf("Service %s/%s has no port %d", infraNamespace, service, port)
	}
	if len(svc.Spec.Selector) == 0 {
		return "", 0, fmt.Errorf("Service %s/%s selects no pods", infraNamespace, service)
	}
	pods, err := typed.CoreV1().Pods(infraNamespace).List(ctx, metav1.ListOptions{
		LabelSelector: labels.SelectorFromSet(svc.Spec.Selector).String(),
	})
	if err != nil {
		return "", 0, fmt.Errorf("listing the pods behind Service %s/%s: %w", infraNamespace, service, err)
	}
	for _, pod := range pods.Items {
		if pod.Status.Phase != corev1.PodRunning || pod.DeletionTimestamp != nil || !podReady(&pod) {
			continue
		}
		target, err := targetPort(sp, &pod)
		if err != nil {
			return "", 0, err
		}
		return pod.Name, target, nil
	}
	return "", 0, fmt.Errorf("no Running, Ready pod behind Service %s/%s", infraNamespace, service)
}

func podReady(pod *corev1.Pod) bool {
	for _, c := range pod.Status.Conditions {
		if c.Type == corev1.PodReady {
			return c.Status == corev1.ConditionTrue
		}
	}
	return false
}

// targetPort resolves a Service port's target on one pod: a number as given, a name
// through the pod's container ports, and unset as the Service port itself.
func targetPort(sp *corev1.ServicePort, pod *corev1.Pod) (int, error) {
	switch {
	case sp.TargetPort.IntValue() != 0:
		return sp.TargetPort.IntValue(), nil
	case sp.TargetPort.StrVal == "":
		return int(sp.Port), nil
	}
	for _, c := range pod.Spec.Containers {
		for _, cp := range c.Ports {
			if cp.Name == sp.TargetPort.StrVal {
				return int(cp.ContainerPort), nil
			}
		}
	}
	return 0, fmt.Errorf("pod %s has no container port named %q", pod.Name, sp.TargetPort.StrVal)
}
