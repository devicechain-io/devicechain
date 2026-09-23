// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"strings"
	"testing"
)

// The drill has no default superuser password — each instance's is generated at
// bootstrap — so a seed given none refuses as a SETUP failure, naming where the password
// lives, before it sends anything. The server is unreachable on purpose: a refusal that
// came from a failed login instead would mention the login, not the Secret.
func TestASeedWithNoSuperuserPasswordIsRefusedBeforeItSignsIn(t *testing.T) {
	t.Setenv(adminPasswordEnv, "")
	err := runSeed(context.Background(), []string{
		"--instance", "drill", "--receipt", t.TempDir() + "/receipt.json", "--server", "127.0.0.1:1",
	})
	if err == nil {
		t.Fatal("a seed with no superuser password was not refused")
	}
	if got := codeOf(err); got != exitSetup {
		t.Errorf("exit %d, want the setup code %d", got, exitSetup)
	}
	for _, want := range []string{"--admin-password", adminPasswordEnv, "dci-drill/dci-drill-superuser"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not mention %q: %v", want, err)
		}
	}
	if strings.Contains(err.Error(), "admin login") {
		t.Errorf("the seed tried to sign in with an empty password: %v", err)
	}
}
