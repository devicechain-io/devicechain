// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package controllers

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/events"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	dcv1beta1 "github.com/devicechain-io/dc-k8s/api/v1beta1"
)

// SNAPSHOT RETENTION: the pruning CloudNativePG does not do.
//
// A database store can take its scheduled base backup as a CSI volume snapshot
// (the cnpg-cluster chart's backup.snapshotClass). CloudNativePG takes those, and
// never deletes one: the retention policy on the backup plugin's ObjectStore reaches
// only what is in the object store. Left alone, every daily snapshot is kept for
// ever -- each a full copy of the database as of that day, including rows a tenant
// deletion has since erased.
//
// So the operator prunes them, to the same recovery window the object store keeps,
// which the chart writes on the snapshot ScheduledBackup as a label. Deleting the
// Backup is enough: the chart sets `snapshotOwnerReference: backup` on the Cluster,
// so the Backup owns its VolumeSnapshot and the garbage collector removes it (and,
// under a class with deletionPolicy Delete, the provider's copy).
//
// 🔴 IT RUNS ON A TIMER, NOT A WATCH, AND READS UNCACHED. A watch needs an informer,
// and an informer on a CRD that is not installed -- a cluster installed without
// CloudNativePG -- would keep the manager from ever syncing, taking the Instance
// controller down with it. A list every ten minutes costs nothing at this scale and
// answers "CloudNativePG is not here" with a NoMatch error it can read as exactly
// that.
//
// 🔴 IT IS NOT LEADER-ELECTED, BECAUSE NOTHING HERE IS. The manager runs with leader
// election off and the Deployment at one replica. Two replicas would both prune;
// that is safe rather than merely unlikely, because each delete is conditional on
// the UID it read and a delete that finds the Backup gone, or replaced, is treated
// as done (TestSnapshotRetentionDeletesOnlyTheBackupItRead replaces one mid-pass).
//
// 🔴 WHEN IT STOPS, SOMETHING HAS TO SAY SO. A retention promise enforced by a
// component whose failure is silent is a plausible value, not a guarantee. After
// every pass that looked at a schedule without failing, the operator stamps the
// ScheduledBackup with SnapshotRetentionCheckedAnnotation; kube-state-metrics
// exports it, and an instance's alerting rules fire when it goes stale. An operator
// that is down, crash-looping, from before this existed, or refusing an unreadable
// window, all stop the stamp.

const (
	// SnapshotRetentionLabel is on a volume-snapshot ScheduledBackup the chart
	// renders. Its value is the recovery window, in the ObjectStore's retentionPolicy
	// grammar; empty keeps every snapshot, as an empty retentionPolicy keeps every
	// object-store backup.
	SnapshotRetentionLabel = "devicechain.io/snapshot-retention"

	// SnapshotRetentionCheckedAnnotation is written on the ScheduledBackup, as an
	// RFC 3339 time, after a pass that finished with it. Its age is what the
	// stalled-pruning alert reads.
	SnapshotRetentionCheckedAnnotation = "devicechain.io/snapshot-retention-checked-at"

	// The chart's component label on the snapshot ScheduledBackup. It, the retention
	// label and a DeviceChain namespace are all required before anything is deleted;
	// of the three, only the namespace is a fence, because labelling a namespace
	// takes cluster-scoped rights (see api/v1beta1/labels.go) while anyone who can
	// edit a ScheduledBackup can copy its labels.
	//
	// 🔴 NOTHING HERE READS app.kubernetes.io/managed-by. Helm owns that label: it
	// overwrites it with "Helm" on every object it installs or upgrades, whatever the
	// template rendered, so a selector on a rendered value matches nothing on a real
	// install -- which is exactly how an earlier version of this pruner never pruned.
	// Select only on keys Helm does not write (TestSnapshotRetentionSelectsWhatHelmApplies
	// applies the real chart with Helm and runs a pass over what it left).
	componentLabel          = "app.kubernetes.io/component"
	snapshotBackupComponent = "database-snapshot-backup"

	// CloudNativePG's label on every Backup a ScheduledBackup creates, naming it
	// (pkg/utils/labels_annotations.go, ParentScheduledBackupLabelName, v1.30.0).
	scheduledBackupLabel = "cnpg.io/scheduled-backup"

	// Backup phases, as CloudNativePG writes them (api/v1/backup_types.go, v1.30.0).
	backupPhaseCompleted = "completed"
	backupPhaseFailed    = "failed"
	backupPhaseInvalid   = "invalid backup definition"

	methodVolumeSnapshot = "volumeSnapshot"
)

var (
	scheduledBackupListGVK = schema.GroupVersionKind{Group: "postgresql.cnpg.io", Version: "v1", Kind: "ScheduledBackupList"}
	backupListGVK          = schema.GroupVersionKind{Group: "postgresql.cnpg.io", Version: "v1", Kind: "BackupList"}
)

//+kubebuilder:rbac:groups=postgresql.cnpg.io,resources=scheduledbackups,verbs=get;list;patch
//+kubebuilder:rbac:groups=postgresql.cnpg.io,resources=backups,verbs=get;list;delete
//+kubebuilder:rbac:groups="",resources=namespaces,verbs=get
//+kubebuilder:rbac:groups=events.k8s.io,resources=events,verbs=create;patch

// SnapshotRetention prunes volume-snapshot base backups to their store's recovery
// window. It is a manager Runnable.
type SnapshotRetention struct {
	// Reader lists and gets, uncached (the manager's API reader): see above for why
	// no informer.
	Reader client.Reader
	// Writer deletes Backups and stamps ScheduledBackups.
	Writer   client.Writer
	Recorder events.EventRecorder
	Interval time.Duration
	Now      func() time.Time
}

// Start runs a pass at once and then every Interval, until ctx is done. A failed
// pass is logged and does not stop the loop: the next one retries, and the stamp a
// failed pass does not write is what raises the alert.
func (p *SnapshotRetention) Start(ctx context.Context) error {
	logger := log.FromContext(ctx).WithName("snapshot-retention")
	ticker := time.NewTicker(p.Interval)
	defer ticker.Stop()
	for {
		if err := p.Pass(ctx); err != nil {
			logger.Error(err, "snapshot retention pass failed; the next one retries")
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

// Pass prunes once, over every DeviceChain snapshot ScheduledBackup in the cluster.
// It returns nil on a cluster without CloudNativePG.
func (p *SnapshotRetention) Pass(ctx context.Context) error {
	logger := log.FromContext(ctx).WithName("snapshot-retention")
	schedules := &unstructured.UnstructuredList{}
	schedules.SetGroupVersionKind(scheduledBackupListGVK)
	err := p.Reader.List(ctx, schedules,
		client.HasLabels{SnapshotRetentionLabel},
		client.MatchingLabels{componentLabel: snapshotBackupComponent})
	switch {
	case meta.IsNoMatchError(err):
		logger.V(1).Info("CloudNativePG is not installed; nothing to prune")
		return nil
	case err != nil:
		return fmt.Errorf("listing snapshot ScheduledBackups: %w", err)
	}

	var errs []error
	for i := range schedules.Items {
		if err := p.pruneSchedule(ctx, &schedules.Items[i]); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// pruneSchedule applies one ScheduledBackup's window to its Backups, and stamps it
// when that finished.
func (p *SnapshotRetention) pruneSchedule(ctx context.Context, sb *unstructured.Unstructured) error {
	logger := log.FromContext(ctx).WithName("snapshot-retention").
		WithValues("namespace", sb.GetNamespace(), "scheduledBackup", sb.GetName())
	where := sb.GetNamespace() + "/" + sb.GetName()

	ours, err := p.deviceChainNamespace(ctx, sb.GetNamespace())
	if err != nil {
		return fmt.Errorf("ScheduledBackup %s: %w", where, err)
	}
	if !ours {
		// Not written on the object: it is not ours, so neither is its event stream.
		logger.Info("ignoring a snapshot ScheduledBackup outside a DeviceChain namespace")
		return nil
	}

	method, _, _ := unstructured.NestedString(sb.Object, "spec", "method")
	if method != methodVolumeSnapshot {
		p.Recorder.Eventf(sb, nil, corev1.EventTypeWarning, "SnapshotRetentionIgnored", "Prune",
			"carries %s but its method is %q, not %q; nothing is pruned", SnapshotRetentionLabel, method,
			methodVolumeSnapshot)
		return nil
	}
	raw := sb.GetLabels()[SnapshotRetentionLabel]
	window, err := ParseRetentionWindow(raw)
	if err != nil {
		// 🔴 NOTHING IS DELETED, AND NOTHING IS STAMPED. A window this cannot read is
		// not "no window" and not a default one: guessing would delete what the
		// operator meant to keep, and staying quiet would keep for ever what they meant
		// to prune. The missing stamp is what raises the alert.
		p.Recorder.Eventf(sb, nil, corev1.EventTypeWarning, "SnapshotRetentionInvalid", "Prune",
			"%s is %q: %v. Nothing is pruned until it is fixed", SnapshotRetentionLabel, raw, err)
		return fmt.Errorf("ScheduledBackup %s: %w", where, err)
	}

	now := p.Now()
	if window > 0 {
		cluster, _, _ := unstructured.NestedString(sb.Object, "spec", "cluster", "name")
		backups := &unstructured.UnstructuredList{}
		backups.SetGroupVersionKind(backupListGVK)
		if err := p.Reader.List(ctx, backups, client.InNamespace(sb.GetNamespace()),
			client.MatchingLabels{scheduledBackupLabel: sb.GetName()}); err != nil {
			return fmt.Errorf("listing the Backups of ScheduledBackup %s: %w", where, err)
		}
		var views []backupView
		for i := range backups.Items {
			v := viewOf(&backups.Items[i])
			// This schedule's snapshots of this schedule's Cluster, and nothing else:
			// the label alone would also match a Backup someone created by hand with it.
			if v.Method == methodVolumeSnapshot && v.Cluster == cluster {
				views = append(views, v)
			}
		}
		for _, b := range selectForPruning(views, window, now) {
			if err := p.deleteBackup(ctx, sb.GetNamespace(), b); err != nil {
				return fmt.Errorf("pruning Backup %s/%s: %w", sb.GetNamespace(), b.Name, err)
			}
			p.Recorder.Eventf(sb, nil, corev1.EventTypeNormal, "SnapshotBackupPruned", "Prune",
				"deleted Backup %s (%s, finished %s), outside the %s recovery window", b.Name, b.Phase,
				b.age(), raw)
		}
	}

	patch := client.RawPatch(types.MergePatchType, []byte(fmt.Sprintf(
		`{"metadata":{"annotations":{%q:%q}}}`, SnapshotRetentionCheckedAnnotation, now.UTC().Format(time.RFC3339))))
	if err := p.Writer.Patch(ctx, sb, patch); err != nil {
		return fmt.Errorf("recording the retention pass on ScheduledBackup %s: %w", where, err)
	}
	return nil
}

// deviceChainNamespace reports whether namespace carries one of the labels that
// say DeviceChain owns it.
func (p *SnapshotRetention) deviceChainNamespace(ctx context.Context, namespace string) (bool, error) {
	var ns corev1.Namespace
	if err := p.Reader.Get(ctx, client.ObjectKey{Name: namespace}, &ns); err != nil {
		return false, fmt.Errorf("reading namespace %s: %w", namespace, err)
	}
	labels := ns.GetLabels()
	if _, ok := labels[dcv1beta1.InstanceNamespaceLabel]; ok {
		return true, nil
	}
	return labels[dcv1beta1.ComponentLabel] == dcv1beta1.InfrastructureComponent, nil
}

// deleteBackup deletes one Backup, only if it is still the object that was read.
// Gone already, or replaced by one of the same name, is done: the garbage collector,
// a second replica or an operator got there first.
func (p *SnapshotRetention) deleteBackup(ctx context.Context, namespace string, b backupView) error {
	obj := &unstructured.Unstructured{}
	obj.SetGroupVersionKind(schema.GroupVersionKind{Group: "postgresql.cnpg.io", Version: "v1", Kind: "Backup"})
	obj.SetNamespace(namespace)
	obj.SetName(b.Name)
	uid := b.UID
	err := p.Writer.Delete(ctx, obj, client.Preconditions{UID: &uid},
		client.PropagationPolicy(metav1.DeletePropagationBackground))
	if apierrors.IsNotFound(err) || apierrors.IsConflict(err) {
		return nil
	}
	return err
}

// retentionWindow is the grammar of the ObjectStore's retentionPolicy
// (^[1-9][0-9]*[dwm]$), plus h.
var retentionWindow = regexp.MustCompile(`^([1-9][0-9]*)([hdwm])$`)

// ParseRetentionWindow reads a recovery window. Empty is zero: keep everything.
//
// d is a day, w seven, and m -- MONTHS, as in the ObjectStore -- thirty-one, so a
// window here is never SHORTER than barman's reading of the same string, and a
// snapshot is never pruned before the object-store backups of the same age.
//
// h exists for verifying pruning on a live cluster without waiting days. The chart
// never writes it: the ObjectStore's pattern refuses it, and the chart writes the
// ObjectStore's own window.
func ParseRetentionWindow(s string) (time.Duration, error) {
	if s == "" {
		return 0, nil
	}
	m := retentionWindow.FindStringSubmatch(s)
	if m == nil {
		return 0, fmt.Errorf("not a recovery window: want a whole number and h, d, w or m (months), like \"7d\"")
	}
	n, err := strconv.ParseInt(m[1], 10, 32)
	if err != nil {
		return 0, fmt.Errorf("not a recovery window: %w", err)
	}
	unit := map[string]time.Duration{
		"h": time.Hour,
		"d": 24 * time.Hour,
		"w": 7 * 24 * time.Hour,
		"m": 31 * 24 * time.Hour,
	}[m[2]]
	return time.Duration(n) * unit, nil
}

// backupView is what pruning needs of a CloudNativePG Backup.
type backupView struct {
	Name, Phase, Method, Cluster string
	UID                          types.UID
	Created, StoppedAt           time.Time
}

func (b backupView) age() string {
	if b.StoppedAt.IsZero() {
		return "never"
	}
	return b.StoppedAt.UTC().Format(time.RFC3339)
}

func viewOf(u *unstructured.Unstructured) backupView {
	v := backupView{Name: u.GetName(), UID: u.GetUID(), Created: u.GetCreationTimestamp().Time}
	v.Phase, _, _ = unstructured.NestedString(u.Object, "status", "phase")
	v.Method, _, _ = unstructured.NestedString(u.Object, "spec", "method")
	v.Cluster, _, _ = unstructured.NestedString(u.Object, "spec", "cluster", "name")
	if s, ok, _ := unstructured.NestedString(u.Object, "status", "stoppedAt"); ok {
		if t, err := time.Parse(time.RFC3339, s); err == nil {
			v.StoppedAt = t
		}
	}
	return v
}

// selectForPruning returns the backups a window of this length, at now, deletes.
//
// The window's first moment has to stay restorable, so the base it replays from
// stays: every completed backup that finished inside the window, AND the newest one
// that finished before it -- barman's own rule for the object store. The others that
// finished before it go.
//
// A failed or invalid backup holds nothing restorable; it goes once it was created
// before the window. A backup in any other phase -- pending, running, finalizing --
// is never touched: it may be the one being taken now.
func selectForPruning(backups []backupView, window time.Duration, now time.Time) []backupView {
	if window <= 0 {
		return nil
	}
	start := now.Add(-window)

	var before []backupView
	var out []backupView
	for _, b := range backups {
		switch b.Phase {
		case backupPhaseCompleted:
			if !b.StoppedAt.IsZero() && b.StoppedAt.Before(start) {
				before = append(before, b)
			}
		case backupPhaseFailed, backupPhaseInvalid:
			if b.Created.Before(start) {
				out = append(out, b)
			}
		}
	}
	// Newest first; the first one is the base of the window's first moment.
	sort.SliceStable(before, func(i, j int) bool { return before[i].StoppedAt.After(before[j].StoppedAt) })
	if len(before) > 1 {
		out = append(out, before[1:]...)
	}
	return out
}
