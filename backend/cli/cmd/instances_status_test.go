// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	dcv1beta1 "github.com/devicechain-io/dc-k8s/api/v1beta1"
	"github.com/devicechain-io/dcctl/bootstrap"
)

// 🔴 THE STATUS CELL IS THE WHOLE COMMAND, AND IT PRINTED THE OPPOSITE OF THE TRUTH.
// Measured on a real cluster: the declaration's phase annotation read `Failed` and
// `dcctl instances list` printed `running`, one annotation read away, with no interrupt
// and no timing involved. The word was derived from "does the CLUSTER exist", which is a
// question about the cluster and not about the instance.
//
// So every row below is a condition an operator can be in, and the rule the table
// enforces is narrow and absolute: NO FAILURE MAY READ AS HEALTHY. A marker that could
// not be statted, a declaration that could not be read, a CRD that is not installed and
// an API server that does not answer are all "we could not tell", and each one has a cell
// that says so — because the direction that costs something is the one where an operator
// reads `running` and stops looking.

// declaredAs builds a declaration carrying the given phase, as the cluster would hold it.
func declaredAs(phase string) *dcv1beta1.Instance {
	inst := &dcv1beta1.Instance{}
	inst.Name = "inst"
	if phase != "" {
		inst.Annotations = map[string]string{dcv1beta1.AnnotationPhase: phase}
	}
	return inst
}

// knownLocal is a row whose local half is healthy: a readable record, no destroy marker.
func knownLocal() bootstrap.KnownInstance {
	return bootstrap.KnownInstance{
		Instance:  "inst",
		HasRecord: true,
		Record: bootstrap.InstanceRecord{
			Instance: "inst", Provider: "local", Cluster: "c",
			KubeContext: "kind-c", Managed: true,
		},
	}
}

// probesReturning answers both cluster questions with fixed values.
func probesReturning(exists bool, existsErr error, inst *dcv1beta1.Instance, readErr error) listingProbes {
	return listingProbes{
		clusterExists: func(context.Context, bootstrap.Provider, bootstrap.ClusterBinding) (bool, error) {
			return exists, existsErr
		},
		readDeclaration: func(context.Context, string, string) (*dcv1beta1.Instance, error) {
			return inst, readErr
		},
	}
}

func TestInstanceStatusSaysWhatIsTrueOfEveryCondition(t *testing.T) {
	deleting := declaredAs(dcv1beta1.PhaseReady)
	now := metav1.Now()
	deleting.DeletionTimestamp = &now

	unreadableRecord := bootstrap.KnownInstance{Instance: "inst", Err: errors.New("invalid character 'x'")}
	noRecord := bootstrap.KnownInstance{Instance: "inst"}

	marked := knownLocal()
	marked.Destroying = true

	uncheckableMarker := knownLocal()
	uncheckableMarker.DestroyingErr = errors.New("permission denied")

	unknownProvider := knownLocal()
	unknownProvider.Record.Provider = "no-such-provider"

	adopted := knownLocal()
	adopted.Record.Managed = false

	for _, tc := range []struct {
		name   string
		known  bootstrap.KnownInstance
		probes listingProbes
		want   string
		// notWant guards the direction that costs something: a cell that reads as a
		// healthy, running instance.
		notWant []string
	}{
		{
			name:  "the destroy marker is there",
			known: marked,
			// 🔴 ANSWERED WITHOUT ASKING THE CLUSTER. The probes fail loudly here: a
			// part-way destroyed instance is knowable from this machine alone, and a row
			// that needed the cluster to say so would go silent in the case the marker
			// exists for — a destroy that could not reach the cluster at all.
			probes: listingProbes{
				clusterExists: func(context.Context, bootstrap.Provider, bootstrap.ClusterBinding) (bool, error) {
					t.Error("the marked row asked the provider about a cluster it does not need")
					return true, nil
				},
				readDeclaration: func(context.Context, string, string) (*dcv1beta1.Instance, error) {
					t.Error("the marked row read a declaration it does not need")
					return nil, nil
				},
			},
			want:    "PART-WAY DESTROYED",
			notWant: []string{"running", "ready"},
		},
		{
			name:    "the marker could not be checked",
			known:   uncheckableMarker,
			probes:  probesReturning(true, nil, declaredAs(dcv1beta1.PhaseReady), nil),
			want:    "could not check",
			notWant: []string{"running", "declared ready"},
		},
		{
			name:    "the record could not be read",
			known:   unreadableRecord,
			probes:  probesReturning(true, nil, declaredAs(dcv1beta1.PhaseReady), nil),
			want:    "record unreadable",
			notWant: []string{"running"},
		},
		{
			name:    "there is no record",
			known:   noRecord,
			probes:  probesReturning(true, nil, declaredAs(dcv1beta1.PhaseReady), nil),
			want:    "no record",
			notWant: []string{"running"},
		},
		{
			name:    "the provider is not one dcctl knows",
			known:   unknownProvider,
			probes:  probesReturning(true, nil, declaredAs(dcv1beta1.PhaseReady), nil),
			want:    "unknown provider",
			notWant: []string{"running"},
		},
		{
			name:    "the provider could not say whether the cluster is there",
			known:   knownLocal(),
			probes:  probesReturning(false, errors.New("cannot connect to the docker daemon"), nil, nil),
			want:    "could not check",
			notWant: []string{"running", "cluster gone"},
		},
		{
			name:   "the cluster is gone",
			known:  knownLocal(),
			probes: probesReturning(false, nil, nil, nil),
			want:   "cluster gone — stale local state",
		},
		{
			name:  "the declaration could not be read, or its CRD is not installed",
			known: knownLocal(),
			probes: probesReturning(true, nil, nil, errors.New(
				`reading the instance declaration "inst": the Instance CRD is not installed`)),
			want:    "could not check",
			notWant: []string{"running", "no declaration"},
		},
		{
			name:    "the cluster is there and nothing is declared",
			known:   knownLocal(),
			probes:  probesReturning(true, nil, nil, nil),
			want:    "cluster present, no declaration",
			notWant: []string{"running"},
		},
		{
			name:    "the declaration is marked for deletion",
			known:   knownLocal(),
			probes:  probesReturning(true, nil, deleting, nil),
			want:    "declaration marked for deletion",
			notWant: []string{"running", "declared ready"},
		},
		{
			name:    "the phase says a destroy did not finish",
			known:   knownLocal(),
			probes:  probesReturning(true, nil, declaredAs(dcv1beta1.PhaseDestroying), nil),
			want:    "PART-WAY DESTROYED",
			notWant: []string{"running"},
		},
		{
			name:    "the phase says a bootstrap is under way",
			known:   knownLocal(),
			probes:  probesReturning(true, nil, declaredAs(dcv1beta1.PhaseBootstrapping), nil),
			want:    "bootstrap started, not finished",
			notWant: []string{"running", "did not finish"},
		},
		{
			name:    "the phase says an upgrade is under way",
			known:   knownLocal(),
			probes:  probesReturning(true, nil, declaredAs(dcv1beta1.PhaseUpgrading), nil),
			want:    "upgrade started, not finished",
			notWant: []string{"running", "bootstrap", "did not finish"},
		},
		{
			name:    "the phase says the last run failed",
			known:   knownLocal(),
			probes:  probesReturning(true, nil, declaredAs(dcv1beta1.PhaseFailed), nil),
			want:    "last run failed",
			notWant: []string{"running", "ready"},
		},
		{
			// 🔴 Ready IS NOT `running`. The phase is INTENT — what the last dcctl run was
			// TRYING to do — and nothing in it observes a workload. `running` is a claim
			// about the pods, and this command has not looked at one.
			name:    "the phase says the last run declared it ready",
			known:   knownLocal(),
			probes:  probesReturning(true, nil, declaredAs(dcv1beta1.PhaseReady), nil),
			want:    "declared ready",
			notWant: []string{"running"},
		},
		{
			name:    "there is a declaration with no phase on it",
			known:   knownLocal(),
			probes:  probesReturning(true, nil, declaredAs(""), nil),
			want:    "declared, phase not recorded",
			notWant: []string{"running", "ready"},
		},
		{
			// A newer dcctl wrote a phase this build has never heard of. Printing it is
			// the only honest move: inventing a word for it would report a state this
			// binary cannot reason about as one it can.
			name:    "the phase is one this build does not know",
			known:   knownLocal(),
			probes:  probesReturning(true, nil, declaredAs("Quiescing"), nil),
			want:    `unknown phase "Quiescing"`,
			notWant: []string{"running", "ready"},
		},
		{
			// The adopted note survives, as a qualifier on the phase rather than in place
			// of it. It used to BE the status, which is how `running (adopted cluster)`
			// was printed over an instance whose declaration read Failed.
			name:    "an adopted cluster still gets the phase",
			known:   adopted,
			probes:  probesReturning(true, nil, declaredAs(dcv1beta1.PhaseFailed), nil),
			want:    "last run failed",
			notWant: []string{"running"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := instanceStatus(t.Context(), tc.known, tc.probes)
			if !strings.Contains(got, tc.want) {
				t.Errorf("status %q does not contain %q", got, tc.want)
			}
			for _, no := range tc.notWant {
				if strings.Contains(strings.ToLower(got), strings.ToLower(no)) {
					t.Errorf("status %q still reads as %q, which is the direction that costs something", got, no)
				}
			}
		})
	}

	// The qualifier itself, asserted once rather than in every row above.
	got := instanceStatus(t.Context(), adopted, probesReturning(true, nil, declaredAs(dcv1beta1.PhaseReady), nil))
	if !strings.Contains(got, "adopted cluster") {
		t.Errorf("an adopted cluster lost the note saying dcctl did not name it: %q", got)
	}
}

// 🔴 ONE UNANSWERABLE ROW MUST NOT STALL THE TABLE. The listing reads a declaration per
// instance, serially, so an API server that accepts connections and never answers would
// hang the whole command on the first row — and a command that prints nothing is
// indistinguishable from one that has not been run.
func TestADeclarationReadThatNeverAnswersBecomesACellRatherThanAHang(t *testing.T) {
	orig := declarationReadTimeout
	t.Cleanup(func() { declarationReadTimeout = orig })
	declarationReadTimeout = 20 * time.Millisecond

	probes := listingProbes{
		clusterExists: func(context.Context, bootstrap.Provider, bootstrap.ClusterBinding) (bool, error) {
			return true, nil
		},
		readDeclaration: func(ctx context.Context, _, _ string) (*dcv1beta1.Instance, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		},
	}

	done := make(chan string, 1)
	go func() { done <- instanceStatus(context.Background(), knownLocal(), probes) }()
	select {
	case got := <-done:
		if !strings.Contains(got, "could not check") {
			t.Errorf("a read that timed out printed %q rather than saying it could not tell", got)
		}
		if strings.Contains(strings.ToLower(got), "running") {
			t.Errorf("a read that timed out printed %q", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the row never came back, so one unreachable instance hangs the whole listing")
	}
}

// 🔴 THE TIMEOUT IS THE ROW'S OWN, NOT THE COMMAND'S. Deriving it from the caller's
// context would give the first row the whole budget and every row after it none.
func TestTheDeclarationReadGetsItsOwnDeadline(t *testing.T) {
	var hadDeadline bool
	probes := listingProbes{
		clusterExists: func(context.Context, bootstrap.Provider, bootstrap.ClusterBinding) (bool, error) {
			return true, nil
		},
		readDeclaration: func(ctx context.Context, _, _ string) (*dcv1beta1.Instance, error) {
			_, hadDeadline = ctx.Deadline()
			return declaredAs(dcv1beta1.PhaseReady), nil
		},
	}
	// A caller with no deadline of its own, which is what cmd.Context() is.
	instanceStatus(context.Background(), knownLocal(), probes)
	if !hadDeadline {
		t.Fatal("the declaration read was handed an unbounded context, so a hung API server " +
			"stalls the listing row by row")
	}
}

// 🔴 THE WIRING, WHICH THE TABLE ABOVE STRUCTURALLY CANNOT SEE. Every row hands
// instanceStatus its own probes, so a build whose live probes read nothing — or read the
// wrong thing — passes all of them. This asserts the ones the command actually runs with.
func TestTheLiveProbesAreTheRealReads(t *testing.T) {
	if liveProbes.clusterExists == nil || liveProbes.readDeclaration == nil {
		t.Fatal("the listing ships with a probe that is nil, so every row would panic or lie")
	}
	// bootstrap.ReadInstanceCR is the one read that distinguishes "no declaration" from
	// "the CRD is not installed"; a listing wired to anything else reports the second as
	// the first. Compared by behaviour rather than by pointer: a function value cannot be
	// compared in Go, and asking it about a cluster that does not resolve is enough to
	// show it is a real cluster read rather than a stub.
	_, err := liveProbes.readDeclaration(t.Context(), "no-such-context-anywhere", "inst")
	if err == nil {
		t.Fatal("the live declaration probe answered for a context that does not exist, so it " +
			"is not reading a cluster and every row's phase is invented")
	}
}
