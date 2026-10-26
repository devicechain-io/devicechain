// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"strings"
	"testing"

	"github.com/devicechain-io/dcctl/bootstrap"
)

// The three front-end flags are bound to the variables the option builders read, and the option
// builders carry them into the options the engine is handed. Driven through the cobra flag set
// itself, so a flag registered on the wrong variable, or a field dropped from the literal, fails.
func TestTheFrontEndFlagsReachTheOptions(t *testing.T) {
	t.Cleanup(func() {
		upgradeIngressController, upgradeDropLegacyIngress, installRemoveLegacy = "", false, false
	})
	if err := upgradeCmd.Flags().Set("ingress-controller", "nginx"); err != nil {
		t.Fatal(err)
	}
	if err := upgradeCmd.Flags().Set("drop-legacy-ingress", "true"); err != nil {
		t.Fatal(err)
	}
	up := upgradeOptions("prod", nil, nil)
	if up.IngressController != "nginx" || !up.DropLegacyIngress {
		t.Errorf("upgrade options = %q drop=%v", up.IngressController, up.DropLegacyIngress)
	}
	if err := installCmd.Flags().Set("remove-legacy-ingress", "true"); err != nil {
		t.Fatal(err)
	}
	in, err := installOptions(nil, bootstrap.RestorePlan{}, bootstrap.ImageSource{})
	if err != nil {
		t.Fatal(err)
	}
	if !in.RemoveLegacyIngress {
		t.Error("--remove-legacy-ingress did not reach InstallOptions")
	}
	// And off by default, in all three.
	upgradeIngressController, upgradeDropLegacyIngress, installRemoveLegacy = "", false, false
	if up := upgradeOptions("prod", nil, nil); up.IngressController != "" || up.DropLegacyIngress {
		t.Errorf("the front-end flags default to %q/%v", up.IngressController, up.DropLegacyIngress)
	}
	if in, _ := installOptions(nil, bootstrap.RestorePlan{}, bootstrap.ImageSource{}); in.RemoveLegacyIngress {
		t.Error("--remove-legacy-ingress is on by default")
	}
}

func TestTheFrontEndFlagUsageSaysItIsRecorded(t *testing.T) {
	f := upgradeCmd.Flags().Lookup("ingress-controller")
	if f == nil || !strings.Contains(f.Usage, "RECORDED") || !strings.Contains(f.Usage, "auto") {
		t.Errorf("--ingress-controller help does not say the choice is recorded and how to clear it: %v", f)
	}
}
