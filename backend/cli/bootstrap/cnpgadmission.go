// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/fatih/color"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/dynamic"
)

// The CloudNativePG admission gate (ADR-020 A2).
//
// THE BUG THIS EXISTS FOR
//
// The infrastructure apply installs the CNPG operator and creates the two
// database Clusters in ONE graph. Helm's wait returns when the operator's
// Deployment reports readyReplicas=1 — which is not the same instant its
// admission webhook can be called. The webhook's ClusterIP is not routable
// until the EndpointSlice is written and every kube-proxy on the path has
// re-synced, and the path that matters is the API SERVER's, which on EKS/GKE is
// not even a node we can see. So the Cluster creates can land in a window where
// the API server tries to call `mcluster.cnpg.io` and cannot:
//
//	Internal error occurred: failed calling webhook "mcluster.cnpg.io":
//	failed to call webhook: Post "https://cnpg-webhook-service...":
//	dial tcp 10.96.33.253:443: connect: connection refused
//
// Measured on a cold `--compact` bootstrap: operator pod Ready at 02:24:38, both
// Cluster releases attempted at 02:24:38, refused. It is a race — one failure in
// two cold runs — which is exactly what makes it dangerous: it passes CI and
// bites a customer.
//
// WHY THE FIX IS HERE AND NOT IN THE TOFU ROOT
//
// No Terraform resource retries admission, and `depends_on` cannot express "the
// webhook answers". Two in-cluster designs were proposed and both were refuted:
// a Helm pre-install hook blocks the release's own manifests and fails exactly
// the way the wedge below does, and a `kubernetes_job_v1` probe measures the
// wrong path — a Job's connectivity to the webhook Service says nothing about
// the API server's, and on a managed control plane (EKS, GKE) the API server is
// not in the cluster at all. Every in-cluster probe checks a route the admitter
// does not use.
//
// 🔑 A SERVER-SIDE DRY-RUN CREATE IS THE ONLY PROBE ON THE REAL PATH. The client
// never dials the webhook. It asks the API SERVER to admit an object, and the
// API server dials — over its own network path, with its own CA bundle and its
// own timeout. That makes this identical on kind, k3s, EKS and GKE, and it needs
// no image, no NetworkPolicy hole and no PodSecurity exception. Nothing is
// persisted: dry-run runs the full admission chain and discards the result.
//
// It also needs no privilege the bootstrap does not already exercise, and that is
// why the probe runs in infraNamespace rather than `default`: creating a Cluster
// THERE is exactly the operation that just failed. Probing `default` would have
// added one permission an installer role scoped to the DeviceChain namespaces —
// an ordinary least-privilege setup on EKS — would not grant, turning a working
// gate into an unmeasurable one.
//
// WHAT PAIRS WITH IT
//
// `atomic = true` on the Cluster releases (modules/cnpg-cluster/main.tf) makes a
// lost race leave NO residue — without it the failed create tainted the resource,
// the next plan wanted to replace it, and `prevent_destroy` refused, wedging the
// instance permanently. Read the two together: atomic makes the failure
// survivable, this makes it not happen.

const (
	// cnpgAdmissionTimeout bounds the wait. Generous, because the cost of giving
	// up early is a failed bootstrap and the cost of waiting is a slower one —
	// but bounded, because a webhook that never comes back is a real failure that
	// must be REPORTED rather than waited on forever.
	cnpgAdmissionTimeout = 5 * time.Minute
	// cnpgAdmissionPoll is the gap between probes. Each probe carries the API
	// server's own webhook timeout (10s) when the webhook is unreachable, so the
	// effective cadence is slower than this and that is fine.
	cnpgAdmissionPoll = 3 * time.Second
	// cnpgProbeNamespace is where the throwaway Cluster is dry-run created: the
	// namespace the real Clusters live in, so the probe needs no privilege the
	// apply itself does not.
	//
	// The CNPG webhooks carry an EMPTY namespaceSelector (verified against a live
	// install), so any namespace reaches them — but a probe in a namespace the
	// webhook did NOT select would be admitted without the webhook ever being
	// called, which is a check that cannot fail. Verify that selector before
	// moving this.
	cnpgProbeNamespace = infraNamespace
	// cnpgProbeName is the throwaway object's name. It is never persisted, so this
	// only has to be recognisable in an audit log.
	cnpgProbeName = "dcctl-webhook-probe"
)

// webhookCallFailure is the API server's wording when it could not complete the
// call to a webhook. A webhook that ANSWERS and says no reads completely
// differently — `admission webhook "..." denied the request` or, for CNPG's
// declarative validation, `The Cluster "..." is invalid: ...` — and neither
// contains this phrase. That is the discriminator the classifier rests on, and
// both halves of it were measured against a live cluster rather than reasoned
// about.
const webhookCallFailure = "failed calling webhook "

// webhookDenial is the API server's wording when a webhook answered and refused.
const webhookDenial = "admission webhook "

// isCNPGWebhookUnavailable reports whether msg is the API server saying it could
// not REACH a CloudNativePG admission webhook.
//
// 🔴 It must not match anything else, and that is the whole point of it. A retry
// that swallows a genuine failure is worse than the race it was written for: the
// race costs one re-run, a swallowed error costs the operator their ability to
// see what actually broke. So this matches on two things together — the API
// server's fixed "could not complete the call" wording, AND a webhook name in the
// cnpg.io domain — and it deliberately does NOT enumerate transport errors.
// `connection refused`, `context deadline exceeded`, `no endpoints available`,
// `EOF` and `x509` are all the same event from our side, the list is not closed,
// and a missing entry would silently turn the gate off.
func isCNPGWebhookUnavailable(msg string) bool {
	return namesCNPGWebhook(msg, webhookCallFailure)
}

// isCNPGWebhookDenial reports whether msg is a CNPG admission webhook ANSWERING
// and refusing. That is the opposite verdict to the one above and it matters just
// as much: a denial proves the webhook was reached, which is the only thing the
// probe is asking.
func isCNPGWebhookDenial(msg string) bool {
	return namesCNPGWebhook(msg, webhookDenial)
}

// namesCNPGWebhook scans msg for `marker` followed by a quoted webhook name in
// the cnpg.io domain.
//
// 🔴 Whitespace is normalised first. The phrase has to survive Helm, the
// terraform provider, tofu's diagnostic renderer and terraform-exec's stderr
// capture, and both tofu and terraform word-wrap diagnostic detail at a width
// that depends on how long the release name and URL earlier in the line were. A
// wrap landing inside "failed calling webhook" would turn the whole gate off
// silently, reverting to the old race while the README promises a retry. Webhook
// names contain no whitespace, so collapsing runs of it cannot create a false
// positive.
func namesCNPGWebhook(msg, marker string) bool {
	rest := strings.Join(strings.Fields(msg), " ")
	for {
		i := strings.Index(rest, marker)
		if i < 0 {
			return false
		}
		rest = rest[i+len(marker):]
		if !strings.HasPrefix(rest, `"`) {
			continue
		}
		end := strings.Index(rest[1:], `"`)
		if end < 0 {
			return false
		}
		// Suffix, not substring: a failure calling cert-manager's webhook is not
		// ours to retry, and neither is one from a tenant's policy engine.
		if strings.HasSuffix(rest[1:1+end], ".cnpg.io") {
			return true
		}
	}
}

// cnpgProbeCluster is the smallest object that exercises the Cluster admission
// chain. It is dry-run created and never persisted.
//
// The spec must be VALID, not merely well-formed: the validating webhook runs on
// a dry-run too, and an invalid spec would be rejected by a webhook that answered
// — which is a pass for this probe's question, but a confusing one to read in a
// log. `instances: 1` with a storage size is the minimum CNPG accepts (verified
// against a live install).
func cnpgProbeCluster() *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "postgresql.cnpg.io/v1",
		"kind":       "Cluster",
		"metadata": map[string]any{
			"name":      cnpgProbeName,
			"namespace": cnpgProbeNamespace,
		},
		"spec": map[string]any{
			"instances": int64(1),
			"storage":   map[string]any{"size": "1Gi"},
		},
	}}
}

// probeVerdict is what one dry-run create established about the webhook.
type probeVerdict int

const (
	// webhookReachable — the API server completed the call to the CNPG webhook.
	// Admitted or refused, both count: the probe asks whether the call COMPLETED,
	// not whether it was allowed.
	webhookReachable probeVerdict = iota
	// webhookUnavailable — the API server could not complete the call. The one
	// verdict worth waiting on.
	webhookUnavailable
	// probeInconclusive — the request never reached the webhook, or we could not
	// tell whether it did.
	//
	// 🔴 This case exists because the first version of this file did not have it,
	// and two independent reviews found the same defect: everything that was not a
	// recognisable webhook failure was being reported as "the API server can admit
	// a Cluster". That is a claim, and it was false for an RBAC denial (authorization
	// runs BEFORE admission), a missing namespace (NamespaceLifecycle likewise), an
	// API server we could not reach at all, and — worst — a user pressing Ctrl-C,
	// which printed a green success line in response to an abort. A probe that
	// cannot measure must say so.
	probeInconclusive
)

// probeCNPGAdmission asks the API server to admit a throwaway Cluster and reports
// what that established about the CNPG webhook.
//
// 🔑 Reachability is inferred only from outcomes that PROVE the admission chain
// ran past the mutating webhooks:
//
//   - admitted — the whole chain ran;
//   - a denial naming a cnpg.io webhook — it answered;
//   - Invalid — object schema validation, which runs AFTER mutating admission;
//   - AlreadyExists — the storage layer, which is after everything.
//
// Every other error means the request stopped somewhere earlier, or somewhere we
// cannot place, and yields probeInconclusive. The returned error is always the one
// the API server gave, so the caller can print what actually happened rather than
// a guess about it.
func probeCNPGAdmission(ctx context.Context, dyn dynamic.Interface) (probeVerdict, error) {
	_, err := dyn.Resource(clusterGVR).
		Namespace(cnpgProbeNamespace).
		Create(ctx, cnpgProbeCluster(), metav1.CreateOptions{DryRun: []string{metav1.DryRunAll}})
	switch {
	case ctx.Err() != nil:
		// Checked first and separately: a cancelled context makes every other
		// signal meaningless, and reading a cancel as any kind of verdict about
		// the cluster is how an abort turns into a success message.
		return probeInconclusive, ctx.Err()
	case err == nil:
		return webhookReachable, nil
	case isCNPGWebhookUnavailable(err.Error()):
		return webhookUnavailable, err
	case isCNPGWebhookDenial(err.Error()),
		apierrors.IsInvalid(err),
		apierrors.IsAlreadyExists(err):
		return webhookReachable, err
	default:
		return probeInconclusive, err
	}
}

// waitForCNPGAdmission blocks until the API server can call the CNPG Cluster
// webhook, or the timeout expires.
//
// It prints LOUDLY, on purpose. A silent retry would turn a systematic failure
// into an intermittent one: if the webhook is never reachable in some
// environment, the only symptom would be a bootstrap that got mysteriously
// slower before failing for a reason two apply attempts old. The operator gets
// told what is being waited for, what the API server said, and what a repeat
// occurrence means.
func waitForCNPGAdmission(ctx context.Context, kubeContext string, timeout time.Duration) error {
	dyn, _, _, err := kubeClients(kubeContext)
	if err != nil {
		return fmt.Errorf("building kube clients to check CNPG admission: %w", err)
	}
	return awaitCNPGAdmission(ctx, dyn, timeout, cnpgAdmissionPoll)
}

// awaitCNPGAdmission is the loop, split from its client construction so the
// directions that matter can be asserted without a cluster. The live proof is the
// negative control; this is the regression guard. poll is a parameter for the same
// reason: a test that waited the production cadence would be slow enough that
// someone would delete it.
//
// Returning nil means "go ahead and re-apply" — which is NOT the same as "the
// webhook is up", and the two are printed differently on purpose.
func awaitCNPGAdmission(ctx context.Context, dyn dynamic.Interface, timeout, poll time.Duration) error {
	deadline := time.Now().Add(timeout)

	// 🔴 The deadline has to bound the CALLS, not merely the gaps between them.
	// The kube clients carry no timeout of their own, so a black-holed connection
	// to the API server — an idle-dropped NLB during a managed control-plane
	// scale event, precisely the environment this gate was built for — would hang
	// a single probe forever, and a loop that checks the clock only between probes
	// would never get back to the clock. "Bounded" has to mean bounded.
	ctx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()

	var last error
	for attempt := 1; ; attempt++ {
		verdict, err := probeCNPGAdmission(ctx, dyn)

		// A finished context ends the wait whatever the probe made of it, and it
		// is checked BEFORE the verdict. A deadline that expires inside a hung
		// Create surfaces as an unclassifiable error, and reading that as "cannot
		// tell, go ahead and retry" would quietly convert a full five-minute
		// timeout — the one outcome that must be reported — into a green light.
		if ctx.Err() != nil {
			return giveUpOnCNPGAdmission(ctx, timeout, last)
		}

		switch verdict {
		case webhookReachable:
			fmt.Println(color.GreenString("   the API server can admit a Cluster; retrying the infrastructure apply."))
			return nil
		case probeInconclusive:
			// We could not measure, so we do not claim to have. Fall back to what
			// would have happened without this gate at all — one more apply, which
			// atomic has made safe — and say exactly that.
			fmt.Println(color.YellowString("   cannot tell whether the webhook is reachable from here: %v", err))
			fmt.Println(color.YellowString("   retrying the infrastructure apply anyway; re-running bootstrap is safe."))
			return nil
		}

		last = err
		if time.Now().After(deadline) {
			return giveUpOnCNPGAdmission(ctx, timeout, last)
		}
		fmt.Println(color.YellowString("   [%d] still unreachable, waiting %s...", attempt, poll))
		select {
		case <-ctx.Done():
			return giveUpOnCNPGAdmission(ctx, timeout, last)
		case <-time.After(poll):
		}
	}
}

// giveUpOnCNPGAdmission builds the terminal error for a wait that ended without
// the webhook coming back.
//
// It separates an ABORT from a TIMEOUT because they need different things from
// the reader: a Ctrl-C needs no explanation, and a five-minute timeout needs the
// API server's own words plus the likeliest cause — which on a managed cluster is
// not a race at all but a control-plane-to-node firewall that never allowed the
// webhook's port.
func giveUpOnCNPGAdmission(ctx context.Context, timeout time.Duration, last error) error {
	if errors.Is(context.Cause(ctx), context.Canceled) {
		return ctx.Err()
	}
	if last == nil {
		return fmt.Errorf("gave up waiting for the CloudNativePG admission webhook after %s", timeout)
	}
	return fmt.Errorf("the CloudNativePG admission webhook was still unreachable after %s. "+
		"The API server reported: %w. "+
		"If this happens on every bootstrap it is not a race — the usual cause is a "+
		"firewall between the control plane and the webhook's port (9443); on a GKE "+
		"private cluster that rule has to be added by hand", timeout, last)
}

// reportCNPGAdmissionWait prints the banner explaining why the bootstrap paused.
//
// It says the database cluster STEP was refused, and it no longer says the
// clusters "were created" or that "nothing was left behind". The API server words
// a refused CREATE and a refused UPDATE identically, so the classifier cannot tell
// them apart — and on the update path both of those claims are false. Telling an
// operator mid-re-bootstrap that nothing was left behind, while their populated
// production databases sit right there, would be worse than saying nothing.
func reportCNPGAdmissionWait() {
	fmt.Println()
	fmt.Println(color.YellowString("⚠  The API server could not reach the CloudNativePG admission webhook, so the"))
	fmt.Println(color.YellowString("   database cluster step was refused and rolled itself back. Waiting for the"))
	fmt.Println(color.YellowString("   API server to be able to admit a Cluster, then retrying once."))
	fmt.Println(color.YellowString("   If you see this on EVERY bootstrap it is not a race and should be reported."))
}
