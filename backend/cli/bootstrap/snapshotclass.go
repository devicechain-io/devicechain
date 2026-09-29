// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"fmt"
	"sort"
	"strings"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
)

// The VolumeSnapshotClass preflight: `dcctl install --backup-snapshot-class` and every
// bootstrap on a cluster installed with it.
//
// 🔴 EVERY WRONG CLASS IS ACCEPTED BY EVERY API IT PASSES THROUGH. The Cluster takes any
// class name, the ScheduledBackup is created, and the apply is green; then:
//
//   - a class that does not exist, or a cluster with no snapshot controller at all,
//     fails every snapshot;
//   - a class for another CSI driver than the one that provisions the database volumes
//     fails every snapshot too (a snapshot is taken by the driver that owns the disk);
//   - a class with deletionPolicy Retain takes every snapshot, and then keeps the
//     provider's copy when the operator prunes the VolumeSnapshot -- so the recovery
//     window reads as honoured while every snapshot is kept, and paid for, for ever.
//
// The first two are loud eventually, as failed Backups; the third is never loud. So all
// three are refused here, before anything is written, with the fix in the message.

var volumeSnapshotClassGVR = schema.GroupVersionResource{
	Group: "snapshot.storage.k8s.io", Version: "v1", Resource: "volumesnapshotclasses",
}

// The in-tree provisioner names whose volumes CSI migration hands to a CSI driver, which
// is then the driver that snapshots them. A StorageClass may still carry the old name.
var migratedProvisioners = map[string]string{
	"kubernetes.io/gce-pd":         "pd.csi.storage.gke.io",
	"kubernetes.io/aws-ebs":        "ebs.csi.aws.com",
	"kubernetes.io/azure-disk":     "disk.csi.azure.com",
	"kubernetes.io/cinder":         "cinder.csi.openstack.org",
	"kubernetes.io/vsphere-volume": "csi.vsphere.vmware.com",
}

// checkVolumeSnapshotClass refuses a class that cannot take and prune the base backups
// of the store whose volumes carry cnpg.io/cluster=<cluster> in namespace -- or, when
// that store has no volumes yet, of a store the cluster's default StorageClass would
// provision.
func checkVolumeSnapshotClass(ctx context.Context, dyn dynamic.Interface, typed kubernetes.Interface,
	class, namespace, cluster string) error {
	if _, err := typed.Discovery().ServerResourcesForGroupVersion(volumeSnapshotClassGVR.GroupVersion().String()); err != nil {
		if apierrors.IsNotFound(err) {
			return fmt.Errorf("--backup-snapshot-class %s: this cluster serves no VolumeSnapshotClass API "+
				"(snapshot.storage.k8s.io/v1), so it has no CSI snapshot controller to take one. GKE and AKS "+
				"include it with their disk drivers; on EKS install the snapshot controller add-on. Or drop "+
				"--backup-snapshot-class to keep base backups in the backup store", class)
		}
		return fmt.Errorf("asking the cluster whether it serves VolumeSnapshotClasses: %w", err)
	}

	got, err := dyn.Resource(volumeSnapshotClassGVR).Get(ctx, class, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return fmt.Errorf("--backup-snapshot-class %s: VolumeSnapshotClass %q does not exist on this cluster "+
			"(it has: %s). Create one for the driver that provisions the database volumes, with "+
			"deletionPolicy Delete, or name one of those", class, class, existingSnapshotClasses(ctx, dyn))
	}
	if err != nil {
		return fmt.Errorf("reading VolumeSnapshotClass %s: %w", class, err)
	}
	driver, _ := got.Object["driver"].(string)
	policy, _ := got.Object["deletionPolicy"].(string)
	if policy != "Delete" {
		return fmt.Errorf("--backup-snapshot-class %s: VolumeSnapshotClass %q has deletionPolicy %q. Old "+
			"snapshots are removed by deleting them, and under anything but Delete the provider's snapshot "+
			"outlives that -- the recovery window would read as honoured while every snapshot was kept, "+
			"and paid for. Use a class with deletionPolicy Delete", class, class, policy)
	}

	volumes, where, err := databaseVolumeDriver(ctx, typed, namespace, cluster)
	if err != nil {
		return fmt.Errorf("--backup-snapshot-class %s: %w; a snapshot class for another driver fails every "+
			"backup, so this is not guessed", class, err)
	}
	if volumes != driver {
		return fmt.Errorf("--backup-snapshot-class %s: VolumeSnapshotClass %q is for driver %q, but the "+
			"database volumes are provisioned by %q (%s). Every snapshot would fail. Use a class for %s",
			class, class, driver, volumes, where, volumes)
	}
	return nil
}

// snapshotRestoreNote is what a restore on a snapshot cluster says about the snapshots:
// that it does not use them, and what that costs.
//
// 🔴 RESTORE FROM A VOLUME SNAPSHOT IS NOT WIRED, and saying so where the restore is
// requested is what keeps "not wired" from reading as "used". dcctl recovers a store by
// building it anew -- there is deliberately no restore into a running install -- and a
// VolumeSnapshot, which CloudNativePG needs with its own annotations to recover a hot
// snapshot, lives in the namespace a destroy deletes. So every restore reads the backup
// store: the newest weekly base backup and the log since.
func snapshotRestoreNote(store string) string {
	return fmt.Sprintf("base backups on this cluster are volume snapshots, and a restore does not use them: "+
		"the %s recovers from the backup store, from its newest weekly base backup and the log since, so "+
		"it can replay up to a week of log", store)
}

// ValidateBackupSnapshotClass settles `--backup-snapshot-class` from argv, before any
// cluster is touched: a name no Kubernetes object can have, or a class on a cluster
// whose other flags leave it with no database backups to take as snapshots.
func ValidateBackupSnapshotClass(class string, backupsEnabled bool) error {
	if class == "" {
		return nil
	}
	if errs := validation.IsDNS1123Subdomain(class); len(errs) > 0 {
		return fmt.Errorf("--backup-snapshot-class %q is not a Kubernetes object name: %s", class,
			strings.Join(errs, "; "))
	}
	if !backupsEnabled {
		return fmt.Errorf("--backup-snapshot-class takes the databases' base backups as volume snapshots, " +
			"but this combination of flags leaves the cluster with no database backups (--no-cnpg removes " +
			"the operator the backup plugin extends; --compact with --no-tls drops the cert-manager it " +
			"needs). Drop the flag, or drop whichever of those turned backups off")
	}
	return nil
}

// precheckSnapshotClass is the bootstrap's half: the class the install record names,
// against this instance's event store (which has no volumes yet, so the cluster's
// default StorageClass decides the driver). Nothing to check when the record names
// none.
func precheckSnapshotClass(ctx context.Context, st *State) error {
	class := backupSnapshotClass(st)
	if class == "" {
		return nil
	}
	dyn, typed, err := snapshotClassClients(st.KubeContext)
	if err != nil {
		return fmt.Errorf("connecting to the cluster to check VolumeSnapshotClass %s: %w", class, err)
	}
	return checkVolumeSnapshotClass(ctx, dyn, typed, class, InstanceNamespace(st.Instance), TsdbClusterName)
}

// snapshotClassClients is the seam the bootstrap preflight reaches the cluster through,
// so the STEP that runs it can be exercised, not only the check.
var snapshotClassClients = func(kubeContext string) (dynamic.Interface, kubernetes.Interface, error) {
	dyn, _, typed, err := kubeClients(kubeContext)
	if err != nil {
		return nil, nil, err
	}
	return dyn, typed, nil
}

// existingSnapshotClasses names the classes the cluster has, for the refusal of one it
// does not. A failure to list says so rather than claiming there are none.
func existingSnapshotClasses(ctx context.Context, dyn dynamic.Interface) string {
	list, err := dyn.Resource(volumeSnapshotClassGVR).List(ctx, metav1.ListOptions{})
	if err != nil {
		return "could not list them: " + err.Error()
	}
	var names []string
	for _, c := range list.Items {
		d, _ := c.Object["driver"].(string)
		names = append(names, fmt.Sprintf("%s (driver %s)", c.GetName(), d))
	}
	if len(names) == 0 {
		return "none"
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}

// databaseVolumeDriver is the CSI driver that provisions the store's volumes, and a
// description of where that was read from.
//
// The store's own volumes when it has any: a bound volume names its driver outright,
// whatever class it came from. A new store -- every bootstrap's event store, and the
// relational store on a first install -- has none yet, and gets the cluster's default
// StorageClass, because dcctl names no class for either.
func databaseVolumeDriver(ctx context.Context, typed kubernetes.Interface, namespace, cluster string) (string, string, error) {
	pvcs, err := typed.CoreV1().PersistentVolumeClaims(namespace).List(ctx, metav1.ListOptions{
		LabelSelector: "cnpg.io/cluster=" + cluster,
	})
	if err != nil && !apierrors.IsNotFound(err) {
		return "", "", fmt.Errorf("listing the volumes of %s/%s: %w", namespace, cluster, err)
	}
	if err == nil && len(pvcs.Items) > 0 {
		drivers := map[string][]string{}
		for i := range pvcs.Items {
			d, err := claimDriver(ctx, typed, &pvcs.Items[i])
			if err != nil {
				return "", "", err
			}
			drivers[d] = append(drivers[d], pvcs.Items[i].Name)
		}
		if len(drivers) > 1 {
			var parts []string
			for d, claims := range drivers {
				parts = append(parts, fmt.Sprintf("%s: %s", d, strings.Join(claims, ", ")))
			}
			sort.Strings(parts)
			return "", "", fmt.Errorf("the volumes of %s/%s belong to more than one driver (%s), and one "+
				"class belongs to one", namespace, cluster, strings.Join(parts, "; "))
		}
		for d := range drivers {
			return d, fmt.Sprintf("the volumes of %s/%s", namespace, cluster), nil
		}
	}

	classes, err := typed.StorageV1().StorageClasses().List(ctx, metav1.ListOptions{})
	if err != nil {
		return "", "", fmt.Errorf("listing StorageClasses: %w", err)
	}
	var defaults []string
	var provisioner string
	for _, sc := range classes.Items {
		a := sc.GetAnnotations()
		if a["storageclass.kubernetes.io/is-default-class"] == "true" ||
			a["storageclass.beta.kubernetes.io/is-default-class"] == "true" {
			defaults = append(defaults, sc.Name)
			provisioner = sc.Provisioner
		}
	}
	switch len(defaults) {
	case 0:
		return "", "", fmt.Errorf("%s/%s has no volumes yet and the cluster has no default StorageClass, "+
			"so which driver will provision them cannot be told", namespace, cluster)
	case 1:
		return csiDriver(provisioner), "the default StorageClass " + defaults[0], nil
	default:
		sort.Strings(defaults)
		return "", "", fmt.Errorf("%s/%s has no volumes yet and the cluster marks %d StorageClasses as "+
			"default (%s), so which one will provision them cannot be told", namespace, cluster,
			len(defaults), strings.Join(defaults, ", "))
	}
}

// claimDriver is the CSI driver behind one claim: its bound volume's, or its class's.
func claimDriver(ctx context.Context, typed kubernetes.Interface, pvc *corev1.PersistentVolumeClaim) (string, error) {
	if pvc.Spec.VolumeName != "" {
		pv, err := typed.CoreV1().PersistentVolumes().Get(ctx, pvc.Spec.VolumeName, metav1.GetOptions{})
		if err != nil {
			return "", fmt.Errorf("reading volume %s of claim %s/%s: %w", pvc.Spec.VolumeName, pvc.Namespace, pvc.Name, err)
		}
		switch src := pv.Spec.PersistentVolumeSource; {
		case src.CSI != nil:
			return src.CSI.Driver, nil
		case src.GCEPersistentDisk != nil:
			return migratedProvisioners["kubernetes.io/gce-pd"], nil
		case src.AWSElasticBlockStore != nil:
			return migratedProvisioners["kubernetes.io/aws-ebs"], nil
		case src.AzureDisk != nil:
			return migratedProvisioners["kubernetes.io/azure-disk"], nil
		}
	}
	if pvc.Spec.StorageClassName == nil || *pvc.Spec.StorageClassName == "" {
		return "", fmt.Errorf("claim %s/%s names no StorageClass and is bound to no CSI volume", pvc.Namespace, pvc.Name)
	}
	sc, err := typed.StorageV1().StorageClasses().Get(ctx, *pvc.Spec.StorageClassName, metav1.GetOptions{})
	if err != nil {
		return "", fmt.Errorf("reading StorageClass %s of claim %s/%s: %w", *pvc.Spec.StorageClassName,
			pvc.Namespace, pvc.Name, err)
	}
	return csiDriver(sc.Provisioner), nil
}

// csiDriver maps an in-tree provisioner name to the CSI driver that serves it under
// migration, and leaves any other name as it is.
func csiDriver(provisioner string) string {
	if d, ok := migratedProvisioners[provisioner]; ok {
		return d
	}
	return provisioner
}
