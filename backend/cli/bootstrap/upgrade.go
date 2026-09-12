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
	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// UpgradeOptions drives an instance upgrade. It reuses Options for the instance,
// kube-context and image-source fields; the rest of Options — profile, HA, TLS, the
// whole bring-up surface — is deliberately not consulted, because an upgrade takes
// an instance's SHAPE from its declaration rather than from flags. See Upgrade.
type UpgradeOptions struct {
	Options
	// EscrowFile and EscrowPassphraseFile locate the instance's root-key escrow, so
	// an upgrade can check it still protects the key the instance is running on — and
	// write one where there is none.
	//
	// 🔴 THEY ARE HERE BECAUSE THE RE-RUN THAT USED TO DO THIS IS GONE. An instance
	// built with --no-escrow could gain an escrow by being bootstrapped again, and
	// every re-run checked an existing one. Bootstrap refuses to run against a live
	// instance now, so without these there is no supported way to give a running
	// instance a second copy of its root key. See reconcileUpgradeEscrow.
	EscrowFile           string
	EscrowPassphraseFile string
}

// Upgrade moves a live instance onto a release: the cluster-scoped operator
// install, the configuration document its services read, and the Helm release
// that runs them.
//
// 🔴 BOOTSTRAP CREATES, UPGRADE EVOLVES, AND THAT SPLIT IS THE POINT. They are not
// two spellings of one pipeline. A bootstrap composes an instance out of its
// arguments and mints every credential in it, because none of them exists yet. An
// upgrade composes a VERSION CHANGE over an instance that already exists, so it
// reads every credential back and mints none — a database owner's password was set
// when the Cluster was created and is reconciled by nothing afterwards, and
// generating a replacement is not an update but a break that reports success.
//
// 🔴 IT OWNS THE DOCUMENT BECAUSE NOTHING ELSE CAN ANY MORE. `templates/
// instance-config.yaml` renders the configuration document only `if not
// .Values.instance.existingSecret`, so from the moment dcctl became that Secret's
// author, `helm upgrade` — the documented way to move an instance — could move the
// pods and could no longer move the configuration they mount. A release that added
// a configuration field would have had no writer at all, silently: the pods keep
// reading the document they were bootstrapped with.
//
// What it deliberately does NOT do:
//
//   - It does not run the infrastructure apply. Two of that apply's inputs cannot
//     be recovered from the cluster — the endpoint and bucket names of an operator's
//     own backup destination, and the Grafana SSO client secret's cleartext — so an
//     upgrade that ran it would either demand them again every time or reconfigure
//     the instance without them. The apply joins this verb when the chart becomes an
//     OpenTofu release and the state lives in the cluster.
//   - It does not change an instance's shape. Profile, topology and areas come from
//     the declaration, not from flags here: this verb moves a version, and changing
//     what an instance IS is a different question with different answers (raising
//     replicas does not re-replicate streams that were created at one).
//
// The whole rendered stream is applied, not just the Deployment's image. CRDs are
// in it, and they are the half with a trap: the API server prunes fields a
// structural schema does not declare, so an instance whose CRDs stayed at the
// version they were bootstrapped at would silently discard anything a later
// release added to them. Applying the stream costs nothing extra — RenderOperator
// already emits it as one document — and it means the CRD path is never the thing
// nobody remembered.
func Upgrade(ctx context.Context, provider Provider, opts UpgradeOptions) error {
	// 🔴 THE SAME BINDING DESTROY USES, AND ANNOUNCED THE SAME WAY. `upgrade` carried the
	// identical defect: it derived kind-<instance> and so pointed at a cluster that does
	// not exist for any instance bootstrapped with --kube-context. Reading the record
	// fixes where it points; announcing the SOURCE is what stops a guess being presented
	// as knowledge, which is the half a silent fix would have left behind.
	binding, source := ResolveBinding(opts.Options)
	if err := refuseUnreadable(source, opts.Instance); err != nil {
		return err
	}
	announceBinding(binding, source, opts.Instance)
	kubeContext := binding.KubeContext

	dyn, disco, typed, err := kubeClients(kubeContext)
	if err != nil {
		return fmt.Errorf("building kube clients: %w", err)
	}

	// READ THE INSTANCE BEFORE DECIDING ANYTHING, INCLUDING UNDER --dry-run.
	//
	// 🔴 THE IMAGE SOURCE IS WHY THIS MOVED UP, AND THE BUG IT FIXES IS A SPLIT
	// UPGRADE. The operator's image used to be resolved from the flags alone, with
	// dcctl's published defaults filling the gaps, while the SERVICES took theirs
	// from the declaration. So `dcctl upgrade --version v1.3.0` against an instance
	// built from a local registry moved the controller to a ghcr.io image and the
	// services to a local one — two halves of a release that is defined as one
	// version across the images, the chart, the operator and this CLI.
	//
	// Reading first makes the declaration the DEFAULT for both halves and the flags
	// an override of both, so they cannot diverge. A dry run reads too: there is
	// nothing to rehearse about an instance without looking at it, and everything
	// here is a read.
	st, err := hydrateUpgradeState(ctx, typed, provider, binding, opts)
	if err != nil {
		return err
	}
	st.Evolving = true

	image := fmt.Sprintf("%s/%s:%s", st.ImageRegistry, operatorImageName, st.ImageVersion)

	fmt.Println(GreenUnderline(fmt.Sprintf(
		"\nUpgrade instance %q on provider %q", opts.Instance, provider.Name())))
	fmt.Printf("  %s %s\n", color.WhiteString("Context:"), color.GreenString(kubeContext))
	fmt.Printf("  %s %s\n", color.WhiteString("Operator:"), color.GreenString(image))
	fmt.Printf("  %s %s\n", color.WhiteString("Services:"),
		color.GreenString(fmt.Sprintf("%s/<area>:%s", st.ImageRegistry, st.ImageVersion)))

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
		wouldDo("recompose the instance configuration document from this release's chart, " +
			"keeping every credential the instance is running on")
		wouldDo("upgrade the instance's Helm release")
		return nil
	}

	// 🔴 UPGRADE TAKES THE CLUSTER LOCK TOO, AND FORGETTING IT WOULD HAVE LEFT A
	// HOLE SHAPED EXACTLY LIKE THIS COMMAND. The lock exists so that two operators
	// cannot mutate one cluster at once, and this command server-side-applies the
	// CRDs, the RBAC and the controller Deployment — cluster-scoped objects that a
	// concurrent bootstrap is also applying. A lock that only bootstrap and destroy
	// take is a lock with a documented bypass.
	//
	// It is announced and not enforced, for the same reason destroy's is: an
	// operator repairing a stuck instance must not be blocked by a lock held by
	// the very run that got stuck.
	upgradeClaim := beginUpgradeClaim(ctx, kubeContext, opts.Instance)
	defer func() {
		if upgradeClaim != nil {
			upgradeClaim.Release(ctx)
		}
	}()

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

	// THE SERVICES, AND THE DOCUMENT THEY READ.
	//
	// 🔴 THIS IS THE HALF `helm upgrade` CANNOT DO ANY MORE, AND IT IS WHY THIS
	// COMMAND GREW. Once the release points at a Secret dcctl owns, the chart stops
	// rendering the instance configuration document — `templates/instance-config.yaml`
	// is wrapped in `if not .Values.instance.existingSecret`. So the documented
	// upgrade could still move the pods and could no longer move the configuration
	// they mount, and a release that ADDED a configuration field would have no writer
	// at all. Nothing would error: the pods would keep reading the document they were
	// bootstrapped with.
	//
	// It runs AFTER the operator on purpose. The CRDs move with the operator, and a
	// service rolled onto a new version ahead of the schema it writes is the ordering
	// that fails; the reverse is a controller that briefly knows about a field
	// nothing is sending yet.
	// THE CERTIFICATE, BEFORE THE SERVICES ROLL.
	//
	// 🔴 PERIODIC MAINTENANCE BELONGS TO THE VERB THAT RUNS PERIODICALLY, AND THIS IS
	// THE ONLY ONE THERE IS. The broker's leaf is good for a year; the infrastructure
	// module that used to re-issue it inside its last thirty days was retired with the
	// credentials it also held, and nothing replaced that. An instance nobody upgrades
	// still expires, but an instance nobody upgrades is one nothing else was going to
	// help either — what this closes is the case where an operator does everything
	// they were told to and the broker stops accepting connections anyway, on the
	// anniversary of a bootstrap.
	//
	// Ahead of the release on purpose: the renewal restarts the broker, and doing that
	// while the services are mid-roll means two disruptions overlapping instead of
	// one finishing before the other starts.
	doing("checking the broker's certificate")
	if err := renewBrokerCertificate(ctx, typed, st); err != nil {
		return fail("renewing the broker's certificate", err)
	}
	done()

	if err := runStreamed("Upgrading the instance's services", "helm upgrade", func() error {
		return helmInstall(ctx, st)
	}); err != nil {
		return err
	}

	doing("waiting for the services to roll over")
	if err := waitForAreas(ctx, typed, st.Instance, areaReadyTimeout, areaReadyPollInterval); err != nil {
		return fail("waiting for the services", err)
	}
	done()

	fmt.Println(color.HiGreenString("\nInstance upgraded."))
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
	// 🔴 SAID OUT LOUD BECAUSE IT IS THE ONE THING THIS COMMAND NO LONGER LEAVES TO
	// SOMEBODY ELSE, AND THE ONE THING NOBODY WOULD CHECK. It used to close by naming
	// `helm upgrade` as the missing half; now it IS both halves, and the fact worth
	// stating is the one an operator would otherwise have to take on trust — that a
	// version change did not quietly become a credential change.
	reconcileUpgradeEscrow(st, st.Values["secretsRootKey"], opts)
	fmt.Println(color.WhiteString(
		"\nEvery credential this instance was running on was kept. An upgrade reads them;\n" +
			"it mints nothing, so nothing here rotated."))
	return nil
}

// resolveUpgradeImageSource settles the ONE image source both halves of this upgrade
// use: the declaration's, unless a flag overrode it.
//
// 🔴 IT IS ONE SOURCE BECAUSE A SPLIT ONE IS A SPLIT RELEASE. The operator's image was
// resolved from the flags alone, with dcctl's published defaults filling the gaps,
// while the services took theirs from the declaration — so an upgrade could move the
// controller to a published image and the services to a locally built one. A release
// is defined as one version across the images, the chart, the operator and this CLI.
//
// 🔴 THE UNPUBLISHED-VERSION REFUSAL IS THE LOAD-BEARING PART. A locally built dcctl
// carries DefaultImageVersion "dev", which names no image in any registry; deploying
// it manifests as an ImagePullBackOff on a controller nobody is watching, leaving the
// cluster on the old operator while this command reported success. Refusing here is
// the difference between a failed upgrade and a silent one.
//
// 🔴 IT DELEGATES RATHER THAN REPEATING. This and ResolveImageSource applied the same
// rules from two bodies once, and they had already drifted. Two resolvers that must
// agree are one resolver with two callers. What stays here is the MESSAGE, which is
// genuinely different — an upgrade names the release it is upgrading TO.
//
// fmt.Errorf, not fail(): fail() prints a red "failed." meant to close out an
// in-flight doing() line, and nothing has been started when this runs.
func resolveUpgradeImageSource(st *State) (ImageSource, error) {
	img, err := ResolveImageSource(st.ImageRegistry, st.ImageVersion, false)
	if err != nil {
		version := st.ImageVersion
		if version == "" {
			version = DefaultImageVersion
		}
		return ImageSource{}, fmt.Errorf(
			"resolving the images to upgrade to: this dcctl build has no pinned image version "+
				"(%q names no published image); pass --version <tag> to name the release you are "+
				"upgrading to, or --registry/--version together to point at images you built "+
				"yourself", version)
	}
	return img, nil
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

// deploymentRolledOut is that four-condition check, as one function, because two
// callers need it and a restatement is how the wrong one spreads. The naive form
// it replaces is not merely weaker — it is true of the pods the rollout is
// replacing, so a second copy written from memory reintroduces a check that
// passes before the work starts.
func deploymentRolledOut(d *appsv1.Deployment) bool {
	desired := int32(1)
	if d.Spec.Replicas != nil {
		desired = *d.Spec.Replicas
	}
	s := d.Status
	return s.ObservedGeneration >= d.Generation &&
		s.UpdatedReplicas == desired &&
		s.Replicas == s.UpdatedReplicas &&
		s.AvailableReplicas >= s.UpdatedReplicas
}

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
			if deploymentRolledOut(d) {
				continue
			}
			desired := int32(1)
			if d.Spec.Replicas != nil {
				desired = *d.Spec.Replicas
			}
			s := d.Status
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

// beginUpgradeClaim takes the cluster lock for an operator upgrade, warning rather
// than refusing when it cannot. See the call site for why it does not enforce.
func beginUpgradeClaim(ctx context.Context, kubeContext, instance string) *Claim {
	ns, typed, err := ClaimClients(kubeContext)
	if err != nil {
		fmt.Println(color.YellowString("warning: could not take the cluster lock before upgrading (%v); continuing", err))
		return nil
	}
	claim, err := AcquireClaim(ctx, typed, ns, instance, kubeContext)
	if err != nil {
		fmt.Println(color.YellowString("warning: %v", err))
		fmt.Println(color.YellowString("  continuing with the upgrade anyway — but if that run is live, this will fight it"))
		return nil
	}
	return claim
}
