// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"crypto/sha256"
	"fmt"

	"github.com/fatih/color"
	"helm.sh/helm/v3/pkg/chart"
	"helm.sh/helm/v3/pkg/kube"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// The key the instance configuration document travels under, inside the Secret every
// pod mounts at /etc/dci-config. Restated as a literal because it is a contract with
// the services' own loader (core/microservice.go) and with DeployedInstanceConfig,
// neither of which shares a constant with this file.
const instanceConfigSecretKey = "instance"

// 🔴 HELM'S OWNERSHIP METADATA, RESTATED. These four values are what
// action.checkOwnership requires before a chart may adopt an object that already
// exists (helm.sh/helm/v3 pkg/action/validate.go). They are UNEXPORTED there, so
// there is nothing to import; restating them is the only option.
//
// Being wrong about them is loud rather than silent, which is what makes that
// acceptable: a namespace missing the metadata is refused by Helm on the first
// install with "exists and cannot be imported into the current release: invalid
// ownership metadata", naming the exact keys it wanted. There is no path where a
// stale value here produces a working install that quietly owns the wrong thing.
const (
	helmManagedByLabel       = "app.kubernetes.io/managed-by"
	helmManagedByValue       = "Helm"
	helmReleaseNameAnno      = "meta.helm.sh/release-name"
	helmReleaseNamespaceAnno = "meta.helm.sh/release-namespace"
)

// instanceConfigSecretName is the name the instance configuration Secret must have.
//
// 🔴 IT IS NOT A PREFERENCE. DeployedInstanceConfig reads the document back by this
// exact name to decide whether an instance already exists, and reads a NotFound as
// "fresh install, mint everything" — so a Secret under any other name does not fail,
// it makes the next bootstrap rotate the root key and every database and broker
// credential out from under a live instance. The chart refuses any other name for
// the same reason (devicechain.validateInstanceConfigSource).
func instanceConfigSecretName(instance string) string {
	return fmt.Sprintf("dci-%s-config", instance)
}

// instanceConfigChecksum is the digest the chart stamps on every pod template so a
// configuration change actually rolls the workloads.
//
// 🔑 IT MUST AGREE WITH THE CHART'S OWN, BYTE FOR BYTE, and that is a property worth
// naming rather than a coincidence. With inline config the chart computes
// `include "devicechain.instanceConfig" . | sha256sum` over the document it is about
// to write; this computes the same hash over the same bytes, because the bytes came
// from that same render. So moving an instance from inline config to a Secret dcctl
// owns changes no annotation and rolls no pods — the switch is invisible to the
// workloads, which is the only honest way to make it.
func instanceConfigChecksum(doc []byte) string {
	return fmt.Sprintf("%x", sha256.Sum256(doc))
}

// composeInstanceConfig produces the instance configuration document for this run by
// RENDERING THE CHART, and returns the bytes.
//
// 🔴 IT DOES NOT ASSEMBLE THE DOCUMENT. Writing a second author for it here would be
// a reimplementation of devicechain.instanceConfig — a template that injects the
// shutdown budget from the top-level values, drops the ai-inference coordinates when
// that area is not deployed, and refuses an install with no secret-store root key.
// Two authors of one document drift, and the drift is invisible: both produce valid
// JSON, and the services accept either. So the chart stays the author and dcctl
// becomes the WRITER, which is a much smaller claim.
//
// It is also what keeps the chart's authoring-time guards alive. Under
// instance.existingSecret they are all switched off at once, because a template that
// is not rendered raises nothing. Rendering the inline form here, before the install
// values are derived, runs every one of them against the values this run actually
// carries — the root-key refusal included.
//
// The values passed in must be the AUTHORING values (instance.config present,
// instance.existingSecret absent). A nil document means they were not: the chart
// rendered no Secret, and continuing would write an empty document over a live one.
func composeInstanceConfig(ctx context.Context, ch *chart.Chart, authoring map[string]interface{}) ([]byte, error) {
	doc, err := renderInstanceConfigDocument(ctx, ch, authoring)
	if err != nil {
		return nil, fmt.Errorf("composing the instance configuration: %w", err)
	}
	if doc == nil {
		return nil, fmt.Errorf("the chart rendered no instance configuration document from the " +
			"authoring values, which happens only when instance.existingSecret is already set on " +
			"them; dcctl must compose the document from an inline config before it points the " +
			"release at a Secret")
	}
	return doc, nil
}

// externalConfigCoordinates are the values templates read out of instance.config and
// cannot read once the document lives in a Secret the chart cannot see.
//
// 🔴 THE CHART DOES NOT FALL BACK TO NOTHING THERE, IT FALLS BACK TO ITS DEFAULTS.
// values.schema.json makes instance.config required, so under an external Secret the
// block is present and wrong rather than absent — and both consumers fail silently
// on a wrong value: a NetworkPolicy port that does not match blocks the services'
// own egress (which presents as a broker or database outage), and a PodMonitor
// pointed at the wrong namespace collects nothing and reports success.
type externalConfigCoordinates struct {
	// NatsHost is the broker's <service>.<namespace> hostname, whose second label is
	// the namespace the PodMonitor selects.
	NatsHost string
	// NatsPort and RdbPort are the two egress ports the NetworkPolicy opens.
	NatsPort int
	RdbPort  int
}

// readConfigCoordinates takes them from the document itself, through the services'
// own loader.
//
// Reading them from the DOCUMENT rather than from the state that produced it is the
// point: these values exist to tell the chart what the pods are about to be
// configured with, and the pods are configured with this document. Deriving them
// from anything else would be a second answer to the same question, which is the
// shape that ends up disagreeing.
func readConfigCoordinates(doc []byte) (externalConfigCoordinates, error) {
	loaded, err := loadInstanceConfigDocument(doc)
	if err != nil {
		return externalConfigCoordinates{}, err
	}

	// The broker hostname and port are validated by the loader itself (a blank
	// hostname or a zero port is a configuration the services refuse to start on), so
	// reaching here means both are real.
	out := externalConfigCoordinates{
		NatsHost: loaded.Infrastructure.Nats.Hostname,
		NatsPort: int(loaded.Infrastructure.Nats.Port),
	}

	// The relational store's port is not: persistence datastores carry an untyped
	// configuration map, so nothing upstream of here judges it. An absent or
	// unreadable port would restate as 0, and a NetworkPolicy opening port 0 blocks
	// every connection to the database while rendering, applying and reporting
	// success — the exact silent failure the restatement exists to prevent. Refuse.
	port, ok := intFromConfigMap(loaded.Persistence.Rdb.Configuration, "port")
	if !ok {
		return externalConfigCoordinates{}, fmt.Errorf(
			"the instance configuration does not carry a usable persistence.rdb.configuration.port "+
				"(got %v). The chart cannot read the document it mounts, so this port has to be "+
				"restated to it as networkPolicy.externalConfigPorts.rdb, and restating a zero would "+
				"render a policy that blocks the services' own database egress",
			loaded.Persistence.Rdb.Configuration["port"])
	}
	out.RdbPort = port
	return out, nil
}

// intFromConfigMap reads a positive integer out of an untyped configuration map.
// JSON numbers arrive as float64 and YAML ones as int, so both are accepted; a
// missing key, a wrong type or a non-positive value all answer "no", because every
// one of them would restate as a port that silently blocks traffic.
func intFromConfigMap(m map[string]interface{}, key string) (int, bool) {
	var n int
	switch v := m[key].(type) {
	case float64:
		n = int(v)
	case int:
		n = v
	case int64:
		n = int(v)
	default:
		return 0, false
	}
	if n <= 0 {
		return 0, false
	}
	return n, true
}

// installValuesFor turns the values that AUTHORED the document into the values the
// release is actually installed with.
//
// 🔴 THIS IS THE WHOLE POINT OF THE SLICE, IN ONE FUNCTION. The authoring values
// carry the instance's credentials — the secret-store root key, the broker's service
// and system passwords, the callout issuer seed, the cross-service secret — inside
// instance.config. Helm records the values of every revision it keeps, so installing
// with them puts each of those credentials into the release record, where rotating
// one does not retract it: the previous revision still holds the previous value,
// readable by exactly whoever could read the new one. Removing the block is what
// makes the release hold a NAME instead.
//
// Three values go in where it came out:
//
//   - instance.existingSecret, the name of the Secret dcctl wrote.
//   - instance.existingSecretChecksum, the digest that keeps pods rolling when the
//     document changes. The chart cannot hash what it cannot read.
//   - the coordinates the templates read out of instance.config and would otherwise
//     take from the chart's defaults. See externalConfigCoordinates.
//
// It copies rather than mutates: the authoring map is what the document was rendered
// from, and a caller that re-rendered it afterwards must get the same bytes.
func installValuesFor(authoring map[string]interface{}, instance string, doc []byte) (map[string]interface{}, error) {
	coords, err := readConfigCoordinates(doc)
	if err != nil {
		return nil, err
	}

	vals := map[string]interface{}{}
	for k, v := range authoring {
		vals[k] = v
	}

	// instance, minus the config, plus the reference. Rebuilt rather than edited in
	// place so the authoring map's own sub-map is left alone.
	inst := map[string]interface{}{}
	if src, ok := authoring["instance"].(map[string]interface{}); ok {
		for k, v := range src {
			if k == "config" {
				continue
			}
			inst[k] = v
		}
	}
	inst["existingSecret"] = instanceConfigSecretName(instance)
	inst["existingSecretChecksum"] = instanceConfigChecksum(doc)
	vals["instance"] = inst

	// 🔑 RESTATED WHETHER OR NOT THIS RUN'S TOP-LEVEL TOGGLES CONSUME THEM. The chart
	// demands metrics.natsBrokerHost only when the PodMonitor is on, and
	// networkPolicy.externalConfigPorts only when the policy is on — and dcctl turns
	// the policy off today. Writing only what is currently demanded would leave the
	// day somebody adds a --network-policy flag as the day the egress rule silently
	// reverts to the chart's default ports. One rule instead: dcctl restates every
	// coordinate the chart can no longer read.
	//
	// blobStorage.persistence.mountPath is the one that is deliberately NOT here. It
	// is the same kind of escape hatch, but it is only legal alongside a volume: the
	// chart refuses a mount path with no volume to mount, and refuses a blob
	// directory with no volume behind it. The document carries the chart's default —
	// no directory, hence no store — so there is nothing to restate, and restating
	// an empty path would be writing a value with no meaning.
	vals["metrics"] = mergedStringKeyMap(authoring["metrics"], map[string]interface{}{
		"natsBrokerHost": coords.NatsHost,
	})
	vals["networkPolicy"] = mergedStringKeyMap(authoring["networkPolicy"], map[string]interface{}{
		"externalConfigPorts": map[string]interface{}{
			"nats": coords.NatsPort,
			"rdb":  coords.RdbPort,
		},
	})

	return vals, nil
}

// mergedStringKeyMap returns base (when it is a map) with over laid on top, without
// touching base.
func mergedStringKeyMap(base interface{}, over map[string]interface{}) map[string]interface{} {
	out := map[string]interface{}{}
	if m, ok := base.(map[string]interface{}); ok {
		for k, v := range m {
			out[k] = v
		}
	}
	for k, v := range over {
		out[k] = v
	}
	return out
}

// instanceConfigSecret is the Secret the composed document travels in.
//
// It carries the chart's own instance label so a namespace-wide read sees the same
// thing whichever side wrote it, and nothing else: the ownership annotations are
// writeOwnedSecret's business, and a label inviting selection over "everything dcctl
// owns" is one typo from bulk-deleting exactly what cannot be regenerated.
// 🔴 IT CARRIES helm.sh/resource-policy: keep, AND WITHOUT IT THIS WHOLE SLICE
// DELETES THE CONFIGURATION OF EVERY INSTANCE THAT ALREADY EXISTS.
//
// An instance installed before this change has the config Secret in its release
// manifest, because the chart rendered it. Pointing the release at
// instance.existingSecret takes it OUT of the manifest, and Helm 3 deletes what
// leaves the manifest — so the upgrade that follows this write would delete the very
// Secret it just wrote, leaving every pod with nothing to mount. Helm reads the
// policy off the LIVE object at delete time (kube.Client.Update refreshes each
// candidate with info.Get() before checking), which is what makes an annotation
// written here effective against a manifest written by an earlier release.
//
// It stays on every subsequent write too, fresh installs included. The annotation
// costs nothing where nothing would delete the Secret, and the case it guards is the
// one nobody re-checks: some later release rendering this name again.
func instanceConfigSecret(instance string, doc []byte) ownedSecret {
	return ownedSecret{
		Name:        instanceConfigSecretName(instance),
		Namespace:   instance,
		Type:        corev1.SecretTypeOpaque,
		Labels:      map[string]string{"devicechain.io/instance": instance},
		Annotations: map[string]string{kube.ResourcePolicyAnno: kube.KeepPolicy},
		Data:        map[string]string{instanceConfigSecretKey: string(doc)},
	}
}

// adoptChartWrittenInstanceConfig takes over the config Secret on an instance whose
// chart wrote it, so this run can keep writing it.
//
// 🔴 WITHOUT THIS, THE FIRST RUN OF THIS DCCTL AGAINST ANY EXISTING INSTANCE STOPS
// DEAD. writeOwnedSecret refuses anything carrying no dcctl ownership, which is
// exactly right for a credential somebody else authored — and exactly wrong here,
// where the author is the chart of the release this same run is about to upgrade.
// The alternative was to fence the old shape out and tell operators to destroy and
// rebuild, which for this object costs them their data to solve a bookkeeping
// problem.
//
// 🔑 AND IT IS STILL REACHED, WHICH IS WORTH SAYING BECAUSE IT NEARLY WAS NOT.
// checkNoRetiredInfrastructure refuses every instance dcctl built before the
// credentials moved — so if that were the only population with a chart-written
// Secret, this function would be unreachable and should be deleted. It is not: an
// instance installed with plain `helm install dc deploy/helm/devicechain`, which is
// what the published documentation tells an operator to run, has no OpenTofu state
// for the fence to key on and arrives here with exactly the Secret below. See the
// fence for the full answer, and the two tests that keep it honest.
//
// 🔑 WHAT MAKES IT AN IDENTIFICATION RATHER THAN A GUESS: the Secret is claimed only
// when Kubernetes' own record says it belongs to the release being upgraded — Helm
// stamps app.kubernetes.io/managed-by and the two meta.helm.sh annotations on
// everything it applies. A Secret carrying no ownership at all, or another release's,
// is left exactly as found for writeOwnedSecret to refuse with its own message. This
// is not "adopt what looks familiar"; it is "adopt what the cluster says is ours".
//
// The minted-at stamp is taken from the object's own creation timestamp rather than
// from now(). The credential came into existence when the chart wrote it, and a
// stamp that said otherwise would report an adoption as a rotation — which is the one
// question the stamp exists to answer.
func adoptChartWrittenInstanceConfig(
	ctx context.Context,
	typed kubernetes.Interface,
	instance, instanceUID, releaseName, releaseNamespace string,
) error {
	api := typed.CoreV1().Secrets(instance)
	existing, err := api.Get(ctx, instanceConfigSecretName(instance), metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		// A fresh install. Nothing to take over.
		return nil
	} else if err != nil {
		return fmt.Errorf("reading the existing instance configuration Secret %s/%s: %w",
			instance, instanceConfigSecretName(instance), err)
	}

	a := existing.GetAnnotations()
	if a[annotationManagedBy] == managedByDcctl {
		// Already ours. writeOwnedSecret does the rest, including refusing a Secret
		// left behind by a previous instance of this name.
		return nil
	}
	if existing.GetLabels()[helmManagedByLabel] != helmManagedByValue ||
		a[helmReleaseNameAnno] != releaseName ||
		a[helmReleaseNamespaceAnno] != releaseNamespace {
		// Not the chart's, as far as the cluster is concerned. Leave it alone: the
		// writer's refusal names a real problem and says more about it than anything
		// this function could add.
		return nil
	}

	adopted := existing.DeepCopy()
	if adopted.Annotations == nil {
		adopted.Annotations = map[string]string{}
	}
	adopted.Annotations[annotationManagedBy] = managedByDcctl
	adopted.Annotations[annotationOwnerName] = instance
	adopted.Annotations[annotationOwnerUID] = instanceUID
	adopted.Annotations[annotationMintedAt] = existing.CreationTimestamp.UTC().Format(mintedAtTimeFormat)
	// 🔑 helm.sh/resource-policy IS DELIBERATELY NOT SET HERE. Ownership and the keep
	// policy look like one act, and writing both here reads as thorough — but the only
	// caller writes the Secret through writeOwnedSecret one line later, which sets the
	// policy from the spec on every write, fresh installs included. A second copy in
	// this function can never be the one that matters, and a line that cannot matter is
	// worse than no line: it invites the reader to believe this function is what keeps
	// the document alive. instanceConfigSecret is.
	if _, err := api.Update(ctx, adopted, metav1.UpdateOptions{}); err != nil {
		return fmt.Errorf("taking over the instance configuration Secret %s/%s from the chart: %w",
			instance, existing.Name, err)
	}
	return nil
}

// ensureNamespaceForRelease creates the instance namespace, if it is not there, in a
// shape the chart can adopt.
//
// 🔴 SOMETHING HAS TO CREATE IT BEFORE THE INSTALL, AND IT CANNOT BE THE CHART. The
// document now lives in a Secret dcctl writes, that Secret lives in the instance
// namespace, and the pods mount it at container start — so it has to exist before
// Helm creates the workloads that wait on it. The chart still renders the namespace
// (instance.createNamespace), and it must keep rendering it: Helm 3 DELETES a
// resource that leaves the manifest, so removing it from the chart on an instance
// that already exists would cascade-delete the whole namespace.
//
// 🔑 SO OWNERSHIP DOES NOT MOVE — only the timing does. The namespace is created
// carrying the metadata Helm requires to adopt an object it finds already there
// (app.kubernetes.io/managed-by=Helm plus the two meta.helm.sh annotations), so the
// install takes it into the release exactly as if the chart had created it, and
// `helm uninstall` still deletes it and everything in it. Without that metadata the
// install refuses outright, which is Helm protecting a namespace it did not make.
//
// An existing namespace is left alone. Whether the chart may adopt it is Helm's
// judgement and it already makes it, with a better message than anything this could
// say; stamping our metadata onto a namespace somebody else is using would be
// claiming it rather than checking it.
func ensureNamespaceForRelease(
	ctx context.Context,
	typed kubernetes.Interface,
	instance, releaseName, releaseNamespace string,
) error {
	api := typed.CoreV1().Namespaces()
	existing, err := api.Get(ctx, instance, metav1.GetOptions{})
	switch {
	case err == nil:
		// 🔑 A NAMESPACE ON ITS WAY OUT IS NOT A NAMESPACE. Kubernetes refuses new
		// content in a terminating namespace, so the Secret write below would fail with
		// "unable to create new content in namespace ... because it is being
		// terminated" — which reads as a defect rather than as a destroy that has not
		// finished. Teardown waits on finalizers and is not instant, so this is exactly
		// what an operator who runs bootstrap straight after destroy hits.
		if existing.DeletionTimestamp != nil {
			return fmt.Errorf("namespace %q is still being deleted, so this instance cannot be "+
				"built into it yet: a previous `dcctl destroy` has not finished. Wait for the "+
				"namespace to go and run this again", instance)
		}
		return nil
	case !apierrors.IsNotFound(err):
		return fmt.Errorf("reading namespace %q before writing the instance configuration: %w",
			instance, err)
	}

	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{
		Name: instance,
		Labels: map[string]string{
			"devicechain.io/instance": instance,
			helmManagedByLabel:        helmManagedByValue,
		},
		Annotations: map[string]string{
			helmReleaseNameAnno:      releaseName,
			helmReleaseNamespaceAnno: releaseNamespace,
		},
	}}
	if _, err := api.Create(ctx, ns, metav1.CreateOptions{}); err != nil {
		if apierrors.IsAlreadyExists(err) {
			// Somebody created it between the read and the write. Nothing here wanted
			// to own it — only to be sure it exists — so this is success.
			return nil
		}
		return fmt.Errorf("creating namespace %q for the instance configuration: %w", instance, err)
	}
	return nil
}

// removeInstanceNamespace deletes the instance's namespace once its release is gone.
//
// 🔴 THE UNINSTALL DOES NOT ALWAYS REACH IT, AND WHAT IS LEFT BEHIND IS THE ROOT KEY.
// dcctl writes the instance configuration Secret — which carries the secret-store root
// key — BEFORE Helm installs anything, because the pods mount it at container start. A
// run that dies between those two leaves a namespace holding that document and no
// release to uninstall, and `helm uninstall` on a release that does not exist is a
// no-op destroy reports as success.
//
// 🔑 WHAT THAT COSTS IS A REMEDY THAT DOES NOT WORK. The next bootstrap of the same
// name finds the leftover Secret, sees it was minted for a declaration that is gone,
// refuses it, and tells the operator to run `dcctl destroy` — which they just did.
//
// So this makes the command do what its own dry run already says: "helm uninstall the
// instance release and delete namespace <instance>".
//
// Guarded on the chart's instance label, so what goes is a namespace this instance
// owns rather than one that merely shares its name. A namespace already gone, or one
// deleted between the read and the delete, is success: destroy is re-run precisely
// when something went wrong the first time.
func removeInstanceNamespace(ctx context.Context, typed kubernetes.Interface, instance string) error {
	api := typed.CoreV1().Namespaces()
	ns, err := api.Get(ctx, instance, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil
	} else if err != nil {
		return fmt.Errorf("reading namespace %q: %w", instance, err)
	}
	if ns.Labels["devicechain.io/instance"] != instance {
		fmt.Println(color.YellowString(
			"  namespace %q is not labelled as this instance's, so it was left alone; "+
				"anything dcctl wrote inside it is still there", instance))
		return nil
	}
	if err := api.Delete(ctx, instance, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("deleting namespace %q: %w", instance, err)
	}
	return nil
}
