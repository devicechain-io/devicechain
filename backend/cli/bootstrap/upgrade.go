// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	apply "github.com/devicechain-io/dc-k8s/apply"
	dck8s "github.com/devicechain-io/dc-k8s/config"
	"github.com/fatih/color"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// UpgradeOptions drives an operator upgrade. It reuses Options for the instance,
// kube-context and image-source fields; the rest of Options (profile, HA, TLS,
// escrow, the whole bring-up surface) is deliberately not consulted here — see
// Upgrade for why this command touches nothing but the operator.
type UpgradeOptions struct {
	Options
}

// Upgrade moves the cluster-scoped operator install — namespace, CRDs, RBAC and
// the controller Deployment — onto a target version.
//
// 🔴 THIS EXISTS BECAUSE THE DOCUMENTED UPGRADE CANNOT REACH THE OPERATOR. A
// release is one version across the service images, the chart, the operator and
// dcctl, and docs/docs/deployment/releases-and-upgrades.md says so. But the
// documented procedure is `helm upgrade`, and the operator is not in the chart:
// stepInstallCore applies it from manifests embedded in this binary, and before
// this command existed nothing moved it afterwards. An operator who followed the
// documentation exactly ended up with new services, an indefinitely old
// controller, and a promise that said otherwise.
//
// Re-running `bootstrap` was not the answer and must not become the workaround:
// it rotates every generated credential. The whole point of a separate verb is
// that it can be run on a live instance with nothing else at stake.
//
// What this deliberately does NOT do:
//
//   - It does not touch the Helm release. `helm upgrade` moves the services and
//     is documented; duplicating it here would give two commands that both claim
//     to upgrade an instance and disagree about what that means.
//   - It does not generate, read or rotate a single credential.
//   - It does not run the infrastructure apply.
//
// The whole rendered stream is applied, not just the Deployment's image. CRDs are
// in it, and they are the half with a trap: the API server prunes fields a
// structural schema does not declare, so an instance whose CRDs stayed at the
// version they were bootstrapped at would silently discard anything a later
// release added to them. Applying the stream costs nothing extra — RenderOperator
// already emits it as one document — and it means the CRD path is never the thing
// nobody remembered.
func Upgrade(ctx context.Context, provider Provider, opts UpgradeOptions) error {
	kubeContext := instanceContext(opts.Options)

	registry, version, err := resolveOperatorImageSource(opts.Options)
	if err != nil {
		return err
	}
	image := fmt.Sprintf("%s/%s:%s", registry, operatorImageName, version)

	fmt.Println(GreenUnderline(fmt.Sprintf(
		"\nUpgrade the operator for instance %q on provider %q", opts.Instance, provider.Name())))
	fmt.Printf("  %s %s\n", color.WhiteString("Context:"), color.GreenString(kubeContext))
	fmt.Printf("  %s %s\n", color.WhiteString("Operator:"), color.GreenString(image))

	manifests, err := dck8s.RenderOperator(image)
	if err != nil {
		return fmt.Errorf("rendering operator manifests: %w", err)
	}
	targets, err := operatorDeployments(manifests)
	if err != nil {
		return fmt.Errorf("reading the rendered operator manifests: %w", err)
	}
	if len(targets) == 0 {
		// Not a warning to print and continue past. This command's only
		// observable effect is a controller running new code; a stream with no
		// Deployment in it would apply cleanly, report success, and move nothing.
		return fmt.Errorf(
			"reading the rendered operator manifests: the operator overlay rendered no Deployment, " +
				"so there is nothing to upgrade and reporting success would be false; " +
				"the overlay at backend/k8s/config was changed")
	}

	if opts.DryRun {
		fmt.Println()
		for _, t := range targets {
			wouldDo(fmt.Sprintf("apply CRDs/RBAC and set %s/%s to %s", t.namespace, t.name, image))
		}
		return nil
	}

	dyn, disco, typed, err := kubeClients(kubeContext)
	if err != nil {
		return fmt.Errorf("building kube clients: %w", err)
	}

	// Read what is running BEFORE anything is applied. An upgrade that reports
	// only its destination cannot be told apart from a no-op, and "no-op" is the
	// answer an operator most wants confirmed on a cluster they are unsure about.
	before := currentOperatorImages(ctx, typed, targets)

	doing("applying operator manifests (CRDs + RBAC + controller)")
	if err := apply.NewApplyOptions(dyn, disco).WithServerSide(true).Apply(ctx, manifests); err != nil {
		return fail("applying operator manifests", err)
	}
	done()

	doing("waiting for the controller to roll over")
	if err := waitForRollout(ctx, typed, targets, 5*time.Minute); err != nil {
		return fail("waiting for the controller", err)
	}
	done()

	fmt.Println(color.HiGreenString("\nOperator upgraded."))
	for _, t := range targets {
		was := before[t.String()]
		switch {
		case was == "":
			fmt.Printf("  %s %s\n", color.WhiteString(t.String()+":"), color.GreenString(image))
		case was == image:
			fmt.Printf("  %s %s %s\n", color.WhiteString(t.String()+":"), color.GreenString(image),
				color.YellowString("(already at this version — re-applied)"))
		default:
			fmt.Printf("  %s %s → %s\n", color.WhiteString(t.String()+":"),
				color.YellowString(was), color.GreenString(image))
		}
	}
	// Named because this command is half of a procedure and the other half is the
	// one that moves the data path. An operator who runs only this has upgraded a
	// controller and nothing else.
	fmt.Println(color.WhiteString(
		"\nThis moved the operator only. The services are upgraded by `helm upgrade` — see\n" +
			"docs/docs/deployment/releases-and-upgrades.md for the full procedure."))
	return nil
}

// resolveOperatorImageSource settles the registry and tag the operator image is
// pulled from, applying exactly the rules bootstrap applies to the services so
// the two cannot drift into naming different things by default.
//
// 🔴 THE UNPUBLISHED-VERSION REFUSAL IS THE LOAD-BEARING PART. A locally built
// dcctl carries DefaultImageVersion "dev", which names no image in any registry;
// deploying it manifests as an ImagePullBackOff on a controller nobody is
// watching, leaving the cluster on the old operator while this command reported
// success. Refusing here is the difference between a failed upgrade and a silent
// one.
func resolveOperatorImageSource(opts Options) (registry, version string, err error) {
	registry, version = opts.ImageRegistry, opts.ImageVersion
	if registry == "" {
		registry = DefaultImageRegistry
	}
	if version == "" {
		version = DefaultImageVersion
	}
	// The empty check is not defensive padding: `version` reaches here empty only
	// if DefaultImageVersion is itself empty, which is what a broken ldflags stamp
	// produces — and IsUnpublishedImageVersion does NOT catch it, because "" is
	// neither "dev" nor a dev stamp. Without this the command renders
	// "…/operator:" with no tag at all, which Kubernetes reads as :latest.
	if version == "" || IsUnpublishedImageVersion(version) {
		// fmt.Errorf, not fail(): fail() prints a red "failed." meant to close out
		// an in-flight doing() line, and nothing has been started yet here. It
		// rendered a bare "failed." above the message with no step to attach to.
		return "", "", fmt.Errorf(
			"resolving the operator image: this dcctl build has no pinned image version "+
				"(%q names no published image); pass --version <tag> to name the release you are "+
				"upgrading to, or --registry/--version together to point at images you built yourself", version)
	}
	return registry, version, nil
}

// deploymentRef names one Deployment in the rendered stream.
type deploymentRef struct{ namespace, name string }

func (d deploymentRef) String() string { return d.namespace + "/" + d.name }

// operatorDeployments picks the Deployments out of the rendered operator stream.
//
// Read from the manifests rather than hard-coded, for the reason the overlay is
// rendered at all: the namespace ("dc-k8s-system") and the name prefix ("dc-k8s-")
// are kustomize settings, and a copy of them here would be a second place to
// remember. A rename would then leave this command applying the new manifests and
// waiting on a Deployment that no longer exists — which reads as a hung upgrade,
// not as a stale constant.
func operatorDeployments(manifests []byte) ([]deploymentRef, error) {
	objs, err := apply.Decode(manifests)
	if err != nil {
		return nil, err
	}
	var refs []deploymentRef
	for _, o := range objs {
		if o.GetKind() != "Deployment" {
			continue
		}
		ns := o.GetNamespace()
		if ns == "" {
			// A Deployment is namespaced, so an empty namespace here means the
			// overlay stopped setting one and the object would land in whatever
			// namespace the client's context happens to name. Refuse rather than
			// guess: guessing writes a controller into someone else's namespace.
			return nil, fmt.Errorf("the rendered Deployment %q declares no namespace", o.GetName())
		}
		refs = append(refs, deploymentRef{namespace: ns, name: o.GetName()})
	}
	sort.Slice(refs, func(i, j int) bool { return refs[i].String() < refs[j].String() })
	return refs, nil
}

// currentOperatorImages reads the container images each target Deployment runs
// right now, joined when a Deployment has more than one container. A target that
// does not exist yet, or cannot be read, is simply absent from the map — this is
// reporting, and a failure to read it must not fail an upgrade that then went on
// to work.
func currentOperatorImages(ctx context.Context, typed kubernetes.Interface, targets []deploymentRef) map[string]string {
	out := make(map[string]string, len(targets))
	for _, t := range targets {
		d, err := typed.AppsV1().Deployments(t.namespace).Get(ctx, t.name, metav1.GetOptions{})
		if err != nil {
			continue
		}
		var images []string
		for _, c := range d.Spec.Template.Spec.Containers {
			images = append(images, c.Image)
		}
		out[t.String()] = strings.Join(images, ", ")
	}
	return out
}

// waitForRollout blocks until every target Deployment has fully rolled onto its
// new pod template.
//
// 🔴 "AVAILABLE REPLICAS >= DESIRED" IS NOT A ROLLOUT CHECK AND MUST NOT BE USED
// HERE. It is true of the OLD pods, continuously, from before the apply until
// well into the rollout — so a wait written that way returns immediately and
// every time, and the command reports an upgrade that has not happened yet. The
// four conditions below are the ones `kubectl rollout status` uses, and each
// rules out a different way of passing early:
//
//   - ObservedGeneration >= Generation: the controller has SEEN the new template.
//     Without it the three status counts below are still describing the old one.
//   - UpdatedReplicas == desired: every replica has been recreated on it.
//   - Replicas == UpdatedReplicas: no old pod is still running.
//   - AvailableReplicas >= UpdatedReplicas: the new ones actually came up.
const rolloutPollInterval = 3 * time.Second

func waitForRollout(ctx context.Context, typed kubernetes.Interface, targets []deploymentRef, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		pending := ""
		for _, t := range targets {
			d, err := typed.AppsV1().Deployments(t.namespace).Get(ctx, t.name, metav1.GetOptions{})
			if err != nil {
				pending = fmt.Sprintf("%s (%v)", t, err)
				break
			}
			desired := int32(1)
			if d.Spec.Replicas != nil {
				desired = *d.Spec.Replicas
			}
			s := d.Status
			if s.ObservedGeneration >= d.Generation &&
				s.UpdatedReplicas == desired &&
				s.Replicas == s.UpdatedReplicas &&
				s.AvailableReplicas >= s.UpdatedReplicas {
				continue
			}
			pending = fmt.Sprintf("%s (%d/%d updated, %d available)",
				t, s.UpdatedReplicas, desired, s.AvailableReplicas)
			break
		}
		if pending == "" {
			return nil
		}
		// The deadline is checked before sleeping, and the sleep is clamped to
		// what is left of it. A fixed poll interval slept first would overshoot
		// the caller's timeout by up to a whole interval — harmless at five
		// minutes, but it makes a short timeout unusable and it means the value
		// this function reports as the limit is not the limit it enforced.
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return fmt.Errorf("the operator did not finish rolling over within %s: %s", timeout, pending)
		}
		wait := rolloutPollInterval
		if remaining < wait {
			wait = remaining
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(wait):
		}
	}
}
