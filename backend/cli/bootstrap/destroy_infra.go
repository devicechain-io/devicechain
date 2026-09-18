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
	"slices"
	"strings"
	"time"

	assets "github.com/devicechain-io/dc-deploy"
	"github.com/fatih/color"
	"github.com/hashicorp/terraform-exec/tfexec"
	"helm.sh/helm/v3/pkg/action"
	"helm.sh/helm/v3/pkg/cli"
	"helm.sh/helm/v3/pkg/release"
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
	tf, err := newTofuExec(rootdir, tofuBin)
	if err != nil {
		return err
	}
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
// Every step below tolerates having already happened, because a destroy is re-run
// precisely when the last one died: the release may be gone (the uninstall ignores
// not-found), the state entry removed (state rm runs only when listed), the rest
// destroyed (a destroy over an emptied state is "No changes"). The chart uninstall
// before this is resumable for the same reason — see resolveForeignRelease.
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
	namespace, name, listed := InstanceNamespace(instance), tsdbClusterName, false
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
		tfexec.Var("instance_namespace="+InstanceNamespace(instance)),
	); err != nil {
		return fmt.Errorf("tofu destroy: %w", err)
	}
	return nil
}

// checkDestroyFences runs the refusals that apply to a destroy — only the pre-split one.
// See destroyInstanceRoot for why the other fences of the apply path are absent.
//
// 🔴 THE SECOND LAYER, NOT THE FIRST. refuseUndestroyableInstance reads the same
// addresses from the state FILE before the prompt, the lock and the chart uninstall,
// which is where a refusal can still say nothing has been removed. This one runs after
// the chart is gone, over the state tofu actually reads, and says so.
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
	return preSplitDestroyRefusal(instance, found, true)
}

// preSplitDestroyRefusal is the refusal to run `tofu destroy` over a state that still
// holds the cluster prerequisites. releaseUninstalled says whether the chart release
// is already gone, because the sentence about what has been removed must be true at
// the point it is printed.
//
// 🔑 THE MESSAGE SAYS EXACTLY WHAT --without-state LEAVES, not merely that it exists: an
// instance this old also predates per-instance namespaces, so its broker and event store
// are in the shared namespace, outside anything the override removes.
func preSplitDestroyRefusal(instance string, found []string, releaseUninstalled bool) error {
	removed := "Nothing has been removed."
	if releaseUninstalled {
		removed = "The instance's chart release has already been uninstalled, but tofu destroy has NOT run: " +
			"nothing this state describes has been removed."
	}
	return fmt.Errorf(
		"instance %q was built by a dcctl that kept the cluster prerequisites in this instance's own "+
			"infrastructure state, so `tofu destroy` over it would DESTROY them — %d such resource(s):\n  %s\n"+
			"That includes the shared relational database, which holds data for every instance on this "+
			"cluster, and the object store holding every backup archive.\n"+
			"%s To remove this instance WITHOUT running tofu destroy, re-run with --without-state: it "+
			"uninstalls the instance's chart release, drops its database and login where the cluster has "+
			"a per-instance one (and says so where it does not), deletes its namespace, and removes its local "+
			"state — including this infrastructure state, the only record of the resources above. Everything "+
			"that state describes is LEFT on the cluster: the prerequisites, the shared relational database "+
			"and, for an instance built before each instance had its own namespace, its broker and event "+
			"store in %s. To remove those as well, delete and recreate the cluster",
		instance, len(found), strings.Join(found, "\n  "), removed, infraNamespace)
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
	// Interruptible only in what it SAYS, for the reason the chart release's is: see
	// awaitUninstall.
	res, err := awaitUninstall(ctx, os.Stdout, kubeContext, func() (*release.UninstallReleaseResponse, error) {
		return un.Run(name)
	})
	if err != nil {
		return false, err
	}
	return res != nil, nil
}

// instanceRootState is what the instance root's local state holds, read from the file.
type instanceRootState struct {
	// Resources counts what `tofu destroy` would remove from the cluster: managed
	// resources with an instance, terraform_data excluded.
	Resources int
	// PreSplit lists the preSplitStateAddresses the state holds.
	PreSplit []string
}

// readInstanceRootState reads the instance root's local state. A missing state is
// empty.
//
// 🔑 READ FROM THE FILE, NOT THROUGH tofu, BECAUSE IT RUNS BEFORE ANYTHING IS CHANGED.
// It decides whether a destroy may proceed at all, and extracting and initialising a
// root to answer that would write into the instance directory before the operator has
// even confirmed. Both places a state has lived are read: an instance from before the
// roots moved down a directory keeps it one level up until the next tofu run moves it.
//
// Fails closed: a state that cannot be parsed is an error, never "empty".
func readInstanceRootState(instance string) (instanceRootState, error) {
	var out instanceRootState
	dir, err := instanceRoot(instance)
	if err != nil {
		return out, err
	}
	for _, p := range []string{
		filepath.Join(dir, "infra", assets.InstanceRootDir, "terraform.tfstate"),
		filepath.Join(dir, "infra", "terraform.tfstate"),
	} {
		b, err := os.ReadFile(p)
		if errors.Is(err, os.ErrNotExist) {
			continue
		} else if err != nil {
			return instanceRootState{}, fmt.Errorf("reading %s: %w", p, err)
		}
		doc, err := parseStateDocument(b)
		if err != nil {
			return instanceRootState{}, fmt.Errorf("reading %s: %w", p, err)
		}
		out.Resources += doc.managed()
		held := doc.addresses()
		for _, address := range preSplitStateAddresses {
			if slices.Contains(held, address) && !slices.Contains(out.PreSplit, address) {
				out.PreSplit = append(out.PreSplit, address)
			}
		}
	}
	return out, nil
}

// stateDocument is the part of a state file this package reads without tofu.
type stateDocument struct {
	Resources []struct {
		Module    string `json:"module"`
		Mode      string `json:"mode"`
		Type      string `json:"type"`
		Name      string `json:"name"`
		Instances []struct {
			IndexKey json.RawMessage `json:"index_key"`
		} `json:"instances"`
	} `json:"resources"`
}

func parseStateDocument(state []byte) (stateDocument, error) {
	var doc stateDocument
	if len(strings.TrimSpace(string(state))) == 0 {
		return doc, nil
	}
	if err := json.Unmarshal(state, &doc); err != nil {
		return doc, fmt.Errorf("parsing the state: %w", err)
	}
	return doc, nil
}

// managed counts managed resources with at least one instance — what `tofu destroy`
// would remove from the cluster.
//
// 🔴 terraform_data IS NOT COUNTED. It is a state-only resource with no object in the
// cluster (see deliberatelyNotFenced), so a state holding nothing but the plan-time
// guards describes nothing running — and counting them let such a state skip the
// probe for a broker or event store nothing in state describes. Data sources are not
// infrastructure either, and a resource with no instances is nothing tofu would destroy.
func (d stateDocument) managed() int {
	n := 0
	for _, r := range d.Resources {
		if r.Mode == "managed" && r.Type != "terraform_data" && len(r.Instances) > 0 {
			n++
		}
	}
	return n
}

// addresses renders every resource instance's address the way `tofu state list` and
// preSplitStateAddresses spell it: module path, `data.` for data sources, and the index
// key — `[0]` for count, `["key"]` for for_each.
func (d stateDocument) addresses() []string {
	var out []string
	for _, r := range d.Resources {
		base := r.Type + "." + r.Name
		if r.Mode == "data" {
			base = "data." + base
		}
		if r.Module != "" {
			base = r.Module + "." + base
		}
		for _, inst := range r.Instances {
			key := strings.TrimSpace(string(inst.IndexKey))
			if key == "" || key == "null" {
				out = append(out, base)
				continue
			}
			out = append(out, base+"["+key+"]")
		}
	}
	return out
}

// liveInstanceInfrastructure reports what of the instance root is running — in the
// instance's namespace, or for the broker and event store in the shared one — for a
// destroy that has no state describing it.
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
	var found []string

	// 🔴 AND THE SHARED NAMESPACE, FOR THE BROKER AND EVENT STORE THEMSELVES. An instance
	// built before each instance had its own namespace runs both in it, under the same
	// names, and only such an instance can put them there — the cluster root installs
	// neither. Asked only in the instance's own namespace, a lost state over one of those
	// instances found nothing, and the destroy closed green over a broker and event store
	// it had never touched.
	//
	// 🔴 AND THE UNPREFIXED NAMESPACE, WHICH IS THAT SAME MISTAKE ONE GENERATION LATER. An
	// instance built before instance namespaces carried a prefix runs its broker and event
	// store in the bare id. Without asking there, such an instance with a lost state passes
	// this refusal, is told its state lists nothing, and has its namespace deleted with no
	// `tofu destroy` ever run — which is exactly what `--without-state` means, granted
	// without the operator having asked for it. Label-gated in instanceNamespaceCandidates,
	// so a namespace that merely shares the name is never probed as this instance's.
	// Computed once and used by both sweeps below; append never shares this slice's array
	// with the loop's, which would make the second sweep's list depend on the first's.
	candidates := instanceNamespaceCandidates(ctx, typed, instance)
	for _, where := range append(append([]string{}, candidates...), infraNamespace) {
		sts, err := typed.AppsV1().StatefulSets(where).Get(ctx, natsStatefulSetName, metav1.GetOptions{})
		switch {
		case apierrors.IsNotFound(err):
		case err != nil:
			return nil, fmt.Errorf("reading StatefulSet %s/%s: %w", where, natsStatefulSetName, err)
		case live(&sts.ObjectMeta):
			found = append(found, fmt.Sprintf("StatefulSet %s/%s", where, natsStatefulSetName))
		}

		cl, err := dyn.Resource(clusterGVR).Namespace(where).Get(ctx, tsdbClusterName, metav1.GetOptions{})
		switch {
		case err != nil && (apierrors.IsNotFound(err) || meta.IsNoMatchError(err)):
		case err != nil:
			return nil, fmt.Errorf("reading Cluster %s/%s: %w", where, tsdbClusterName, err)
		case cl.GetDeletionTimestamp() == nil:
			found = append(found, fmt.Sprintf("CloudNativePG Cluster %s/%s", where, tsdbClusterName))
		}
	}

	// The release RECORDS of the broker and the event store live in the namespace their
	// workloads do — unlike the instance chart's, which is kept in `default`. So they are
	// looked for in the same candidates, and for the same reason: a pre-prefix instance
	// keeps both where it was built.
	for _, release := range []string{natsReleaseName, tsdbClusterName} {
		for _, ns := range candidates {
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
	}
	return found, nil
}

// live is an object that is not already being deleted.
func live(m *metav1.ObjectMeta) bool { return m.DeletionTimestamp == nil }

// probeLiveInstanceInfrastructure is the seam Destroy reaches the cluster through for the
// empty-state refusal.
var probeLiveInstanceInfrastructure = func(ctx context.Context, kubeContext, instance string) ([]string, error) {
	dyn, typed, err := teardownClients(kubeContext)
	if err != nil {
		return nil, fmt.Errorf("connecting to the cluster: %w", err)
	}
	return liveInstanceInfrastructure(ctx, typed, dyn, instance)
}

// destroyInstanceInfrastructure and teardownClients are the seams uninstallInstance
// reaches `tofu destroy` and the cluster through.
//
// 🔴 WITHOUT THEM THE ONLY THING UNDER TEST IS THE STEPS, NOT THE SEQUENCE. Each step is
// a correct function whether or not uninstallInstance calls it, and a call behind a tofu
// binary or a live API server is one no unit test reaches: a destroy that stopped
// running tofu destroy, or stopped waiting for the namespace, broke nothing any test
// could see. TestUninstallInstanceRunsEveryStepInOrder drives the sequence through these.
var (
	destroyInstanceInfrastructure = destroyInstanceRoot
	teardownClients               = func(kubeContext string) (dynamic.Interface, kubernetes.Interface, error) {
		dyn, _, typed, err := kubeClients(kubeContext)
		if err != nil {
			return nil, nil, err
		}
		return dyn, typed, nil
	}
)

// refuseUndestroyableInstance runs every refusal a destroy can meet before anything is
// changed, from one read of the state file, and reports whether that state lists
// anything — which is what decides whether `tofu destroy` has work to do.
//
//   - a state that cannot be read: what tofu destroy would remove is unknown.
//   - a state still holding the cluster prerequisites: tofu destroy would take them.
//   - a missing or empty state over a running broker or event store: tofu destroy would
//     remove nothing and the destroy would report a teardown it never performed.
func refuseUndestroyableInstance(ctx context.Context, kubeContext, instance string) (stateHasResources bool, err error) {
	st, err := readInstanceRootState(instance)
	if err != nil {
		return false, fmt.Errorf("%w\n  The instance's infrastructure state cannot be read, so what `tofu destroy` would "+
			"remove is unknown. Nothing has been removed. If you know the state is lost, re-run with "+
			"--without-state to remove the instance by its release, database, login and namespace instead", err)
	}
	if len(st.PreSplit) > 0 {
		return false, preSplitDestroyRefusal(instance, st.PreSplit, false)
	}
	if st.Resources > 0 {
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
			"and namespace, and says that tofu destroy was skipped. Anything listed above outside namespace %s "+
			"(an instance built before each instance had its own namespace runs its broker and event store in %s) "+
			"is not in that namespace and is left running",
		instance, strings.Join(found, "\n  "), InstanceNamespace(instance), infraNamespace)
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
//
// 🔴 A FAILED READ IS NOT AN ANSWER EITHER WAY. An API server that blips mid-wait —
// a leader election, a dropped connection — says nothing about the namespace, so the
// error is recorded and the wait goes on to its deadline. Returning on it misreported a
// one-second blip as a namespace "still terminating after 10m0s", and dropped the error
// that said what actually happened.
func waitForNamespaceGone(ctx context.Context, typed kubernetes.Interface, name string, timeout, every time.Duration) error {
	start := time.Now()
	var (
		last    *corev1.Namespace
		lastErr error
	)
	err := wait.PollUntilContextTimeout(ctx, every, timeout, true, func(ctx context.Context) (bool, error) {
		ns, err := typed.CoreV1().Namespaces().Get(ctx, name, metav1.GetOptions{})
		switch {
		case apierrors.IsNotFound(err):
			return true, nil
		case err != nil:
			lastErr = err
			return false, nil
		}
		last, lastErr = ns, nil
		return false, nil
	})
	if err == nil {
		return nil
	}
	elapsed := time.Since(start)
	if elapsed >= time.Second {
		elapsed = elapsed.Round(time.Second)
	} else {
		elapsed = elapsed.Round(time.Millisecond)
	}
	const kept = "Local state has been kept; once whatever holds the namespace is resolved, run the same destroy again"
	// The CALLER's context ended — an interrupt, not a namespace that would not go.
	if cerr := ctx.Err(); cerr != nil {
		return fmt.Errorf("waiting for namespace %q to be deleted was interrupted after %s: %w\n%s",
			name, elapsed, cerr, kept)
	}
	if last == nil {
		if lastErr == nil {
			lastErr = err
		}
		return fmt.Errorf("namespace %q could not be confirmed deleted within %s: every read of it failed, "+
			"the last with: %w\n%s", name, elapsed, lastErr, kept)
	}
	var why []string
	for _, c := range last.Status.Conditions {
		why = append(why, fmt.Sprintf("%s=%s (%s): %s", c.Type, c.Status, c.Reason, c.Message))
	}
	if len(why) == 0 {
		why = append(why, "no conditions reported")
	}
	if lastErr != nil {
		return fmt.Errorf("namespace %q was still %s when last read, and after %s the latest read failed: %w\n  %s\n%s",
			name, namespacePhase(last), elapsed, lastErr, strings.Join(why, "\n  "), kept)
	}
	return fmt.Errorf("namespace %q was still %s after %s:\n  %s\n%s",
		name, namespacePhase(last), elapsed, strings.Join(why, "\n  "), kept)
}

// namespacePhase names a namespace's phase for an error, never as an empty string.
func namespacePhase(ns *corev1.Namespace) string {
	if ns.Status.Phase == "" {
		return "present"
	}
	return strings.ToLower(string(ns.Status.Phase))
}

// withoutStateWarning names what --without-state leaves unmanaged when the state it
// skips was not in fact empty — or could not be read at all.
func withoutStateWarning(instance string, st instanceRootState, readErr error) string {
	if readErr != nil {
		return color.YellowString(
			"--without-state: tofu destroy will be SKIPPED, and the local infrastructure state of %q cannot be "+
				"read (%v), so what it describes is unknown. Anything of it outside the instance namespace is left "+
				"running, and this state is removed with the instance.", instance, readErr)
	}
	if len(st.PreSplit) > 0 {
		return color.YellowString(
			"--without-state: tofu destroy will be SKIPPED. The local infrastructure state of %q still holds the "+
				"cluster prerequisites (%s); they, the shared relational database and — for an instance built before "+
				"each instance had its own namespace — its broker and event store in %s are LEFT on the cluster, and "+
				"this state, the only record of them, is removed with the instance.",
			instance, strings.Join(st.PreSplit, ", "), infraNamespace)
	}
	return color.YellowString(
		"--without-state: tofu destroy will be SKIPPED, but the local infrastructure state of %q lists %d "+
			"resource(s). They are removed only if they live in the instance namespace; anything elsewhere is "+
			"left running, and this state — the only record of it — is removed with the instance.",
		instance, st.Resources)
}
