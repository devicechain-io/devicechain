// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"go/ast"
	"go/token"
	"strings"
	"testing"

	"github.com/devicechain-io/dc-microservice/natsauth"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

// 🔴 THE ORDER THAT KEEPS DEVICES CONNECTING THROUGH THE UPGRADE.
//
// The broker cannot reload auth_callout, so telling it to encrypt is a rolling restart, and the
// responder that reads the encrypted requests is a service that `dcctl upgrade` rolls AFTER the
// broker. These tests hold the arrangement that makes that safe: an instance whose broker is not
// encrypting yet has the key withheld from its first apply, and handed over by a second apply once
// the services have rolled.

func xkeyVarOf(vars []string) string {
	for _, v := range vars {
		if strings.HasPrefix(v, "nats_callout_xkey_public=") {
			return strings.TrimPrefix(v, "nats_callout_xkey_public=")
		}
	}
	return ""
}

func TestTheBrokerXKeyIsReadOutOfItsRenderedConfiguration(t *testing.T) {
	_, pub, err := natsauth.GenerateXKey()
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	cm := func(conf string) *corev1.ConfigMap {
		return &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "dc-nats-config", Namespace: "dci-prod"},
			Data: map[string]string{"nats.conf": conf}}
	}
	// The shape the chart renders: the merge block as JSON.
	encrypting := `{"authorization": {"auth_callout": {"account": "APP", "auth_users": ["dc_service"], "issuer": "AX", "xkey": "` + pub + `"}}}`
	plain := `{"authorization": {"auth_callout": {"account": "APP", "auth_users": ["dc_service"], "issuer": "AX"}}}`

	if got := deployedBrokerXKeyIn(ctx, fake.NewSimpleClientset(cm(encrypting)), "dci-prod", "dc-nats"); got != pub {
		t.Errorf("read %q from a broker that encrypts to %q", got, pub)
	}
	if got := deployedBrokerXKeyIn(ctx, fake.NewSimpleClientset(cm(plain)), "dci-prod", "dc-nats"); got != "" {
		t.Errorf("read %q from a broker that does not encrypt", got)
	}
	// Every problem answers "nothing", which defers the encryption: the safe direction.
	if got := deployedBrokerXKeyIn(ctx, fake.NewSimpleClientset(), "dci-prod", "dc-nats"); got != "" {
		t.Errorf("a missing ConfigMap read as %q", got)
	}
}

func TestTheXKeyIsHeldBackFromABrokerThatIsNotEncryptingYet(t *testing.T) {
	creds, err := natsauth.GenerateCredentials()
	if err != nil {
		t.Fatal(err)
	}
	fresh := func() *State {
		st := &State{Instance: "prod", KubeContext: "kind-x", Values: map[string]string{}}
		setBrokerCredentialValues(st, creds)
		return st
	}

	t.Run("a broker that is not encrypting is held back", func(t *testing.T) {
		withDeployedBrokerXKey(t, "")
		st := fresh()
		if !withholdXKeyFromBroker(context.Background(), st) {
			t.Fatal("the key was handed to a broker whose responder has not rolled yet")
		}
		if xkeyVarOf(infraVars(st)) != "" {
			t.Error("the broker apply carries the key although it was held back")
		}
		if !xkeyPending(st) {
			t.Error("a run that held the key back does not owe the broker a second apply")
		}
		if st.Values["natsCalloutXKeyPublic"] != creds.XKeyPublic {
			t.Error("the document must still record the key: the responder is told it before the broker is")
		}
	})
	t.Run("once the services have rolled, the same broker is given it", func(t *testing.T) {
		withDeployedBrokerXKey(t, "")
		st := fresh()
		st.XKeyServicesReady = true
		if withholdXKeyFromBroker(context.Background(), st) {
			t.Fatal("the key is still held back after the services rolled")
		}
		if got := xkeyVarOf(infraVars(st)); got != creds.XKeyPublic {
			t.Errorf("the second apply carries %q, want %q", got, creds.XKeyPublic)
		}
		if xkeyPending(st) {
			t.Error("a run that applied the key still owes a second apply")
		}
	})
	t.Run("a broker that already encrypts to this key is not rolled again", func(t *testing.T) {
		withDeployedBrokerXKey(t, creds.XKeyPublic)
		st := fresh()
		if withholdXKeyFromBroker(context.Background(), st) {
			t.Fatal("the key was held back from a broker that already has it")
		}
		if got := xkeyVarOf(infraVars(st)); got != creds.XKeyPublic {
			t.Errorf("the apply carries %q, which would DROP the key from a broker that has it", got)
		}
	})
	t.Run("a broker encrypting to some other key is brought to this one, after the services", func(t *testing.T) {
		_, otherPub, _ := natsauth.GenerateXKey()
		withDeployedBrokerXKey(t, otherPub)
		if !withholdXKeyFromBroker(context.Background(), fresh()) {
			t.Fatal("a broker encrypting to a different key was switched in the same apply as the services")
		}
	})
	t.Run("an instance with no callout has nothing to hold back", func(t *testing.T) {
		st := &State{Instance: "prod", Values: map[string]string{}}
		if withholdXKeyFromBroker(context.Background(), st) || xkeyPending(st) {
			t.Error("an xkey step was planned for an instance with no device callout")
		}
	})
}

// 🔑 THROUGH THE REAL SETTLE, because the hold-back is only worth anything if the settle
// reaches it on the path an upgrade takes. A legacy instance (the seed in the document, no xkey)
// gets an xkey introduced and is held back; the second settle, run after the services, hands the
// broker the same key, and neither replaces the one before it.
func TestAnUpgradeOfALegacyInstanceHoldsTheKeyBackThenHandsItOver(t *testing.T) {
	creds, err := natsauth.GenerateCredentials()
	if err != nil {
		t.Fatal(err)
	}
	st := &State{Instance: "prod", KubeContext: "kind-x", Values: map[string]string{
		"natsCA": "ca", "natsCalloutIssuerSeed": creds.IssuerSeed, "natsServicePassword": creds.ServicePassword,
		"natsSysPassword": creds.SysPassword}}
	if err := introduceCalloutXKey(st); err != nil {
		t.Fatal(err)
	}
	introduced := st.Values["natsCalloutXKeyPublic"]
	if introduced == "" {
		t.Fatal("nothing was introduced")
	}
	stubUpgradeReads(t, clusterArchiveState{Exists: true}, healthyInfra("16Gi", "32Gi"),
		natsauth.DeployedHashes{Service: creds.ServicePasswordBcrypt, Sys: creds.SysPasswordBcrypt})
	withDeployedBrokerXKey(t, "")

	if err := settleUpgradeInfraInputs(context.Background(), st); err != nil {
		t.Fatal(err)
	}
	if got := xkeyVarOf(infraVars(st)); got != "" {
		t.Fatalf("the FIRST apply tells the broker to encrypt (%q) while the running responder cannot read it: "+
			"every device connect is refused until the services roll", got)
	}
	if !xkeyPending(st) {
		t.Fatal("the held-back key is not owed to the broker afterwards")
	}
	said := false
	for _, n := range st.InfraNotes {
		said = said || strings.Contains(n, "encrypting its device auth callout")
	}
	if !said {
		t.Error("the plan does not say why the broker's callout is left as it is")
	}

	st.XKeyServicesReady = true
	if err := settleUpgradeInfraInputs(context.Background(), st); err != nil {
		t.Fatal(err)
	}
	for _, n := range st.InfraNotes {
		if strings.Contains(n, "encrypting its device auth callout") {
			t.Errorf("the second apply still says it leaves the broker as it is: %q", n)
		}
	}
	if got := xkeyVarOf(infraVars(st)); got != introduced {
		t.Fatalf("the second apply carries %q, want the key the services were given (%q)", got, introduced)
	}
}

// ASSERTED ON THE SOURCE, like the other order tests, because Upgrade needs a cluster: the second
// broker apply exists, runs exactly once, only for a plan that was made, and only AFTER the
// services have rolled. Move it before the rollout and the window it exists to close is back.
func TestTheSecondBrokerApplyFollowsTheServiceRollout(t *testing.T) {
	upgrade, fset := upgradeFuncDecl(t)
	calls, guards := guardsOf(upgrade, "enableCalloutEncryption")
	if calls != 1 {
		t.Fatalf("Upgrade calls enableCalloutEncryption %d times, want exactly once", calls)
	}
	if len(guards[0]) != 1 || guards[0][0] != "infra != nil && xkeyPending(st)" {
		t.Errorf("the second apply runs under %q, want `infra != nil && xkeyPending(st)`", strings.Join(guards[0], " && "))
	}
	first := map[string]token.Pos{}
	ast.Inspect(upgrade.Body, func(n ast.Node) bool {
		if call, ok := n.(*ast.CallExpr); ok {
			if id, ok := call.Fun.(*ast.Ident); ok {
				if _, seen := first[id.Name]; !seen {
					first[id.Name] = call.Pos()
				}
			}
		}
		return true
	})
	for _, before := range []string{"applyUpgradeInfra", "rolloutWithLoginResize"} {
		if first[before] == 0 || first["enableCalloutEncryption"] < first[before] {
			t.Errorf("enableCalloutEncryption (%s) does not come after %s (%s)",
				fset.Position(first["enableCalloutEncryption"]), before, fset.Position(first[before]))
		}
	}
}

// A bootstrap of a NEW instance has no responder to wait for, so the broker is given the key in
// its one and only apply.
func TestABootstrapOfANewInstanceGivesTheBrokerTheKeyAtOnce(t *testing.T) {
	creds, err := natsauth.GenerateCredentials()
	if err != nil {
		t.Fatal(err)
	}
	st := &State{Instance: "prod", Values: map[string]string{}}
	setBrokerCredentialValues(st, creds)
	if got := xkeyVarOf(infraVars(st)); got != creds.XKeyPublic {
		t.Errorf("a new instance's broker is given %q, want %q", got, creds.XKeyPublic)
	}
}
