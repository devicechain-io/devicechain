// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"

	"github.com/devicechain-io/dc-microservice/config"
)

// 🔴 AN UPGRADE MINTS NO BROKER AUTHORITY, SO THE APPLY MUST TAKE IT FROM WHAT THE
// INSTANCE RUNS ON. The CA used to be emitted only from the mint, which only a
// bootstrap performs — so an upgrade applying the instance root would have passed no
// CA, and the root's default for that is an empty one: a broker no client can verify.
//
// The State here is built the way an upgrade builds it, from the configuration document
// the services read (applyDeployedInfrastructure), with no mint at all.
func TestInfraVarsTakeTheBrokerAuthorityFromTheInstance(t *testing.T) {
	deployed := &config.InstanceConfiguration{}
	deployed.Infrastructure.Nats.Tls.Enabled = true
	deployed.Infrastructure.Nats.Tls.Ca = "-----BEGIN CERTIFICATE----- running-instance-ca"

	st := &State{Instance: "prod", KubeContext: "gke_p_z_c", Values: map[string]string{}}
	applyDeployedInfrastructure(st, deployed)

	want := "nats_ca_cert_pem=-----BEGIN CERTIFICATE----- running-instance-ca"
	var got []string
	for _, v := range infraVars(st) {
		if strings.HasPrefix(v, "nats_ca_cert_pem=") {
			got = append(got, v)
		}
	}
	if len(got) != 1 || got[0] != want {
		t.Fatalf("an upgrade-shaped State renders nats_ca_cert_pem as %q, want exactly [%q]. Without "+
			"it the broker's apply falls back to an EMPTY authority and no client can verify the "+
			"broker", got, want)
	}

	// And a bootstrap, which mints and records the same value, renders the same var:
	// one source on both paths.
	minted := &State{Instance: "prod", KubeContext: "gke_p_z_c", Values: map[string]string{
		"natsCA": "-----BEGIN CERTIFICATE----- running-instance-ca"}}
	minted.NATSTLS = &natsTLSMaterial{CACertPEM: "-----BEGIN CERTIFICATE----- running-instance-ca"}
	if a, b := strings.Join(infraVars(st), "\n"), strings.Join(infraVars(minted), "\n"); a != b {
		t.Errorf("a bootstrap and an upgrade holding the same authority render different vars:\n"+
			"upgrade:\n%s\nbootstrap:\n%s", a, b)
	}
}

// 🔴 A REHEARSAL THAT LEFT THE INFRASTRUCTURE OUT WOULD REHEARSE A DIFFERENT RUN. The
// broker and event store restart during a real upgrade; a dry run that did not say so
// would describe an upgrade that touches only the services.
func TestTheUpgradeRehearsalNamesTheInfrastructureApply(t *testing.T) {
	out := captureStdout(t, func() { sayUpgradeDryRun(&State{Values: map[string]string{}}) })
	if !strings.Contains(out, "message broker and event store") {
		t.Errorf("the upgrade rehearsal does not say it applies the instance's message broker and "+
			"event store:\n%s", out)
	}
}

// 🔴 THE ORDER IS THE GUARANTEE, AND IT IS ASSERTED ON THE SOURCE BECAUSE Upgrade NEEDS A
// CLUSTER. The plan has to be made — and judged — before the declaration is rewritten, so
// a refusal means nothing moved; and the apply has to come after the broker's certificate
// restart and before the services roll, so the disruptions come one at a time and the
// services move onto a broker and store already at the release.
func TestTheUpgradePlansBeforeItWritesAndAppliesBeforeTheServices(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "upgrade.go", nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatal(err)
	}
	var upgrade *ast.FuncDecl
	for _, d := range file.Decls {
		if fd, ok := d.(*ast.FuncDecl); ok && fd.Name.Name == "Upgrade" {
			upgrade = fd
		}
	}
	if upgrade == nil {
		t.Fatal("upgrade.go declares no Upgrade")
	}
	first := map[string]token.Pos{}
	count := map[string]int{}
	ast.Inspect(upgrade.Body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		if id, ok := call.Fun.(*ast.Ident); ok {
			count[id.Name]++
			if _, seen := first[id.Name]; !seen {
				first[id.Name] = call.Pos()
			}
		}
		return true
	})
	for _, name := range []string{"planUpgradeInfra", "recordUpgradedVersion", "renewBrokerCertificate",
		"applyUpgradeInfra", "rolloutWithLoginResize"} {
		if _, ok := first[name]; !ok {
			t.Fatalf("Upgrade does not call %s, so an upgraded instance's broker and event store are "+
				"never applied, or applied out of order", name)
		}
	}
	if count["applyUpgradeInfra"] != 1 {
		t.Errorf("Upgrade calls applyUpgradeInfra %d times, want exactly once", count["applyUpgradeInfra"])
	}
	order := []string{"planUpgradeInfra", "recordUpgradedVersion"}
	if first[order[0]] > first[order[1]] {
		t.Errorf("the infrastructure is planned at %s, after the declaration is rewritten at %s: a "+
			"refusal would no longer mean nothing moved", fset.Position(first[order[0]]), fset.Position(first[order[1]]))
	}
	if !(first["renewBrokerCertificate"] < first["applyUpgradeInfra"] && first["applyUpgradeInfra"] < first["rolloutWithLoginResize"]) {
		t.Errorf("want renewBrokerCertificate < applyUpgradeInfra < rolloutWithLoginResize, got %s, %s, %s",
			fset.Position(first["renewBrokerCertificate"]), fset.Position(first["applyUpgradeInfra"]),
			fset.Position(first["rolloutWithLoginResize"]))
	}
}
