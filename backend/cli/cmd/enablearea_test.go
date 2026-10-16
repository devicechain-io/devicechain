// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"
)

// A request that names no area, or a typo, is answered from the command line: the
// catalog check runs before the provider is asked for any cluster.
func TestEnableAreaJudgesTheNamesBeforeAnyCluster(t *testing.T) {
	if err := enableAreaCmd.Args(enableAreaCmd, []string{"local", "prod"}); err == nil {
		t.Error("enable-area with no area named was accepted")
	}
	if err := enableAreaCmd.Args(enableAreaCmd, []string{"local", "prod", "mcp"}); err != nil {
		t.Errorf("enable-area local prod mcp was refused: %v", err)
	}

	err := enableAreaCmd.RunE(enableAreaCmd, []string{"local", "prod", "update-managment"})
	if err == nil || !strings.Contains(err.Error(), `unknown functional area "update-managment"`) {
		t.Fatalf("a misspelled area returned %v, want the catalog refusal", err)
	}
	if !strings.Contains(err.Error(), "update-management") {
		t.Errorf("the refusal does not offer the names that would have been accepted: %v", err)
	}
}

// 🔑 THE KUBE-CONTEXT REFUSAL AND EVERYTHING ELSE ABOUT REACHING THE INSTANCE ARE
// UPGRADE'S, because this verb IS the upgrade with the areas appended. What makes that
// true is the RunE's composition — it calls bootstrap.Upgrade, and hands it the flag
// and the areas — so that is read off the source.
func TestEnableAreaIsTheUpgradeWithTheAreasAndTheContext(t *testing.T) {
	file, err := parser.ParseFile(token.NewFileSet(), "enablearea.go", nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatal(err)
	}
	threads := map[string]string{
		"KubeContext": "enableAreaKubeContext",
		"DryRun":      "enableAreaDryRun",
		"EscrowFile":  "enableAreaEscrowFile",
		"EnableAreas": "areas",
	}
	found := map[string]bool{}
	callsUpgrade := false
	ast.Inspect(file, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.KeyValueExpr:
			k, kok := n.Key.(*ast.Ident)
			v, vok := n.Value.(*ast.Ident)
			if kok && vok && threads[k.Name] == v.Name {
				found[k.Name] = true
			}
		case *ast.SelectorExpr:
			if x, ok := n.X.(*ast.Ident); ok && x.Name == "bootstrap" && n.Sel.Name == "Upgrade" {
				callsUpgrade = true
			}
		}
		return true
	})
	if !callsUpgrade {
		t.Error("enable-area does not run bootstrap.Upgrade, so it does not share its binding refusals")
	}
	for k, v := range threads {
		if !found[k] {
			t.Errorf("enable-area does not pass %s to UpgradeOptions.%s", v, k)
		}
	}
	if f := enableAreaCmd.Flags().Lookup("kube-context"); f == nil || f.DefValue != "" {
		t.Errorf("--kube-context missing or defaulted: %+v", f)
	}
}
