// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package installchoice

import (
	"strings"
	"testing"
)

// --expose-lan reaches the cluster step, is reproduced in the equivalent command, and is
// refused where it could do nothing (it widens only a kind cluster dcctl creates).
func TestExposeLANReachesTheClusterStepOrIsRefused(t *testing.T) {
	for _, on := range []bool{false, true} {
		c := Defaults()
		c.Build = true
		c.Given["build"] = true
		c.ExposeLAN, c.Given["expose-lan"] = on, on
		p, err := c.Resolve("dev")
		if err != nil {
			t.Fatal(err)
		}
		if p.Options.ExposeLAN != on {
			t.Fatalf("--expose-lan=%v arrived as %v", on, p.Options.ExposeLAN)
		}
	}
	for name, mutate := range map[string]func(*Choices){
		"an existing cluster": func(c *Choices) { c.KubeContext, c.Given["kube-context"] = "prod", true },
		"another provider":    func(c *Choices) { c.Provider = "gke" },
	} {
		c := Defaults()
		c.Build = true
		c.Given["build"] = true
		c.ExposeLAN, c.Given["expose-lan"] = true, true
		mutate(&c)
		if _, err := c.Resolve("dev"); err == nil || !strings.Contains(err.Error(), "--expose-lan") {
			t.Errorf("%s: --expose-lan was not refused: %v", name, err)
		}
	}

}
