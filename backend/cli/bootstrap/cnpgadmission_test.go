// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/rest"
	k8stesting "k8s.io/client-go/testing"
)

// Every string below was CAPTURED from a live cluster (CNPG 1.30.0 on kind),
// not composed to match the classifier. The unavailable cases were produced by
// scaling `cnpg-cloudnative-pg` to zero and issuing a server-side dry-run create;
// the rejection case by dry-run creating a Cluster with an invalid synchronous
// configuration against a HEALTHY webhook. Writing the classifier and then
// inventing strings for it would test nothing but my own imagination.
const (
	// Endpoint still listed, pod terminating: the API server dials and waits out
	// its own 10s webhook timeout.
	realDeadlineExceeded = `Internal error occurred: failed calling webhook "mcluster.cnpg.io": ` +
		`failed to call webhook: Post "https://cnpg-webhook-service.cnpg-system.svc:443/mutate-postgresql-cnpg-io-v1-cluster?timeout=10s": ` +
		`context deadline exceeded`

	// Endpoints drained: nothing answers on the ClusterIP.
	realConnectionRefused = `Internal error occurred: failed calling webhook "mcluster.cnpg.io": ` +
		`failed to call webhook: Post "https://cnpg-webhook-service.cnpg-system.svc:443/mutate-postgresql-cnpg-io-v1-cluster?timeout=10s": ` +
		`dial tcp 10.96.33.253:443: connect: connection refused`

	// The form the original bug produced, through Helm and the terraform provider.
	realThroughHelm = `Error: release cluster-rdb failed, and has been uninstalled due to atomic being set: ` +
		`Internal error occurred: failed calling webhook "mcluster.cnpg.io": ` +
		`failed to call webhook: Post "https://cnpg-webhook-service.cnpg-system.svc:443/mutate-postgresql-cnpg-io-v1-cluster?timeout=10s": ` +
		`dial tcp 10.96.221.14:443: connect: connection refused`

	// 🔴 The counterweight the whole design rests on: the webhook ANSWERED and
	// said no. This is a real configuration error and must reach the operator
	// unretried and unmodified.
	realRejection = `The Cluster "dcctl-webhook-probe-bad" is invalid: spec.postgresql.synchronous: ` +
		`Invalid value: {"method":"any","number":5,"dataDurability":"required","failoverQuorum":false}: ` +
		`Invalid synchronous configuration: the number of synchronous replicas must be less than ` +
		`the total number of instances and the provided standby names.`
)

func TestIsCNPGWebhookUnavailable_MatchesRealUnreachableWebhook(t *testing.T) {
	for name, msg := range map[string]string{
		"context deadline exceeded": realDeadlineExceeded,
		"connection refused":        realConnectionRefused,
		"through helm and tofu":     realThroughHelm,
		// Not measured here, but the same API server wording with a different
		// transport failure underneath — the classifier must not care which.
		"no endpoints available": `Internal error occurred: failed calling webhook "mcluster.cnpg.io": ` +
			`failed to call webhook: Post "https://cnpg-webhook-service.cnpg-system.svc:443/mutate": ` +
			`no endpoints available for service "cnpg-webhook-service"`,
		"empty CA bundle": `Internal error occurred: failed calling webhook "mcluster.cnpg.io": ` +
			`failed to call webhook: Post "https://cnpg-webhook-service.cnpg-system.svc:443/mutate": ` +
			`x509: certificate signed by unknown authority`,
		// A2.5 adds ScheduledBackups; the domain suffix must carry them without
		// anyone remembering to come back here.
		"a different cnpg webhook": `Internal error occurred: failed calling webhook "mscheduledbackup.cnpg.io": ` +
			`failed to call webhook: Post "https://cnpg-webhook-service.cnpg-system.svc:443/mutate": EOF`,
	} {
		t.Run(name, func(t *testing.T) {
			if !isCNPGWebhookUnavailable(msg) {
				t.Fatalf("expected the gate to recognise an unreachable CNPG webhook, but it did not:\n%s", msg)
			}
		})
	}
}

// TestIsCNPGWebhookUnavailable_DoesNotSwallowRealFailures is the test that gives
// the retry the right to exist. A classifier that matched too widely would turn
// every apply failure into a five-minute wait followed by the same failure, with
// the cause two attempts back in the scrollback.
func TestIsCNPGWebhookUnavailable_DoesNotSwallowRealFailures(t *testing.T) {
	for name, msg := range map[string]string{
		// The webhook answered. This is the discriminator the design rests on.
		"webhook rejected the spec": realRejection,
		// The other rejection wording Kubernetes uses.
		"generic admission denial": `admission webhook "vcluster.cnpg.io" denied the request: ` +
			`instances must be at least 1`,
		// Another operator's webhook being down is not ours to retry.
		"cert-manager webhook down": `Internal error occurred: failed calling webhook "webhook.cert-manager.io": ` +
			`failed to call webhook: Post "https://cert-manager-webhook.cert-manager.svc:443/mutate": ` +
			`connect: connection refused`,
		// Neither is a tenant policy engine's, even though it fails identically.
		"policy engine webhook down": `Internal error occurred: failed calling webhook "validate.kyverno.svc-fail": ` +
			`failed to call webhook: connect: connection refused`,
		// A name that merely mentions cnpg.io without being in the domain.
		"lookalike webhook name": `Internal error occurred: failed calling webhook "cnpg.io.evil.example.com": ` +
			`failed to call webhook: connect: connection refused`,
		// The failure modes that actually strand a bootstrap, none of which a
		// retry helps.
		"crd missing":     `Error: resource mapping not found for kind "Cluster": no matches for kind "Cluster" in version "postgresql.cnpg.io/v1"`,
		"image pull":      `Error: timed out waiting for the condition: pod cnpg-cloudnative-pg-0 has ErrImagePull`,
		"quota":           `Error: pods "cluster-rdb-1" is forbidden: exceeded quota: compute, requested: memory=2Gi`,
		"prevent_destroy": `Error: Instance cannot be destroyed: Resource module.rdb.helm_release.cluster has lifecycle.prevent_destroy set`,
		"state lock":      `Error: Error acquiring the state lock: resource temporarily unavailable`,
		"unreachable apiserver": `Error: Kubernetes cluster unreachable: Get "https://127.0.0.1:6443/version": ` +
			`dial tcp 127.0.0.1:6443: connect: connection refused`,
		"empty": "",
	} {
		t.Run(name, func(t *testing.T) {
			if isCNPGWebhookUnavailable(msg) {
				t.Fatalf("the gate claimed this was an unreachable CNPG webhook and would have retried it:\n%s", msg)
			}
		})
	}
}

// TestIsCNPGWebhookUnavailable_MalformedInput guards the scan loop. It walks the
// message looking for successive occurrences, so a malformed message must
// terminate it rather than spin — and every case asserts an expected value, since
// a case with no assertion passes whatever the classifier does.
func TestIsCNPGWebhookUnavailable_MalformedInput(t *testing.T) {
	for name, tc := range map[string]struct {
		msg  string
		want bool
	}{
		"no quote at all":   {`failed calling webhook mcluster.cnpg.io: connection refused`, false},
		"unterminated name": {`failed calling webhook "mcluster.cnpg.io`, false},
		"repeated marker":   {strings.Repeat(`failed calling webhook `, 50), false},
		// The first candidate is not ours and the second is: the loop must keep
		// looking rather than stop at the first miss.
		"second occurrence matches": {`failed calling webhook "webhook.cert-manager.io": refused; ` +
			`failed calling webhook "mcluster.cnpg.io": refused`, true},
	} {
		t.Run(name, func(t *testing.T) {
			if got := isCNPGWebhookUnavailable(tc.msg); got != tc.want {
				t.Fatalf("isCNPGWebhookUnavailable = %v, want %v for:\n%s", got, tc.want, tc.msg)
			}
		})
	}
}

// TestIsCNPGWebhookUnavailable_SurvivesLineWrapping is the guard on the quietest
// way this gate could die. The phrase has to reach us byte-contiguous through
// Helm, the terraform provider, tofu's diagnostic renderer and terraform-exec —
// and tofu word-wraps diagnostic detail at a width that depends on how long the
// release name and URL earlier in the line happened to be. A wrap inside "failed
// calling webhook" would switch the gate off with no signal at all, reverting to
// the race while the README promises a retry.
func TestIsCNPGWebhookUnavailable_SurvivesLineWrapping(t *testing.T) {
	wrapped := "Error: release dc-rdb failed, and has been uninstalled due to atomic\n" +
		"being set: Internal error occurred: failed calling\n" +
		"webhook \"mcluster.cnpg.io\": failed to call webhook: Post\n" +
		"\"https://cnpg-webhook-service.cnpg-system.svc:443/mutate\": dial tcp\n" +
		"10.96.33.253:443: connect: connection refused"
	if !isCNPGWebhookUnavailable(wrapped) {
		t.Fatal("a line wrap inside the marker phrase silently disabled the gate")
	}
}

// TestCNPGProbeClusterIsMinimalAndDisposable pins the shape of the probe object.
// It is dry-run created against a real API server, so the fields that matter are
// the ones the server routes on — a wrong apiVersion or kind would be rejected
// before admission ever ran, making the probe answer a question nobody asked.
func TestCNPGProbeClusterIsMinimalAndDisposable(t *testing.T) {
	obj := cnpgProbeCluster()

	if got := obj.GetAPIVersion(); got != "postgresql.cnpg.io/v1" {
		t.Errorf("apiVersion = %q, want postgresql.cnpg.io/v1 — the webhook rules match on it", got)
	}
	if got := obj.GetKind(); got != "Cluster" {
		t.Errorf("kind = %q, want Cluster", got)
	}
	// 🔴 The namespace is load-bearing, in two directions. It must be one the
	// webhook's namespaceSelector actually selects, or the probe is admitted
	// without the webhook ever being called — a check that cannot fail. And it
	// must be one the bootstrap already has rights in, or a least-privilege
	// installer role turns the gate into a permanent "cannot tell".
	if got := obj.GetNamespace(); got != infraNamespace {
		t.Errorf("namespace = %q, want %q (where the real Clusters are created)", got, infraNamespace)
	}

	instances, found, err := unstructured.NestedInt64(obj.Object, "spec", "instances")
	if err != nil || !found {
		t.Fatalf("spec.instances missing (found=%v, err=%v) — CNPG's validating webhook runs on a dry-run too", found, err)
	}
	if instances != 1 {
		t.Errorf("spec.instances = %d, want 1: the probe provisions nothing and must not imply otherwise", instances)
	}
}

// reactingClient builds a dynamic client whose Cluster creates return errs in
// order, then succeed.
func reactingClient(t *testing.T, errs ...error) dynamic.Interface {
	t.Helper()
	c := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
		map[schema.GroupVersionResource]string{clusterGVR: "ClusterList"})
	var n int
	c.PrependReactor("create", "clusters", func(k8stesting.Action) (bool, runtime.Object, error) {
		n++
		if n <= len(errs) {
			return true, nil, errs[n-1]
		}
		return true, cnpgProbeCluster(), nil
	})
	return c
}

func repeatErr(err error, n int) []error {
	out := make([]error, n)
	for i := range out {
		out[i] = err
	}
	return out
}

// TestProbeCNPGAdmission_Verdicts is the test the first version of this file
// needed and did not have. Two independent reviews found the same defect: the
// probe reported "the API server can admit a Cluster" for every error it did not
// recognise, including ones where the request never reached admission at all. The
// distinction it must draw is between "the webhook answered" and "I could not
// ask", and only the first of those licenses a retry.
func TestProbeCNPGAdmission_Verdicts(t *testing.T) {
	gvk := schema.GroupVersionResource{Group: "postgresql.cnpg.io", Version: "v1", Resource: "clusters"}
	_ = gvk

	for name, tc := range map[string]struct {
		err  error
		want probeVerdict
	}{
		"admitted": {nil, webhookReachable},

		// The webhook could not be called. The only verdict worth waiting on.
		"webhook unreachable": {errors.New(realConnectionRefused), webhookUnavailable},

		// The webhook answered and refused. Reached, so stop waiting.
		"webhook denied": {errors.New(`admission webhook "vcluster.cnpg.io" denied the request: nope`), webhookReachable},
		// Object schema validation runs AFTER mutating admission, so reaching it
		// proves the webhook was called.
		"invalid": {apierrors.NewInvalid(
			schema.GroupKind{Group: "postgresql.cnpg.io", Kind: "Cluster"}, "probe", nil), webhookReachable},
		// Storage is after everything.
		"already exists": {apierrors.NewAlreadyExists(
			schema.GroupResource{Group: "postgresql.cnpg.io", Resource: "clusters"}, "probe"), webhookReachable},

		// 🔴 These are the ones that used to be reported as success. Authorization
		// and NamespaceLifecycle both run BEFORE admission webhooks, and a dead
		// API server never gets there at all — none of them say anything about the
		// webhook, and claiming otherwise burns the single retry inside the race.
		"rbac forbidden": {apierrors.NewForbidden(
			schema.GroupResource{Group: "postgresql.cnpg.io", Resource: "clusters"}, "probe",
			errors.New("not allowed")), probeInconclusive},
		"namespace missing": {apierrors.NewNotFound(
			schema.GroupResource{Resource: "namespaces"}, infraNamespace), probeInconclusive},
		"apiserver unreachable": {errors.New(
			`Get "https://127.0.0.1:6443/version": dial tcp 127.0.0.1:6443: connect: connection refused`),
			probeInconclusive},
		"dry-run unsupported": {errors.New(
			`the server does not support dry run`), probeInconclusive},
	} {
		t.Run(name, func(t *testing.T) {
			var errs []error
			if tc.err != nil {
				errs = []error{tc.err}
			}
			got, _ := probeCNPGAdmission(t.Context(), reactingClient(t, errs...))
			if got != tc.want {
				t.Fatalf("verdict = %v, want %v for error: %v", got, tc.want, tc.err)
			}
		})
	}
}

// TestAwaitCNPGAdmission_GivesUpAndReports is the half of the negative control a
// test can hold. A webhook that never returns is a REAL failure — an operator
// misconfigured, a firewall in the way, a control plane that cannot reach the
// cluster network at all — and the gate must surface it rather than wait forever
// or, worse, retry the apply into the same wall.
func TestAwaitCNPGAdmission_GivesUpAndReports(t *testing.T) {
	const timeout = 150 * time.Millisecond
	start := time.Now()
	err := awaitCNPGAdmission(t.Context(),
		reactingClient(t, repeatErr(errors.New(realConnectionRefused), 10_000)...),
		timeout, time.Millisecond)
	if err == nil {
		t.Fatal("the gate returned success while the webhook was still unreachable — " +
			"the apply would be retried into the same failure")
	}
	// Tight on purpose: the claim being checked is that the wait is bounded BY ITS
	// TIMEOUT, not merely that it terminates eventually.
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("the wait is not bounded by its timeout (took %s for a %s timeout)", elapsed, timeout)
	}
	// 🔴 The API server's own words must reach the operator. Without them the
	// report says "it didn't come back" and takes the diagnosis with it.
	if !strings.Contains(err.Error(), "connect: connection refused") {
		t.Errorf("the give-up error drops what the API server said, leaving nothing to diagnose:\n%v", err)
	}
}

// TestAwaitCNPGAdmission_ReleasesWhenTheWebhookReturns is the counterweight: a
// gate that only ever blocks is a gate that broke the bootstrap. It must let go.
func TestAwaitCNPGAdmission_ReleasesWhenTheWebhookReturns(t *testing.T) {
	client := reactingClient(t, repeatErr(errors.New(realConnectionRefused), 2)...)
	if err := awaitCNPGAdmission(t.Context(), client, time.Minute, time.Millisecond); err != nil {
		t.Fatalf("the gate never released even though the webhook came back: %v", err)
	}
}

// TestAwaitCNPGAdmission_InconclusiveDoesNotBlock: when the probe cannot measure,
// the gate must neither wait out its full timeout on something waiting cannot fix
// (an RBAC denial is not going to change in five minutes) nor claim a reachability
// it never established. It falls back to the pre-gate behaviour — one more apply,
// which atomic made safe.
func TestAwaitCNPGAdmission_InconclusiveDoesNotBlock(t *testing.T) {
	forbidden := apierrors.NewForbidden(
		schema.GroupResource{Group: "postgresql.cnpg.io", Resource: "clusters"}, "probe",
		errors.New("not allowed"))
	start := time.Now()
	if err := awaitCNPGAdmission(t.Context(), reactingClient(t, repeatErr(forbidden, 10_000)...),
		time.Minute, time.Millisecond); err != nil {
		t.Fatalf("an unmeasurable probe blocked the bootstrap instead of falling back: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("the gate waited %s on a condition waiting cannot fix", elapsed)
	}
}

// TestAwaitCNPGAdmission_AbortIsNotSuccess. Ctrl-C during the wait used to print
// a green "the API server can admit a Cluster" and launch a doomed apply on a
// cancelled context. An abort must end the wait as an abort.
func TestAwaitCNPGAdmission_AbortIsNotSuccess(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	err := awaitCNPGAdmission(ctx,
		reactingClient(t, repeatErr(errors.New(realConnectionRefused), 10_000)...),
		time.Minute, time.Millisecond)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("a cancelled context did not end the wait as a cancellation: %v", err)
	}
}

// TestAwaitCNPGAdmission_BoundsTheCallNotJustTheGap uses a real HTTP transport
// against a server that accepts the request and never answers, because that is
// the only way to exercise the thing being claimed.
//
// 🔴 The fake dynamic client cannot test this: it ignores the context and returns
// instantly, so a loop that checked the clock only BETWEEN probes would pass every
// other test in this file while hanging forever in production. The kube clients
// carry no timeout of their own, and a black-holed connection to the API server —
// an idle-dropped load balancer during a managed control-plane scale event, which
// is exactly the environment this gate exists for — produces precisely this: a
// single Create that never returns, in a wait advertised as bounded.
func TestAwaitCNPGAdmission_BoundsTheCallNotJustTheGap(t *testing.T) {
	// The handler must be releasable by the TEST, not only by the client hanging
	// up: a client-go transport that abandons a request does not necessarily close
	// the connection, so a handler parked on the request context alone leaves
	// srv.Close() waiting forever and the test hangs on its own cleanup rather
	// than on the thing it is measuring.
	stop := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done(): // accept, then never answer
		case <-stop:
		}
	}))
	defer func() {
		close(stop)
		srv.Close()
	}()

	dyn, err := dynamic.NewForConfig(&rest.Config{Host: srv.URL})
	if err != nil {
		t.Fatalf("building a dynamic client against the stalled server: %v", err)
	}

	const timeout = 300 * time.Millisecond
	done := make(chan error, 1)
	start := time.Now()
	go func() { done <- awaitCNPGAdmission(t.Context(), dyn, timeout, time.Millisecond) }()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("a wait that never reached the API server reported success")
		}
		if errors.Is(err, context.Canceled) {
			t.Fatalf("a timeout was reported as a cancellation: %v", err)
		}
		if elapsed := time.Since(start); elapsed > 5*time.Second {
			t.Fatalf("the wait took %s for a %s timeout", elapsed, timeout)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("the wait hung on a single unanswered call — the timeout bounds only " +
			"the gaps between probes, not the probes themselves")
	}
}
