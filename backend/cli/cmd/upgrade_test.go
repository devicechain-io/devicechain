// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

// 🔴 OFF BY DEFAULT, BECAUSE ON BY DEFAULT IS THE DEFECT. --skip-infrastructure is the
// way back to an upgrade that leaves an instance's broker and event store on old
// settings; an upgrade that did that without being asked is what the flag's absence
// fixes. And the flag has to reach the upgrade at all, which the RunE's composition is
// the only thing that says — so it is read off the source, as the value it threads.
func TestSkipInfrastructureIsOffByDefaultAndReachesTheUpgrade(t *testing.T) {
	f := upgradeCmd.Flags().Lookup("skip-infrastructure")
	if f == nil {
		t.Fatal("dcctl upgrade has no --skip-infrastructure flag")
	}
	if f.DefValue != "false" {
		t.Errorf("--skip-infrastructure defaults to %s; an upgrade must apply the infrastructure unless told not to", f.DefValue)
	}

	file, err := parser.ParseFile(token.NewFileSet(), "upgrade.go", nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatal(err)
	}
	threaded := false
	ast.Inspect(file, func(n ast.Node) bool {
		kv, ok := n.(*ast.KeyValueExpr)
		if !ok {
			return true
		}
		k, kok := kv.Key.(*ast.Ident)
		v, vok := kv.Value.(*ast.Ident)
		if kok && vok && k.Name == "SkipInfrastructure" && v.Name == "upgradeSkipInfra" {
			threaded = true
		}
		return true
	})
	if !threaded {
		t.Error("the upgrade command does not pass --skip-infrastructure to UpgradeOptions.SkipInfrastructure")
	}
	upgradeSkipInfra = true
	t.Cleanup(func() { upgradeSkipInfra = false })
	if err := upgradeCmd.Flags().Set("skip-infrastructure", "false"); err != nil || upgradeSkipInfra {
		t.Errorf("the flag is not bound to upgradeSkipInfra (%v)", err)
	}
}
