// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
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

// --mqtt-body-credential exists on both verbs, is off by default (an upgrade that is not
// asked must carry the release's value, which only an unset flag does), and reaches the
// options the verbs build.
func TestMqttBodyCredentialFlagIsOnBothVerbsAndOffByDefault(t *testing.T) {
	for name, f := range map[string]string{
		"upgrade":   upgradeCmd.Flags().Lookup("mqtt-body-credential").DefValue,
		"bootstrap": bootstrapCmd.Flags().Lookup("mqtt-body-credential").DefValue,
	} {
		if f != "" {
			t.Errorf("dcctl %s --mqtt-body-credential defaults to %q; unset must mean 'not asked'", name, f)
		}
	}

	// Both verbs hand on the PARSED flag, not merely some value under that key: a literal
	// there would leave the key in place and the flag inert.
	for _, src := range []string{"upgrade.go", "bootstrap.go"} {
		file, err := parser.ParseFile(token.NewFileSet(), src, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatal(err)
		}
		threaded := false
		ast.Inspect(file, func(n ast.Node) bool {
			kv, ok := n.(*ast.KeyValueExpr)
			if ok {
				k, kok := kv.Key.(*ast.Ident)
				v, vok := kv.Value.(*ast.Ident)
				if kok && vok && k.Name == "MqttBodyCredential" && v.Name == "mqttBodyCredential" {
					threaded = true
				}
			}
			return true
		})
		if !threaded {
			t.Errorf("%s does not pass the parsed --mqtt-body-credential to its options", src)
		}
	}
}

// --values is on both verbs, repeatable, and --reset-values is on upgrade only.
func TestValuesFlagsAreOnTheVerbsThatTakeThem(t *testing.T) {
	for name, cmd := range map[string]*cobra.Command{"upgrade": upgradeCmd, "bootstrap": bootstrapCmd} {
		f := cmd.Flags().Lookup("values")
		if f == nil {
			t.Fatalf("dcctl %s has no --values", name)
		}
		if f.Value.Type() != "stringArray" {
			t.Errorf("dcctl %s --values is %s; it must be repeatable without splitting a path on commas", name, f.Value.Type())
		}
	}
	if upgradeCmd.Flags().Lookup("reset-values") == nil {
		t.Error("dcctl upgrade has no --reset-values")
	}
	if bootstrapCmd.Flags().Lookup("reset-values") != nil {
		t.Error("dcctl bootstrap has --reset-values: a first bootstrap has nothing recorded to reset")
	}
}

// A refused overlay fails in the command, with the key named, before any cluster is
// contacted: the provider is a valid name, so the only thing that can stop this run is
// the overlay -- a cluster call would fail differently.
func TestARefusedValuesFileStopsUpgradeBeforeAnyCluster(t *testing.T) {
	f := filepath.Join(t.TempDir(), "bad.yaml")
	if err := os.WriteFile(f, []byte("image:\n  tag: v9\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	upgradeValues = []string{f}
	t.Cleanup(func() { upgradeValues = nil })
	err := upgradeCmd.RunE(upgradeCmd, []string{"local", "prod"})
	if err == nil || !strings.Contains(err.Error(), "image.tag") {
		t.Fatalf("a values file setting image.tag was not refused by name: %v", err)
	}

	upgradeValues = []string{f}
	upgradeResetValues = true
	t.Cleanup(func() { upgradeResetValues = false })
	if err := upgradeCmd.RunE(upgradeCmd, []string{"local", "prod"}); err == nil ||
		!strings.Contains(err.Error(), "give one") {
		t.Errorf("--values with --reset-values should be refused as contradictory: %v", err)
	}
}
