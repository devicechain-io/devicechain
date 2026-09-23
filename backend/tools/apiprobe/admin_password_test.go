// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"flag"
	"strings"
	"testing"
)

// The probe signs in as the superuser with the password it is GIVEN — the flag, else
// the environment — and refuses an empty one rather than sending it. There is no
// default: it used to be the literal every instance was seeded with, and each instance
// now has a generated password of its own.
func TestTheProbeAdminPasswordHasNoDefault(t *testing.T) {
	parse := func(t *testing.T, argv ...string) *connection {
		t.Helper()
		var c connection
		fs := flag.NewFlagSet("seed", flag.ContinueOnError)
		c.bind(fs)
		if err := fs.Parse(argv); err != nil {
			t.Fatal(err)
		}
		return &c
	}

	t.Setenv(adminPasswordEnv, "")
	if _, err := parse(t).adminPassword(); err == nil ||
		!strings.Contains(err.Error(), adminPasswordEnv) || !strings.Contains(err.Error(), "--admin-password") {
		t.Errorf("an empty password was not refused, naming both ways to supply one: %v", err)
	}

	t.Setenv(adminPasswordEnv, "from-the-env")
	if got, err := parse(t).adminPassword(); err != nil || got != "from-the-env" {
		t.Errorf("the environment was not used: %q, %v", got, err)
	}
	if got, err := parse(t, "--admin-password", "from-the-flag").adminPassword(); err != nil || got != "from-the-flag" {
		t.Errorf("the flag did not win over the environment: %q, %v", got, err)
	}
}
