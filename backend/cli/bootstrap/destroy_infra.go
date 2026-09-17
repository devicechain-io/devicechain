// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	assets "github.com/devicechain-io/dc-deploy"
	"github.com/fatih/color"
	"github.com/hashicorp/terraform-exec/tfexec"
	"helm.sh/helm/v3/pkg/action"
	"helm.sh/helm/v3/pkg/cli"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
)

// tsdbReleaseAddress is the one instance-root resource carrying prevent_destroy.
//
// 🔴 THE LITERAL STAYS IN THE CONFIGURATION, SO DESTROY GOES AROUND IT. The event
// store's release is guarded on every apply path — the cnpg-cluster module shares the
// block with the relational store — and a lifecycle literal cannot be switched per
// command. So a destroy takes the release out of state and uninstalls it itself, then
// runs a plain `tofu destroy` over what is left: the one route measured to work on both
// tofu 1.9 and terraform while every apply stays guarded (ADR-080).
const tsdbReleaseAddress = "module.cnpg_tsdb.helm_release.cluster"

// destroyTofu is what the instance-root teardown needs from tofu, narrowed so the
// sequence can be driven without a binary.
type destroyTofu interface {
	stateLister
	StateRm(ctx context.Context, address string, opts ...tfexec.StateRmCmdOption) error
	Destroy(ctx context.Context, opts ...tfexec.DestroyOption) error
}

// providerReleaseUninstaller removes a Helm release OpenTofu's provider created, and
// reports whether there was one. The seam over uninstallProviderRelease.
type providerReleaseUninstaller func(ctx context.Context, kubeContext, namespace, name string) (bool, error)

// destroyInstanceRoot runs `tofu destroy` over the instance's own root: the broker,
// the event store and the guards beside them.
//
// 🔴 IT DOES NOT OPEN THE ROOT THROUGH openInstanceRoot, AND REUSING IT WOULD LOOP.
// Three of that function's fences — retired infrastructure, the pre-namespace move, a
// broker shrink — refuse an APPLY whose remedy is "destroy the instance and bootstrap
// it again". Run here, they would refuse the destroy they prescribe, and the operator
// would be told to run the command that just refused them. Worse, they would be wrong:
// an instance built before namespaces keeps its broker and event store in the shared
// namespace, and destroying what its state holds is exactly what removes them.
//
// 🔑 THE PRE-SPLIT FENCE IS THE ONE THAT STAYS, BECAUSE ITS HAZARD IS THE SAME UNDER
// DESTROY. A state from before the cluster prerequisites got their own root still holds
// them — the shared relational database among them — and `tofu destroy` destroys what
// state holds. See destroyOpenedInstanceRoot.
func destroyInstanceRoot(ctx context.Context, kubeContext, instance string) (err error) {
	tofuBin, err := findTofu()
	if err != nil {
		return err
	}
	workdir, err := instanceStateDir(instance, "infra")
	if err != nil {
		return err
	}
	rootdir := filepath.Join(workdir, assets.InstanceRootDir)
	// A destroy writes state as it goes, and a failed one leaves the resources it did
	// not reach in cleartext — the same reason openInstanceRoot hardens on every exit.
	defer func() {
		if herr := hardenStateFiles(rootdir); herr != nil && err == nil {
			err = herr
		}
	}()
	if err := extractRoot(assets.OpenTofu(), assets.InstanceRootDir, workdir); err != nil {
		return fmt.Errorf("extracting infrastructure config: %w", err)
	}
	// Same order as the apply path, for the same reason: before any tofu call, or the
	// fence below reads an empty state and the destroy finds nothing to destroy.
	if err := relocateRootState(workdir, rootdir); err != nil {
		return err
	}
	if err := removeSupersededRootConfig(workdir); err != nil {
		return err
	}
	tf, err := tfexec.NewTerraform(rootdir, tofuBin)
	if err != nil {
		return err
	}
	tf.SetStdout(os.Stdout)
	tf.SetStderr(os.Stderr)
	// See openInstanceRoot: an interrupted destroy of a slow release must get to write
	// its state, or the resume has nothing true to read.
	tf.SetWaitDelay(tofuGracefulStopBudget)
	if err := tf.Init(ctx); err != nil {
		return fmt.Errorf("tofu init: %w", err)
	}
	return destroyOpenedInstanceRoot(ctx, tf, kubeContext, instance, uninstallProviderRelease)
}

// destroyOpenedInstanceRoot is the teardown itself, over an initialised root.
//
// Every step tolerates having already happened, because a destroy is re-run precisely
// when the last one died: the release may be gone, the state entry removed, the rest
// destroyed.
func destroyOpenedInstanceRoot(ctx context.Context, tf destroyTofu, kubeContext, instance string, uninstall providerReleaseUninstaller) error {
	if err := checkDestroyFences(ctx, tf, instance); err != nil {
		return err
	}

	state, err := tf.Show(ctx)
	if err != nil {
		return fmt.Errorf("reading the infrastructure state: %w", err)
	}
	// Where the release is, read from the state while the state still says. An instance
	// built before each instance had a namespace keeps it in the shared one.
	namespace, name, listed := instanceNamespace(instance), tsdbClusterName, false
	if state != nil && state.Values != nil {
		if r := findStateResource(state.Values.RootModule, tsdbReleaseAddress); r != nil {
			listed = true
			if ns, _ := r.AttributeValues["namespace"].(string); ns != "" {
				namespace = ns
			}
			if n, _ := r.AttributeValues["name"].(string); n != "" {
				name = n
			}
		}
	}

	// 🔴 UNINSTALL BEFORE THE STATE ENTRY GOES, NOT AFTER. The entry is the only record of
	// where the release lives; removed first, a destroy that died in between would come
	// back knowing only the default namespace. In this order the re-run still reads the
	// entry, finds the release already gone, and finishes the removal.
	if _, err := uninstall(ctx, kubeContext, namespace, name); err != nil {
		return fmt.Errorf("uninstalling the event store release %s/%s: %w", namespace, name, err)
	}
	// 🔴 ONLY WHEN LISTED: `state rm` of an address the state does not hold exits 1
	// (measured), which would fail every resumed destroy at this line.
	if listed {
		if err := tf.StateRm(ctx, tsdbReleaseAddress); err != nil {
			return fmt.Errorf("removing %s from the infrastructure state: %w", tsdbReleaseAddress, err)
		}
	}

	// 🔴 instance_namespace HAS NO DEFAULT, and omitting it fails the destroy (measured).
	// The two variables are what a destroy plan needs to reach the cluster; nothing else
	// this run decides changes what the state says to remove.
	if err := tf.Destroy(ctx,
		tfexec.Var("kubeconfig_context="+kubeContext),
		tfexec.Var("instance_namespace="+instanceNamespace(instance)),
	); err != nil {
		return fmt.Errorf("tofu destroy: %w", err)
	}
	return nil
}

// checkDestroyFences runs the refusals that apply to a destroy — only the pre-split one.
// See destroyInstanceRoot for why the other fences of the apply path are absent.
//
// 🔴 A STATE-ONLY SIGNATURE ON PURPOSE. checkHaNotTornDown needs the requested topology
// and the outputs; taking only a stateLister here means it cannot be added by accident.
func checkDestroyFences(ctx context.Context, tf stateLister, instance string) error {
	found, err := stateAddressesPresent(ctx, tf, preSplitStateAddresses)
	if err != nil {
		return err
	}
	if len(found) == 0 {
		return nil
	}
	return fmt.Errorf(
		"instance %q was built by a dcctl that kept the cluster prerequisites in this instance's own "+
			"infrastructure state, so `tofu destroy` over it would DESTROY them — %d such resource(s):\n  %s\n"+
			"That includes the shared relational database, which holds data for every instance on this "+
			"cluster, and the object store holding every backup archive.\n"+
			"Nothing has been removed yet. To remove this instance WITHOUT running tofu destroy, re-run with "+
			"--without-state: it uninstalls the instance release, drops its database and login, and deletes "+
			"its namespace, leaving the prerequisites on the cluster (the local state that describes them is "+
			"removed with the instance)",
		instance, len(found), strings.Join(found, "\n  "))
}

// uninstallProviderRelease removes a release the instance root's Helm provider created.
//
// 🔴 NOT uninstallRelease. That one refuses any release that does not carry dcctl's
// instance attribution in its values, which is right for the chart release it guards —
// and a release OpenTofu's provider created carries no such stamp, so it would be
// refused every time. The name and namespace here come from the instance's own state or
// its own namespace, which is the attribution.
func uninstallProviderRelease(ctx context.Context, kubeContext, namespace, name string) (bool, error) {
	settings := cli.New()
	settings.KubeContext = kubeContext
	settings.SetNamespace(namespace)
	cfg := new(action.Configuration)
	if err := cfg.Init(settings.RESTClientGetter(), namespace, "secret", func(string, ...interface{}) {}); err != nil {
		return false, err
	}
	un := action.NewUninstall(cfg)
	un.Wait = true
	un.Timeout = helmTimeout
	// A release already gone is a resumed destroy, not a failure.
	un.IgnoreNotFound = true
	res, err := un.Run(name)
	if err != nil {
		return false, err
	}
	return res != nil, nil
}

// instanceRootStateResources counts the managed resources in the instance root's local
// state. A missing state is zero.
//
// 🔑 READ FROM THE FILE, NOT THROUGH tofu, BECAUSE IT RUNS BEFORE ANYTHING IS CHANGED.
// It decides whether a destroy may proceed at all, and extracting and initialising a
// root to answer that would write into the instance directory before the operator has
// even confirmed. Both places a state has lived are read: an instance from before the
// roots moved down a directory keeps it one level up until the next tofu run moves it.
//
// Fails closed: a state that cannot be parsed is an error, never "empty".
func instanceRootStateResources(instance string) (int, error) {
	dir, err := instanceRoot(instance)
	if err != nil {
		return 0, err
	}
	total := 0
	for _, p := range []string{
		filepath.Join(dir, "infra", assets.InstanceRootDir, "terraform.tfstate"),
		filepath.Join(dir, "infra", "terraform.tfstate"),
	} {
		b, err := os.ReadFile(p)
		if errors.Is(err, os.ErrNotExist) {
			continue
		} else if err != nil {
			return 0, fmt.Errorf("reading %s: %w", p, err)
		}
		n, err := managedResourcesIn(b)
		if err != nil {
			return 0, fmt.Errorf("reading %s: %w", p, err)
		}
		total += n
	}
	return total, nil
}

// managedResourcesIn counts managed resources with at least one instance in a state
// document. Data sources are not infrastructure, and a resource with no instances is
// nothing tofu would destroy.
func managedResourcesIn(state []byte) (int, error) {
	if len(strings.TrimSpace(string(state))) == 0 {
		return 0, nil
	}
	var doc struct {
		Resources []struct {
			Mode      string            `json:"mode"`
			Instances []json.RawMessage `json:"instances"`
		} `json:"resources"`
	}
	if err := json.Unmarshal(state, &doc); err != nil {
		return 0, fmt.Errorf("parsing the state: %w", err)
	}
	n := 0
	for _, r := range doc.Resources {
		if r.Mode == "managed" && len(r.Instances) > 0 {
			n++
		}
	}
	return n, nil
}

// liveInstanceInfrastructure reports what of the instance root is running in the
// instance's namespace, for a destroy that has no state describing it.
//
// 🔴 WHY AN EMPTY STATE IS NOT PERMISSION. With nothing in state, `tofu destroy` removes
// nothing and says so successfully; the namespace delete would then take the broker and
// event store anyway, and the destroy would report a teardown tofu never performed —
// with the one record that could have explained the difference already gone. So a
// missing or empty state over a running broker or event store is refused, and the
// operator who knows why says so with --without-state.
//
// 🔑 WHAT COUNTS AS ABSENT is what a legitimate resume leaves behind. An object already
// being deleted is on its way out; a missing CloudNativePG CRD means no event store can
// exist (the dynamic client reports a missing TYPE as the same 404 as a missing object,
// and here both readings are "absent"). PVCs are never asked: they outlive their
// workloads by design and prove nothing about what is running.
func liveInstanceInfrastructure(ctx context.Context, typed kubernetes.Interface, dyn dynamic.Interface, instance string) ([]string, error) {
	ns := instanceNamespace(instance)
	var found []string

	sts, err := typed.AppsV1().StatefulSets(ns).Get(ctx, natsStatefulSetName, metav1.GetOptions{})
	switch {
	case apierrors.IsNotFound(err):
	case err != nil:
		return nil, fmt.Errorf("reading StatefulSet %s/%s: %w", ns, natsStatefulSetName, err)
	case live(&sts.ObjectMeta):
		found = append(found, fmt.Sprintf("StatefulSet %s/%s", ns, natsStatefulSetName))
	}

	cl, err := dyn.Resource(clusterGVR).Namespace(ns).Get(ctx, tsdbClusterName, metav1.GetOptions{})
	switch {
	case err != nil && (apierrors.IsNotFound(err) || meta.IsNoMatchError(err)):
	case err != nil:
		return nil, fmt.Errorf("reading Cluster %s/%s: %w", ns, tsdbClusterName, err)
	case cl.GetDeletionTimestamp() == nil:
		found = append(found, fmt.Sprintf("CloudNativePG Cluster %s/%s", ns, tsdbClusterName))
	}

	for _, release := range []string{natsReleaseName, tsdbClusterName} {
		secrets, err := typed.CoreV1().Secrets(ns).List(ctx, metav1.ListOptions{
			LabelSelector: "owner=helm,name=" + release,
		})
		if err != nil {
			return nil, fmt.Errorf("listing Helm release records for %s in %s: %w", release, ns, err)
		}
		for i := range secrets.Items {
			if live(&secrets.Items[i].ObjectMeta) {
				found = append(found, fmt.Sprintf("Helm release %s/%s", ns, release))
				break
			}
		}
	}
	return found, nil
}

// live is an object that is not already being deleted.
func live(m *metav1.ObjectMeta) bool { return m.DeletionTimestamp == nil }

// probeLiveInstanceInfrastructure is the seam Destroy reaches the cluster through for the
// empty-state refusal.
var probeLiveInstanceInfrastructure = func(ctx context.Context, kubeContext, instance string) ([]string, error) {
	dyn, _, typed, err := kubeClients(kubeContext)
	if err != nil {
		return nil, fmt.Errorf("connecting to the cluster: %w", err)
	}
	return liveInstanceInfrastructure(ctx, typed, dyn, instance)
}

// refuseStatelessLiveInstance is the empty-state refusal, and it runs before anything
// is changed. It reports whether the instance root's state lists anything — which is
// what decides whether `tofu destroy` has work to do.
func refuseStatelessLiveInstance(ctx context.Context, kubeContext, instance string) (stateHasResources bool, err error) {
	n, err := instanceRootStateResources(instance)
	if err != nil {
		return false, fmt.Errorf("%w\n  The instance's infrastructure state cannot be read, so what `tofu destroy` would "+
			"remove is unknown. Nothing has been removed. If you know the state is lost, re-run with "+
			"--without-state to remove the instance by its release, database, login and namespace instead", err)
	}
	if n > 0 {
		return true, nil
	}
	found, err := probeLiveInstanceInfrastructure(ctx, kubeContext, instance)
	if err != nil {
		return false, fmt.Errorf("checking for a running broker or event store with no state describing them: %w", err)
	}
	if len(found) == 0 {
		return false, nil
	}
	return false, fmt.Errorf(
		"instance %q has no infrastructure state on this machine, but its infrastructure is running:\n  %s\n"+
			"`tofu destroy` over an empty state removes nothing, so this destroy would not be the teardown it "+
			"reports. Nothing has been removed. If the state is lost — or this instance was built on another "+
			"machine — re-run with --without-state: it removes the instance by its release, database, login "+
			"and namespace, and says that tofu destroy was skipped",
		instance, strings.Join(found, "\n  "))
}

// namespaceGoneTimeout bounds the wait for an instance namespace to finish deleting.
// Variables so tests need not wait ten minutes.
var (
	namespaceGoneTimeout  = 10 * time.Minute
	namespaceGonePollEach = 2 * time.Second
)

// waitForNamespaceGone waits until the namespace no longer exists.
//
// 🔴 A NAMESPACE DELETE RETURNS AT ONCE AND FINISHES LATER. Returning there let a
// bootstrap of the same id straight after a destroy meet a Terminating namespace, and
// let destroy remove the local state of an instance whose objects were still going.
// So destroy waits, and on timeout returns an error BEFORE the local state is removed:
// the state is what a re-run resumes from.
//
// 🔑 THE INSTANCE CR CANNOT DEADLOCK THIS. It carries dcctl's finalizer, but it is
// cluster-scoped, so it is not in the namespace and the namespace's deletion never waits
// on it.
func waitForNamespaceGone(ctx context.Context, typed kubernetes.Interface, name string, timeout, every time.Duration) error {
	var last *corev1.Namespace
	err := wait.PollUntilContextTimeout(ctx, every, timeout, true, func(ctx context.Context) (bool, error) {
		ns, err := typed.CoreV1().Namespaces().Get(ctx, name, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			return true, nil
		}
		if err != nil {
			return false, err
		}
		last = ns
		return false, nil
	})
	if err == nil {
		return nil
	}
	if last == nil || ctx.Err() != nil {
		return fmt.Errorf("waiting for namespace %q to be deleted: %w", name, err)
	}
	var why []string
	for _, c := range last.Status.Conditions {
		why = append(why, fmt.Sprintf("%s=%s (%s): %s", c.Type, c.Status, c.Reason, c.Message))
	}
	if len(why) == 0 {
		why = append(why, "no conditions reported")
	}
	return fmt.Errorf("namespace %q was still %s after %s:\n  %s\n"+
		"Local state has been kept; once whatever holds the namespace is resolved, run the same destroy again",
		name, namespacePhase(last), timeout, strings.Join(why, "\n  "))
}

// namespacePhase names a namespace's phase for an error, never as an empty string.
func namespacePhase(ns *corev1.Namespace) string {
	if ns.Status.Phase == "" {
		return "present"
	}
	return strings.ToLower(string(ns.Status.Phase))
}

// withoutStateWarning names what --without-state leaves unmanaged when the state it
// skips was not in fact empty.
func withoutStateWarning(instance string, resources int) string {
	return color.YellowString(
		"--without-state: tofu destroy will be SKIPPED, but the local infrastructure state of %q lists %d "+
			"resource(s). They are removed only if they live in the instance namespace; anything elsewhere is "+
			"left running, and this state — the only record of it — is removed with the instance.",
		instance, resources)
}
