// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"encoding/json"
	"reflect"
	"sort"
	"strings"
	"testing"

	dcv1beta1 "github.com/devicechain-io/dc-k8s/api/v1beta1"
)

// specJSONFields lists the JSON names InstanceSpec actually serialises, read off
// the type rather than written down. The k8s module checks the generated CRD
// against the same reflection, so the manifest, the type and this list cannot
// drift apart in pairs.
func specJSONFields(t *testing.T) []string {
	t.Helper()
	rt := reflect.TypeOf(dcv1beta1.InstanceSpec{})
	var out []string
	for i := 0; i < rt.NumField(); i++ {
		tag := rt.Field(i).Tag.Get("json")
		if tag == "" || tag == "-" {
			t.Fatalf("field %s has no json tag, so what it is called in the cluster is a guess",
				rt.Field(i).Name)
		}
		out = append(out, strings.Split(tag, ",")[0])
	}
	sort.Strings(out)
	return out
}

// 🔴 THE LIST IS CLOSED, AND THIS IS WHAT CLOSES IT. The file this replaces
// carried the rule in a comment — "identifiers only, and the list is closed" —
// and a comment cannot refuse a field. A cluster-scoped CR is readable by
// anything with cluster-wide get and turns up in `kubectl get -o yaml`, GitOps
// diffs and support bundles, so the cost of a field arriving unnoticed is higher
// here than it was in a 0700 directory.
//
// Failing this test is not a bug report. It is a question: does this value belong
// in something that many people can read? Answer it, then edit the list.
func TestInstanceSpecIsAClosedList(t *testing.T) {
	want := []string{
		"cluster", "cnpg", "compact", "extraFunctionalAreas", "grafanaSSO", "ha",
		"host", "imageRegistry", "imageVersion", "managed", "monitoring", "profile",
		"provider", "restored", "restoredAt", "tls",
	}
	got := specJSONFields(t)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("the instance declaration's fields have changed.\n got: %v\nwant: %v\n\n"+
			"Changing this list is a decision, and it has TWO halves.\n\n"+
			"ADDING a field: this object is readable by anything with cluster-wide get, and "+
			"appears in GitOps diffs and support bundles. Nothing that names a filesystem path "+
			"and nothing derived from a secret may appear here.\n\n"+
			"REMOVING one, or leaving one out: a declaration exists so a second operator can "+
			"re-run the bootstrap and converge to the SAME instance. If the field changes what "+
			"gets deployed, dropping it does not make this safer — it makes it wrong, and the "+
			"failure lands on whoever trusted it.",
			got, want)
	}
}

// 🔴 AND THE SECOND HALF OF THAT RULE HAS ITS OWN TEST, because a closed list
// makes it easy to check only the first. Every flag that changes what gets
// deployed must be recorded, and the first version of this schema missed three:
// --no-cnpg, --grafana-sso and the operator's --enable-area intent. Nothing about
// the leak test below would have noticed, because they are omissions rather than
// additions.
func TestEveryDeploymentAlteringFlagIsRecorded(t *testing.T) {
	// A State with every deployment-altering flag set AWAY from its default.
	st := &State{
		Instance:      "prod",
		Profile:       "full",
		EnableAreas:   []string{"lwm2m-ingest"},
		EnabledAreas:  []string{"device-management", "lwm2m-ingest"},
		HA:            true,
		Compact:       true,
		NoMonitoring:  true,
		NoCNPG:        true,
		NoTLS:         true,
		IngressHost:   "dc.example.com",
		ImageRegistry: "registry.example/dc",
		ImageVersion:  "v0.17.0",
	}
	spec := InstanceSpecFrom(st, ClusterBinding{Cluster: "c", Managed: true}, "local")

	for _, tc := range []struct {
		flag string
		got  bool
		want bool
	}{
		{"--ha", spec.HA, true},
		{"--compact", spec.Compact, true},
		{"--no-monitoring", spec.Monitoring, false},
		{"--no-cnpg", spec.CNPG, false},
		{"--no-tls", spec.TLS, false},
	} {
		if tc.got != tc.want {
			t.Errorf("%s did not reach the declaration (recorded %v)", tc.flag, tc.got)
		}
	}
	if spec.Host != "dc.example.com" {
		t.Errorf("--host did not reach the declaration: %q", spec.Host)
	}
	if !reflect.DeepEqual(spec.ExtraFunctionalAreas, []string{"lwm2m-ingest"}) {
		t.Errorf("--enable-area did not reach the declaration: %v", spec.ExtraFunctionalAreas)
	}
	if spec.Profile != "full" {
		t.Errorf("--profile did not reach the declaration: %q", spec.Profile)
	}

	// 🔴 THE DEFAULT SIDE, and it is the half that catches an inverted flag. Every
	// boolean above reads its default as the opposite value, so a builder that
	// hard-coded any of them would still satisfy the loop.
	def := InstanceSpecFrom(&State{Profile: "default"}, ClusterBinding{}, "local")
	if !def.Monitoring || !def.CNPG || !def.TLS {
		t.Errorf("a default instance recorded monitoring=%v cnpg=%v tls=%v; all three are on "+
			"unless opted out", def.Monitoring, def.CNPG, def.TLS)
	}
	if def.HA || def.Compact {
		t.Errorf("a default instance recorded ha=%v compact=%v", def.HA, def.Compact)
	}
	// The host is RESOLVED, not copied: an omitted host would leave the next reader
	// to re-derive a default that may have moved between releases.
	if def.Host != DefaultIngressHost {
		t.Errorf("a default instance recorded host %q, not the resolved default %q",
			def.Host, DefaultIngressHost)
	}
}

// 🔴 AND THIS IS THE LEAK TEST, which is a different question from the one above.
// The closed list catches a field being ADDED. This catches a path reaching the
// cluster through a field that is already on the list — which is how it would
// actually happen, because every one of these values is sitting on State when the
// declaration is built.
//
// The flags named here are the ones whose values are a filesystem layout on one
// operator's machine: where the escrow artifact goes, where its passphrase came
// from, which archive a restore reads. They are one-shot intents, not properties
// of the instance, and they are reconnaissance for anyone reading the cluster.
func TestNoPathReachesTheInstanceDeclaration(t *testing.T) {
	const (
		escrowPath  = "/home/someone/secrets/prod.escrow"
		restoredKey = "cm9vdC1rZXktYnl0ZXM="
		rdbArchive  = "/mnt/backups/rdb-2026"
		tsdbArchive = "s3://private-bucket/tsdb"
	)
	st := &State{
		Instance:      "prod",
		Profile:       "default",
		IngressHost:   "dc.example.com",
		ImageRegistry: "ghcr.io/devicechain-io",
		ImageVersion:  "v0.17.0",
		Escrow: EscrowPlan{
			Path:            escrowPath,
			Passphrase:      "correct-horse",
			RestoredRootKey: restoredKey,
			RestoredFrom:    escrowPath,
		},
		Restore: RestorePlan{
			RdbFrom:        rdbArchive,
			RdbTargetTime:  "2026-09-01T00:00:00Z",
			TsdbFrom:       tsdbArchive,
			TsdbTargetTime: "2026-09-01T00:00:00Z",
		},
	}

	spec := InstanceSpecFrom(st, ClusterBinding{Cluster: "kind-devicechain", Managed: true}, "local")
	raw, err := json.Marshal(spec)
	if err != nil {
		t.Fatal(err)
	}
	doc := string(raw)

	for _, leak := range []struct{ what, value string }{
		{"the escrow artifact path", escrowPath},
		{"the escrow passphrase", "correct-horse"},
		{"the restored root key", restoredKey},
		{"the relational archive", rdbArchive},
		{"the event-store archive", tsdbArchive},
	} {
		if strings.Contains(doc, leak.value) {
			t.Errorf("%s reached the instance declaration, where anything with cluster-wide "+
				"get can read it:\n%s", leak.what, doc)
		}
	}

	// The counterweight, and it is doing real work: a test that found nothing
	// would pass just as happily against a declaration that recorded nothing at
	// all. The restore FACT must be there — it is a property of the instance —
	// while every path that produced it is not.
	if !spec.Restored {
		t.Error("the declaration does not record that this instance was restored, so the test " +
			"above is measuring an empty document")
	}
	if spec.RestoredAt == nil {
		t.Error("a restored instance carries no restore timestamp")
	}
	if !strings.Contains(doc, "kind-devicechain") {
		t.Error("the declaration does not carry the cluster binding, so it is not the document " +
			"this test thinks it is")
	}
}

// The binding is fixed at bootstrap because `dcctl destroy` reads it. Rewriting
// any part of it on a re-run does not move the instance — it moves the ANSWER to
// "where is this instance", which is worse.
func TestTheClusterBindingCannotBeRewrittenByARerun(t *testing.T) {
	existing := dcv1beta1.InstanceSpec{Provider: "local", Cluster: "kind-devicechain", Managed: true}

	for _, tc := range []struct {
		name    string
		desired dcv1beta1.InstanceSpec
		names   string
	}{
		{"provider", dcv1beta1.InstanceSpec{Provider: "gcp", Cluster: "kind-devicechain", Managed: true}, "provider"},
		{"cluster", dcv1beta1.InstanceSpec{Provider: "local", Cluster: "someone-elses", Managed: true}, "cluster"},
		{"managed", dcv1beta1.InstanceSpec{Provider: "local", Cluster: "kind-devicechain", Managed: false}, "managed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateInstanceSpecChange(existing, tc.desired)
			if err == nil {
				t.Fatalf("a re-run rewrote the instance's %s and was allowed to", tc.name)
			}
			if !strings.Contains(err.Error(), tc.names) {
				t.Errorf("the refusal does not name the field that moved: %v", err)
			}
		})
	}

	// 🔴 THE COUNTERWEIGHT MATTERS MORE THAN USUAL HERE. A re-run that changes
	// nothing else is the NORMAL case — it is what `dcctl bootstrap` does every
	// time — so a guard that refused an ordinary re-run would break the supported
	// path entirely while looking careful.
	t.Run("everything else may change", func(t *testing.T) {
		desired := existing
		desired.Profile = "full"
		desired.HA = true
		desired.ImageVersion = "v0.18.0"
		desired.Host = "dc.example.com"
		desired.Monitoring = true
		if err := ValidateInstanceSpecChange(existing, desired); err != nil {
			t.Errorf("an ordinary re-run was refused: %v", err)
		}
	})
}

// 🔴 THE DECLARATION RECORDS THE OPERATOR'S REQUEST, NOT ITS EXPANSION, and the
// difference only shows up on a re-run. A profile's membership is a property of
// the release — "full" is contractually exhaustive, so it gains an area whenever
// the platform does. Freezing the expansion would make a later dcctl deploy the
// OLD contents of a profile from this object while deploying the new ones from
// the equivalent command line: two paths to one instance that no longer agree.
func TestTheDeclarationRecordsTheAreaRequestNotItsExpansion(t *testing.T) {
	st := &State{
		Profile:      "default",
		EnableAreas:  []string{"lwm2m-ingest"},
		EnabledAreas: []string{"device-management", "user-management", "lwm2m-ingest"},
	}
	spec := InstanceSpecFrom(st, ClusterBinding{}, "local")

	if spec.Profile != "default" {
		t.Errorf("the base profile was lost: %q. It cannot be recovered from the expansion, so "+
			"a re-run could not tell which profile was asked for", spec.Profile)
	}
	if !reflect.DeepEqual(spec.ExtraFunctionalAreas, []string{"lwm2m-ingest"}) {
		t.Errorf("extraFunctionalAreas is %v, want just the delta", spec.ExtraFunctionalAreas)
	}
	// The expansion must NOT be what is stored, or the release-drift above is baked in.
	if len(spec.ExtraFunctionalAreas) > 1 {
		t.Errorf("the resolved union was stored instead of the request: %v", spec.ExtraFunctionalAreas)
	}
}

// A declaration is an INPUT to a later run, so an area set this build cannot
// deploy has to be refused when it is read — not left to fail inside the chart,
// minutes into a bootstrap.
func TestADeclarationNamingAnUnknownAreaIsRefused(t *testing.T) {
	err := ValidateInstanceSpec(dcv1beta1.InstanceSpec{
		Provider:             "local",
		Profile:              "default",
		ExtraFunctionalAreas: []string{"no-such-area"},
	})
	if err == nil {
		t.Fatal("a declaration naming an area that does not exist was accepted")
	}
	if !strings.Contains(err.Error(), "area set") {
		t.Errorf("the refusal does not say what is wrong: %v", err)
	}

	// The counterweight: a real area must pass, or this bans the feature.
	if err := ValidateInstanceSpec(dcv1beta1.InstanceSpec{
		Provider:             "local",
		Profile:              "default",
		ExtraFunctionalAreas: []string{"lwm2m-ingest"},
	}); err != nil {
		t.Errorf("a valid area set was refused: %v", err)
	}
}

// --no-tls and --no-monitoring are conveniences on a command line. A declaration
// is read by someone asking what the instance IS, so it states them positively —
// and getting the inversion backwards would record the exact opposite of the
// truth while looking entirely reasonable.
func TestTheDeclarationStatesExposureAndMonitoringPositively(t *testing.T) {
	on := InstanceSpecFrom(&State{}, ClusterBinding{}, "local")
	if !on.TLS || !on.Monitoring {
		t.Errorf("a default instance recorded tls=%v monitoring=%v; both are on unless opted out",
			on.TLS, on.Monitoring)
	}
	off := InstanceSpecFrom(&State{NoTLS: true, NoMonitoring: true}, ClusterBinding{}, "local")
	if off.TLS || off.Monitoring {
		t.Errorf("--no-tls/--no-monitoring recorded tls=%v monitoring=%v", off.TLS, off.Monitoring)
	}
}

// 🔴 THE ANNOTATIONS ARE PART OF THE PUBLISHED OBJECT AND SAT OUTSIDE EVERY
// CLOSED-LIST CHECK. Both lists above look at the spec, so a path or a secret
// could reach the cluster through an annotation with nothing able to notice —
// the same leak the spec is guarded against, one field away from the guard.
func TestOnlyProvenanceAnnotationsAreWritten(t *testing.T) {
	want := map[string]bool{
		dcv1beta1.AnnotationLastAppliedBy: true,
		dcv1beta1.AnnotationLastAppliedAt: true,
	}

	inst := &dcv1beta1.Instance{}
	applyProvenance(inst, "v0.17.0")

	if len(inst.Annotations) != len(want) {
		t.Fatalf("the declaration carries %d annotations, want %d: %v",
			len(inst.Annotations), len(want), inst.Annotations)
	}
	for k, v := range inst.Annotations {
		if !want[k] {
			t.Errorf("unexpected annotation %q=%q. Annotations are published with the object; "+
				"nothing that names a path and nothing derived from a secret may go here either", k, v)
		}
	}

	// Foreign annotations — kubectl's last-applied, a GitOps tracking id — must
	// survive, or dcctl would strip other tools' bookkeeping on every re-run.
	existing := &dcv1beta1.Instance{}
	existing.Annotations = map[string]string{"argocd.argoproj.io/tracking-id": "x"}
	applyProvenance(existing, "v0.17.0")
	if existing.Annotations["argocd.argoproj.io/tracking-id"] != "x" {
		t.Error("a foreign annotation was dropped")
	}
}

// The finalizer is what keeps a hand-deleted declaration readable until destroy
// has run, and dcctl writes it on every bootstrap — so "already there" is the
// normal case, not the edge one. Appending a duplicate produces an object the API
// server rejects, which would turn a routine re-run into a failed write.
func TestAddFinalizerIsIdempotent(t *testing.T) {
	inst := &dcv1beta1.Instance{}
	inst.Finalizers = []string{"someone.else/keep-around"}

	addFinalizer(inst)
	addFinalizer(inst)

	seen := 0
	for _, f := range inst.Finalizers {
		if f == dcv1beta1.FinalizerInstance {
			seen++
		}
	}
	if seen != 1 {
		t.Errorf("the declaration carries dcctl's finalizer %d times: %v", seen, inst.Finalizers)
	}
	// A foreign finalizer is another controller's promise to clean something up.
	// Dropping it would leave that cleanup undone, silently.
	if len(inst.Finalizers) != 2 || inst.Finalizers[0] != "someone.else/keep-around" {
		t.Errorf("another controller's finalizer did not survive: %v", inst.Finalizers)
	}
}

// removeFinalizer's RETURN VALUE is what ReleaseInstanceDeclaration decides on:
// false with no deletion pending means there was nothing to release, and the
// command says so instead of reporting a success it did not achieve.
func TestRemoveFinalizerReportsWhetherItRemovedAnything(t *testing.T) {
	t.Run("it reports removing the one that was there", func(t *testing.T) {
		inst := &dcv1beta1.Instance{}
		inst.Finalizers = []string{"someone.else/keep-around", dcv1beta1.FinalizerInstance}

		if !removeFinalizer(inst) {
			t.Error("removing a finalizer that was present reported that nothing changed, so the " +
				"release command would claim there was nothing to do")
		}
		if len(inst.Finalizers) != 1 || inst.Finalizers[0] != "someone.else/keep-around" {
			t.Errorf("the remaining finalizers are %v; dcctl removed more than its own",
				inst.Finalizers)
		}
	})

	t.Run("it reports removing nothing from a declaration that has none", func(t *testing.T) {
		inst := &dcv1beta1.Instance{}
		inst.Finalizers = []string{"someone.else/keep-around"}

		if removeFinalizer(inst) {
			t.Error("removing an absent finalizer reported a removal; the release command would " +
				"then write and delete a declaration it was never holding")
		}
		if len(inst.Finalizers) != 1 {
			t.Errorf("the remaining finalizers are %v", inst.Finalizers)
		}
	})

	t.Run("an empty finalizer list is not a special case", func(t *testing.T) {
		inst := &dcv1beta1.Instance{}
		if removeFinalizer(inst) {
			t.Error("removing from an empty list reported a removal")
		}
		if len(inst.Finalizers) != 0 {
			t.Errorf("the remaining finalizers are %v", inst.Finalizers)
		}
	})
}

// The phase is one annotation on an object several writers annotate. Writing it as
// a wholesale replacement would strip kubectl's last-applied, a GitOps tracking id,
// or dcctl's own provenance — quietly, on a green run.
func TestSetPhaseLeavesOtherAnnotationsAlone(t *testing.T) {
	inst := &dcv1beta1.Instance{}
	inst.Annotations = map[string]string{
		"argocd.argoproj.io/tracking-id":  "x",
		dcv1beta1.AnnotationLastAppliedBy: "v0.17.0",
	}

	setPhase(inst, dcv1beta1.PhaseBootstrapping)

	if inst.Annotations[dcv1beta1.AnnotationPhase] != dcv1beta1.PhaseBootstrapping {
		t.Errorf("the phase was not recorded: %v", inst.Annotations)
	}
	if inst.Annotations["argocd.argoproj.io/tracking-id"] != "x" {
		t.Error("another tool's annotation was dropped when the phase was recorded")
	}
	if inst.Annotations[dcv1beta1.AnnotationLastAppliedBy] != "v0.17.0" {
		t.Error("dcctl's own provenance was dropped when the phase was recorded")
	}

	// A declaration with no annotations at all is the fresh-install case, and it
	// must not panic on the nil map.
	fresh := &dcv1beta1.Instance{}
	setPhase(fresh, dcv1beta1.PhaseBootstrapping)
	if fresh.Annotations[dcv1beta1.AnnotationPhase] != dcv1beta1.PhaseBootstrapping {
		t.Errorf("a fresh declaration recorded no phase: %v", fresh.Annotations)
	}

	// And the phase MOVES: it records what this run is trying to do now, so a
	// destroy has to be able to overwrite a bootstrap's value.
	setPhase(inst, dcv1beta1.PhaseDestroying)
	if inst.Annotations[dcv1beta1.AnnotationPhase] != dcv1beta1.PhaseDestroying {
		t.Errorf("the phase did not move: %v", inst.Annotations)
	}
}
