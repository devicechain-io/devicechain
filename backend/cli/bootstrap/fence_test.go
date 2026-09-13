// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

// 🔴 AN INSTANCE BUILT BEFORE THE CREDENTIALS MOVED MUST BE REFUSED, NOT UPGRADED.
//
// Its state still manages the resources that used to hold those credentials, and this
// configuration no longer declares them — so the apply would not fail, it would
// SUCCEED, having deleted the Secret the database authenticates its own services with
// and the certificate authority the broker's clients trust.
//
// The address here is a literal for the same reason the list is: it names a resource
// that no longer exists in this tree, so nothing would fail to compile if it moved.
func TestAnInstanceBuiltBeforeTheCutoverIsRefused(t *testing.T) {
	f := &fakeState{addresses: []string{"module.cnpg_rdb.kubernetes_secret_v1.app"}}

	err := checkNoRetiredInfrastructure(context.Background(), f, "prod")
	if err == nil {
		t.Fatal("an instance whose state still holds the retired credentials was accepted: " +
			"the apply would delete them")
	}
	for _, want := range []string{
		"module.cnpg_rdb.kubernetes_secret_v1.app", // which resource
		"dcctl destroy prod",                       // what to do about it
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not mention %q: %v", want, err)
		}
	}
}

// ...and the counterweight, which is the half that makes the fence a guard rather
// than a ban: an instance with none of them must pass.
func TestAnInstanceBuiltAfterTheCutoverPasses(t *testing.T) {
	f := &fakeState{addresses: []string{
		"module.namespace.kubernetes_namespace_v1.this[0]",
		"module.cnpg_rdb.helm_release.cluster",
	}}
	if err := checkNoRetiredInfrastructure(context.Background(), f, "prod"); err != nil {
		t.Errorf("a current instance was refused: %v", err)
	}
}

// 🔴 EVERY RETIRED ADDRESS MUST BE FENCED, not just the first one anybody thought of.
// A missing entry is a silent hole: that one resource is destroyed on an apply that
// reports success. Each is exercised on its own, so a list that happens to catch an
// instance through a DIFFERENT entry does not hide a missing one.
func TestEveryRetiredResourceIsFenced(t *testing.T) {
	for _, address := range []string{
		"module.cnpg_rdb.kubernetes_secret_v1.app",
		"module.cnpg_tsdb.kubernetes_secret_v1.app",
		"module.object_store[0].kubernetes_secret_v1.credentials",
		"module.nats.tls_private_key.ca[0]",
		"module.nats.tls_self_signed_cert.ca[0]",
		"module.nats.tls_private_key.server[0]",
		"module.nats.tls_cert_request.server[0]",
		"module.nats.tls_locally_signed_cert.server[0]",
		"module.nats.kubernetes_secret_v1.nats_tls[0]",
	} {
		t.Run(address, func(t *testing.T) {
			f := &fakeState{addresses: []string{address}}
			if err := checkNoRetiredInfrastructure(context.Background(), f, "prod"); err == nil {
				t.Errorf("state holding %s was accepted, so an apply would destroy it silently",
					address)
			}
		})
	}
}

// 🔴 "CANNOT TELL" IS NOT "NOTHING RETIRED HERE". A state that will not read is
// exactly when guessing is worst: the guess that lets the run continue is the one
// that deletes the credentials.
func TestAnUnreadableStateStopsTheRunRatherThanPassingTheFence(t *testing.T) {
	boom := errors.New("state file is corrupt")
	err := checkNoRetiredInfrastructure(context.Background(), &fakeState{showErr: boom}, "prod")
	if err == nil {
		t.Fatal("an unreadable state passed the fence")
	}
	if !errors.Is(err, boom) {
		t.Errorf("the failure lost its cause: %v", err)
	}
}

// 🔴 THE QUESTION THIS FILE'S DESIGN HAS TO ANSWER: DOES ANYTHING STILL REACH THE
// TAKEOVER?
//
// adoptChartWrittenInstanceConfig exists so an operator whose CHART wrote the
// configuration Secret is not forced to rebuild. The fence refuses instances built
// before the credentials moved. If those were the same population, the takeover
// would be code that cannot run — constructed correctly, connected to nothing — and
// it should be deleted rather than explained.
//
// They are NOT the same population, and this is the check that says so. The fence
// keys on OpenTofu STATE holding resources this build no longer declares. An
// instance installed with plain Helm has no such state — it has no state at all —
// so it passes the fence and reaches the takeover with a chart-written Secret.
//
// 🔑 THE RELEASE NAME AND NAMESPACE ARE LITERALS HERE, NOT helmReleaseName. They
// are what the PUBLISHED DOCUMENTATION tells an operator to run — `helm install dc
// deploy/helm/devicechain` with no -n, which is release "dc" in namespace
// "default". Reading them from the constant would make this test agree with dcctl
// by construction while the population it claims to serve quietly stopped matching.
func TestTheChartWrittenTakeoverIsStillReachablePastTheFence(t *testing.T) {
	// 1. The population: an instance installed the way the docs describe has no
	//    infrastructure state, so the fence does not fire on it.
	plainHelm := &fakeState{}
	if err := checkNoRetiredInfrastructure(context.Background(), plainHelm, "dctest"); err != nil {
		t.Fatalf("the fence refused an instance installed with plain Helm, which has no "+
			"infrastructure state at all: %v", err)
	}

	// 2. ...and its Secret is one the takeover claims, stamped as the documented
	//    install produces it.
	c := fake.NewSimpleClientset(helmWrittenConfigSecret("dc", "default"))
	if err := adoptChartWrittenInstanceConfig(context.Background(), c, "dctest", testUID, "dc", "default"); err != nil {
		t.Fatalf("taking over a plain-Helm instance's Secret: %v", err)
	}
	s, err := c.CoreV1().Secrets("dctest").Get(context.Background(), "dci-dctest-config", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if s.Annotations[annotationManagedBy] != managedByDcctl {
		t.Fatal("the takeover did not claim a plain-Helm instance's Secret, so nothing " +
			"reaches it and it should be deleted rather than kept")
	}

	// 3. The counterweight: the population the fence DOES refuse never gets here, so
	//    the takeover is not what serves them. If this ever stops failing, the fence
	//    has stopped covering pre-cutover instances and the takeover is being asked
	//    to do a job it cannot do.
	preCutover := &fakeState{addresses: []string{retiredStateAddresses[0]}}
	if err := checkNoRetiredInfrastructure(context.Background(), preCutover, "dctest"); err == nil {
		t.Error("an instance built before the cutover passed the fence, so it would reach " +
			"the takeover — which cannot help it: its credentials are still OpenTofu's")
	}
}

// The standing instruction attached to the test above: when plain Helm is withdrawn,
// the population reached by the takeover goes with it, and the takeover should be
// deleted in that change rather than left as code nothing can run.
//
// This is deliberately a test rather than a comment. A comment saying "delete this
// later" is read by whoever is already looking at the file; this fails, by name, in
// front of whoever removes the documented install path — which is the person who
// needs to know.
func TestTheTakeoverGoesWhenPlainHelmDoes(t *testing.T) {
	if !plainHelmInstallIsDocumented() {
		t.Fatal("plain `helm install` is no longer a documented install path, so the " +
			"population adoptChartWrittenInstanceConfig serves no longer exists. Delete the " +
			"takeover, its tests, and this one — it is now code nothing can reach")
	}
}

// plainHelmInstallIsDocumented reports whether the published deployment docs still
// tell an operator to install the chart with plain Helm.
//
// 🔴 A FILE IT CANNOT READ IS "CANNOT TELL", NOT "WITHDRAWN". The action this
// answer drives is deleting the takeover, so reading an absent or unreadable docs
// tree as "plain Helm is gone" would delete working code on the strength of a
// missing file — a stripped checkout, a moved path, a test run from somewhere
// unexpected. Cannot-tell therefore skips rather than fails, which is the same rule
// the rest of this package applies to every read whose destructive answer is the
// cheap one.
func plainHelmInstallIsDocumented() bool {
	// Outside this module on purpose: the claim is about what the DOCS say, and
	// nothing inside dcctl can stand in for that. CI passes -count=1, which is what
	// makes reading a file the test cache does not track safe here.
	pages, err := filepath.Glob(filepath.Join("..", "..", "..", "docs", "docs", "deployment", "*.md"))
	if err != nil || len(pages) == 0 {
		return true // cannot tell — see above
	}
	for _, p := range pages {
		b, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		if strings.Contains(string(b), "helm install dc ") {
			return true
		}
	}
	return false
}
