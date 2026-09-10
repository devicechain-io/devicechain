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

// specJSONFields lists the JSON names InstanceSpec actually serialises.
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
		"cluster", "compact", "enabledFunctionalAreas", "ha", "host",
		"imageRegistry", "imageVersion", "managed", "monitoring", "profile",
		"provider", "restored", "restoredAt", "tls",
	}
	got := specJSONFields(t)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("the instance declaration's fields have changed.\n got: %v\nwant: %v\n\n"+
			"Adding one is a decision, not an edit: this object is readable by anything with "+
			"cluster-wide get. Nothing that names a filesystem path and nothing derived from a "+
			"secret may appear here. If the new field is neither, add it to this list deliberately.",
			got, want)
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

// profile and enabledFunctionalAreas are two ways of naming one set, and the
// chart treats them as mutually exclusive. A declaration carrying both would
// leave the next run to pick one.
func TestAProfileAndAnExplicitAreaSetAreMutuallyExclusive(t *testing.T) {
	err := ValidateInstanceSpec(dcv1beta1.InstanceSpec{
		Provider:               "local",
		Profile:                "default",
		EnabledFunctionalAreas: []string{"device-management"},
	})
	if err == nil {
		t.Fatal("a declaration carrying both a profile and an explicit area set was accepted")
	}

	// ...and the builder must not produce one. An explicit set REPLACES the
	// profile; State carries both because the profile is still the base the extras
	// were resolved against.
	st := &State{Profile: "default", EnableAreas: []string{"lwm2m-ingest"},
		EnabledAreas: []string{"device-management", "lwm2m-ingest"}}
	spec := InstanceSpecFrom(st, ClusterBinding{}, "local")
	if spec.Profile != "" {
		t.Errorf("the builder recorded profile %q alongside an explicit area set", spec.Profile)
	}
	if err := ValidateInstanceSpec(spec); err != nil {
		t.Errorf("the builder produced a declaration it will not accept: %v", err)
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
