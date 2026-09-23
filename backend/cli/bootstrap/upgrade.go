// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"fmt"
	"sort"
	"time"

	dcv1beta1 "github.com/devicechain-io/dc-k8s/api/v1beta1"
	apply "github.com/devicechain-io/dc-k8s/apply"
	"github.com/fatih/color"
	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/dynamic"
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

	// DcctlVersion is the build running this upgrade, recorded on the declaration
	// it updates. The bootstrap path sets the same field directly on State because
	// its command layer builds one; this verb's State is built inside
	// hydrateUpgradeState, so it arrives here instead.
	DcctlVersion string
}

// Upgrade moves a live instance onto a release: the configuration document its
// services read, and the Helm release that runs them.
//
// 🔴 IT NO LONGER MOVES THE OPERATOR, AND THAT IS THE POINT RATHER THAN AN
// OMISSION. The CRDs and the controller are cluster-scoped — one copy shared by
// every instance — so this verb moving them moved them for every instance on the
// cluster, including ones nobody had asked about, in either direction and with
// nothing comparing versions. `dcctl install` owns them. hydrateUpgradeState
// refuses unless the cluster already carries the operator this release needs, so
// by the time anything below runs the schema is known to be the right one.
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
//   - It does not run the infrastructure apply. Some of that apply's inputs cannot
//     be recovered from the cluster — the endpoint and bucket names of an operator's
//     own backup destination — so an upgrade that ran it would either demand them
//     again every time or reconfigure the instance without them. The apply joins this verb when the chart becomes an
//     OpenTofu release and the state lives in the cluster.
//   - It does not change an instance's shape. Profile, topology and areas come from
//     the declaration, not from flags here: this verb moves a version, and changing
//     what an instance IS is a different question with different answers (raising
//     replicas does not re-replicate streams that were created at one).
//
// 🔑 THE CRD TRAP THIS USED TO SOLVE HAS MOVED, NOT GONE. The API server prunes
// fields a structural schema does not declare, so an instance running against CRDs
// older than its services silently discards anything a later release added. This
// verb used to close that by re-applying the whole rendered stream; what closes it
// now is the refusal — an instance cannot be upgraded past the operator its cluster
// carries, so the two can no longer drift apart unnoticed.
//
// The result is named because a deferred call reads it: from the moment the
// declaration says Upgrading, every exit from this function has to say how the
// upgrade ended. See finishUpgradePhase.
func Upgrade(ctx context.Context, provider Provider, opts UpgradeOptions) (err error) {
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

	dyn, _, typed, err := kubeClients(kubeContext)
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

	fmt.Println(GreenUnderline(fmt.Sprintf(
		"\nUpgrade instance %q on provider %q", opts.Instance, provider.Name())))
	fmt.Printf("  %s %s\n", color.WhiteString("Context:"), color.GreenString(kubeContext))
	fmt.Printf("  %s %s\n", color.WhiteString("Services:"),
		color.GreenString(fmt.Sprintf("%s/<area>:%s", st.ImageRegistry, st.ImageVersion)))
	// 🔴 THE OPERATOR IS NOT LISTED AS SOMETHING THIS RUN WILL MOVE, because it is
	// not. hydrateUpgradeState has already refused unless the cluster's operator is
	// the one this build expects; what this verb moves is the instance.
	fmt.Printf("  %s %s\n", color.WhiteString("Operator:"),
		color.GreenString("the cluster's — checked, not moved (`dcctl install` moves it)"))

	// 🔴 THE CONNECTION BUDGET IS ASKED BEFORE THE FIRST WRITE, while a refusal still
	// means nothing has moved. See upgradeconnlimit.go.
	if err := precheckUpgradeLogin(ctx, st); err != nil {
		return err
	}

	// 🔴 THE DECLARATION IS UPDATED BEFORE ANYTHING MOVES, AND BOTH HALVES OF THAT
	// ARE DELIBERATE. See recordUpgradedVersion.
	if err := recordUpgradedVersion(ctx, dyn, opts.Instance, st); err != nil {
		return err
	}

	// Declared here and taken further down: the deferred call closes over the
	// VARIABLE, so the lock the run takes later is the lock this hands back — and
	// the exits between the two (a dry run) reach the deferred call with nothing to
	// release, which is the truth.
	var upgradeClaim *Claim

	// 🔴 REGISTERED HERE AND NOT ONE LINE EARLIER, BECAUSE WHAT IS ABOVE IS NOT THIS
	// RUN'S TO OVERWRITE. recordUpgradedVersion refuses a declaration reading
	// Destroying, and a deferred stamp registered before it would answer that
	// refusal by writing Failed over the Destroying that caused it — erasing the
	// record of a teardown that has not finished, which is the one phase value
	// something else acts on.
	//
	// From this line on the declaration may say Upgrading, and a phase that is only
	// ever entered is a phase that is never left: every exit below — the refusals,
	// the rollout failures, the success at the bottom, and the first Ctrl+C, which
	// cancels this context and unwinds rather than exiting (cmd.Execute; the SECOND
	// signal does terminate the process, by design) — has to replace it with a
	// terminal one. A deferred call is what makes that true of exits nobody has
	// written yet.
	defer func() { finishUpgradePhase(ctx, dyn, opts.Instance, st, upgradeClaim, err) }()

	if opts.DryRun {
		fmt.Println()
		wouldDo("recompose the instance configuration document from this release's chart, " +
			"keeping every credential the instance is running on")
		wouldDo("upgrade the instance's Helm release")
		warnPreGeneratedSuperuser(st)
		return nil
	}

	// 🔴 UPGRADE TAKES THE CLUSTER LOCK TOO. It no longer applies the cluster-scoped
	// operator objects that first justified this — that is `dcctl install`'s now — but
	// it still rewrites the instance's configuration document and its Helm release,
	// and a concurrent bootstrap of the same instance writes both. A lock that only
	// bootstrap and destroy take is a lock with a documented bypass.
	//
	// It is announced and not enforced, for the same reason destroy's is: an
	// operator repairing a stuck instance must not be blocked by a lock held by
	// the very run that got stuck.
	//
	// It is given back by finishUpgradePhase rather than by a defer of its own, and
	// that is ordering rather than tidiness: Release DELETES the Lease, and a
	// deleted Lease reads as a LOST claim — so a release registered after the phase
	// write would run before it (defers unwind backwards) and fence out the very
	// stamp it is meant to permit.
	upgradeClaim = beginUpgradeClaim(ctx, kubeContext, opts.Instance)

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

	// Grown before the release, shrunk once its services have rolled over: see
	// rolloutWithLoginResize.
	if err := rolloutWithLoginResize(ctx, st, func() error {
		if err := runStreamed("Upgrading the instance's services", "helm upgrade", func() error {
			return helmInstall(ctx, st)
		}); err != nil {
			return err
		}
		// waitForAreasStep prints the progress line AND its terminator — the counts
		// ARE the terminator. Nothing goes around it: this call site used to open the
		// line itself and then print a bare `done.` on top of `done (12/12 ready).`,
		// and on the failure path a `failed.` on top of `timed out (3/12 ready).`.
		//
		// 🔴 AND WHAT IT IS HANDED IS A NAMESPACE, NOT AN INSTANCE. Unlike the bootstrap
		// path this one never runs stepRenderConfig, so it has no resolved namespace to
		// pass and reached for the instance id instead — the same string until an
		// instance's namespace gained a prefix, and after that a five-minute wait on a
		// namespace that does not exist, on every upgrade.
		if err := waitForAreasStep(ctx, typed, InstanceNamespace(st.Instance),
			"waiting for the services to roll over",
			areaReadyTimeout, areaReadyPollInterval); err != nil {
			return fmt.Errorf("waiting for the services: %w", err)
		}
		return nil
	}); err != nil {
		return err
	}

	fmt.Println(color.HiGreenString("\nInstance upgraded."))
	fmt.Printf("  %s %s\n", color.WhiteString("Services:"),
		color.GreenString(fmt.Sprintf("%s/<area>:%s", st.ImageRegistry, st.ImageVersion)))
	// 🔴 SAID BECAUSE THE OPERATOR IS THE ONE THING THIS COMMAND USED TO MOVE AND NO
	// LONGER DOES, and an operator who upgraded through the previous release has
	// every reason to assume it did. The check that let this run at all proved the
	// cluster's operator is the one this build expects — so naming it here is a
	// statement about what was verified, not about what was applied.
	fmt.Printf("  %s %s\n", color.WhiteString("Operator:"),
		color.GreenString("unchanged — the cluster's, already at this release"))
	// 🔴 SAID OUT LOUD BECAUSE IT IS THE ONE THING THIS COMMAND NO LONGER LEAVES TO
	// SOMEBODY ELSE, AND THE ONE THING NOBODY WOULD CHECK. It used to close by naming
	// `helm upgrade` as the missing half; now it IS both halves, and the fact worth
	// stating is the one an operator would otherwise have to take on trust — that a
	// version change did not quietly become a credential change.
	reconcileUpgradeEscrow(st, st.Values["secretsRootKey"], opts)
	fmt.Println(color.WhiteString(
		"\nEvery credential this instance was running on was kept. An upgrade reads them;\n" +
			"it mints nothing, so nothing here rotated."))
	// ...which, for an instance older than the generated superuser password, is also
	// the one thing that must not be read as good news. See warnPreGeneratedSuperuser.
	warnPreGeneratedSuperuser(st)
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

// recordUpgradedVersion writes the version this upgrade is moving to back into the
// instance's declaration.
//
// 🔴 THE DECLARATION ONLY EVER RECORDED WHAT BOOTSTRAP WAS TOLD, AND THAT IS A
// SILENT ROLLBACK. WriteInstanceCR had exactly one caller — the bootstrap claim step
// — so `dcctl upgrade --version v1.3.0` moved every image and left the CR saying
// v1.2.0. Since applyUpgradeDeclaration takes the declaration as the DEFAULT and the
// flags as an override, the next flagless `dcctl upgrade` then read v1.2.0 and rolled
// every service AND the operator backwards, reporting success and correctly reporting
// that nothing had rotated. A version change that succeeds at moving an instance the
// wrong way is the worst shape this arc has found, and this is the second instance of
// it in the same verb.
//
// 🔴 IT IS WRITTEN BEFORE THE MOVE, NOT AFTER, AND THE ORDERING IS NOT A DETAIL.
// InstanceSpec is documented as the DESIRED state; the version an operator asked for
// becomes desired the moment they ask, so recording it afterwards would be filing an
// observation in a field that means intent. The failure modes decide it too, because
// both orderings have one and they are not equal:
//
//   - written FIRST, an upgrade that dies half-way leaves a declaration naming the
//     new version over a half-moved instance — and a flagless re-run reads it and
//     FINISHES the job. That is the resumable direction, and §5.1l measured a
//     half-failed upgrade being completed by exactly such a re-run.
//   - written LAST, the same failure leaves the old version declared over a half-moved
//     instance, and the flagless re-run rolls the moved half BACK. That is the defect
//     this function exists to remove, merely narrowed.
//
// It writes only the image source. Everything else in the declaration is the
// instance's SHAPE, which this verb does not change (see Upgrade), and
// WriteInstanceCR refuses a move of the immutable fields anyway — so a spec rebuilt
// from anything but the live one would turn a flag typo into a refusal about the
// cluster binding.
// It takes the dynamic client rather than a kube context for the reason
// writeInstanceCR is split from WriteInstanceCR one file over: the refusals here —
// a declaration that vanished mid-run, a spec whose immutable half moved — are the
// part worth testing, and a branch that needs a live cluster to reach is a branch
// that goes untested. Upgrade already holds the client.
func recordUpgradedVersion(ctx context.Context, dyn dynamic.Interface, instance string, st *State) error {
	if st.DryRun {
		wouldDo(fmt.Sprintf("record %s:%s in the instance declaration",
			st.ImageRegistry, st.ImageVersion))
		return nil
	}

	inst, err := readInstanceCR(ctx, dyn, instance)
	if err != nil {
		return err
	}
	if inst == nil {
		// hydrateUpgradeState already refused a missing declaration, so reaching
		// here means it was deleted between that read and this one. Saying so beats
		// a nil dereference, and beats recreating a declaration somebody just removed.
		return fmt.Errorf(
			"the declaration for instance %q disappeared while this upgrade was starting, "+
				"so the version it is moving to cannot be recorded. Nothing has been applied; "+
				"re-run to start from a clean read", instance)
	}

	if inst.Spec.ImageRegistry == st.ImageRegistry && inst.Spec.ImageVersion == st.ImageVersion {
		// Already says what this run is about to do. Writing anyway would bump the
		// provenance annotations on every no-op re-apply, which makes
		// `last-applied-at` useless for the question it exists to answer.
		return nil
	}

	spec := inst.Spec
	spec.ImageRegistry, spec.ImageVersion = st.ImageRegistry, st.ImageVersion
	// 🔴 Upgrading, AND THE VALUE IS THE DEFECT THIS LINE FIXES. writeInstanceCR
	// stamped Bootstrapping on every write, so every instance that had ever been
	// upgraded declared a bootstrap in progress from then on — a phase that is not
	// merely imprecise but names the wrong VERB, which is the one thing a reader
	// uses it for. See finishUpgradePhase for the other half: a phase that is only
	// ever entered is a phase that is never left.
	return writeInstanceCR(ctx, dyn, instance, spec, st.DcctlVersion, dcv1beta1.PhaseUpgrading)
}

// finishUpgradePhase records how this upgrade ENDED on the declaration, and gives
// the cluster lock back afterwards.
//
// 🔴 IT IS THE HALF THAT WAS MISSING, AND WITHOUT IT THE PHASE IS WRITE-ONLY.
// `upgrade` wrote a phase on its way in and none on its way out, so the value an
// instance was left holding was whatever the last write happened to be — which is
// how every upgraded instance came to read Bootstrapping for good. A phase that is
// only ever entered describes a run that never ended, over an instance that is
// running fine.
//
// 🔴 A FENCED RUN WRITES NOTHING, for the reason finishClaim gives at the same
// point in bootstrap: once the claim is lost the declaration belongs to whoever
// reclaimed it, and stamping Failed would overwrite the phase of a run that is
// live and doing well. CheckHeld is what establishes that — asked of the API
// server here, not read from the renewal loop's cached flag, which can be a full
// interval out of date at exactly this moment.
//
// 🔑 A run that never HELD the lock is the other case, and it DOES stamp.
// beginUpgradeClaim warns and continues when it cannot take the lock, so such a
// run has already rewritten this declaration's spec unfenced; refusing it the
// terminal phase would buy no safety and would leave the instance reading
// Upgrading for good — the exact defect this function exists to close.
//
// Reporting, not correctness: an upgrade that worked must not be reported as
// failed because a courtesy annotation did not land.
func finishUpgradePhase(ctx context.Context, dyn dynamic.Interface, instance string, st *State, claim *Claim, runErr error) {
	// Detached from the caller's cancellation, like finishClaim's and Release's own:
	// Ctrl+C is when the terminal phase matters most, and it is also when the run's
	// context is already dead — so a cleanup that inherited it would fail its first
	// call and leave the declaration mid-verb.
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()

	// A rehearsal recorded no Upgrading (recordUpgradedVersion returns early under
	// --dry-run), so it has nothing to close out and must write nothing.
	if !st.DryRun && (claim == nil || claim.CheckHeld(cleanup) == nil) {
		phase := dcv1beta1.PhaseReady
		if runErr != nil {
			phase = dcv1beta1.PhaseFailed
		}
		// 🔴 AND NOT OVER A TEARDOWN. Destroying is the one phase value another command
		// ACTS on — writeInstanceCR refuses a rebuild over it and hydrateUpgradeState
		// refuses an upgrade over it — so stamping it out does not merely mislabel the
		// instance, it removes the only cluster-side evidence that the cluster holds half
		// of one, and hands the next bootstrap a green light onto it.
		//
		// 🔑 THE REFUSAL UPSTREAM SHOULD MEAN THIS NEVER FIRES, AND THAT IS EXACTLY WHY IT
		// IS HERE. hydrateUpgradeState refuses a teardown before this defer is registered,
		// but nothing in THIS function depends on that: it is an ordering two files apart
		// that a reordering, or a future caller reaching finishUpgradePhase by another
		// path, would break in silence. The guarantee is made local to the function that
		// would do the damage.
		//
		// 🔑 A READ THAT FAILS NEEDS NO ARM OF ITS OWN, and giving it one would be a
		// branch nothing could tell from the other. setInstancePhase reads the same
		// declaration through the same client before it patches, so a read this could not
		// make is a write that cannot happen either — and that already warns. What must
		// not happen is the read failing and the phase being written anyway, which is not
		// reachable from here.
		current, readErr := readInstanceCR(cleanup, dyn, instance)
		if readErr == nil && current != nil &&
			current.Annotations[dcv1beta1.AnnotationPhase] == dcv1beta1.PhaseDestroying {
			fmt.Println(color.YellowString(
				"warning: instance %q is part-way through being DESTROYED, so this upgrade left the "+
					"declaration saying so rather than recording itself as %s.\n"+
					"  Finish the teardown with `dcctl destroy %s`, which is resumable, and build it "+
					"again with `dcctl bootstrap` afterwards.", instance, phase, instance))
		} else if err := setInstancePhase(cleanup, dyn, instance, phase); err != nil {
			fmt.Println(color.YellowString("warning: could not record the instance phase (%v)", err))
		}
	}
	if claim != nil {
		claim.Release(cleanup)
	}
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
