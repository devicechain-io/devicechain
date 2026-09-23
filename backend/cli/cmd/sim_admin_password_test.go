// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// The superuser password a sim command signs in with: the flag, else the environment,
// else the instance's Secret — and never a default. Driven through a command carrying
// the REAL persistent flags simCmd registers, so a flag that lost its registration or
// regained a default fails here.
func TestTheSimAdminPasswordIsResolvedInOrderAndHasNoDefault(t *testing.T) {
	if def := simCmd.PersistentFlags().Lookup("admin-password").DefValue; def != "" {
		t.Fatalf("--admin-password defaults to a value again (%d characters); every instance has its own", len(def))
	}

	newCmd := func(t *testing.T, argv ...string) *cobra.Command {
		t.Helper()
		c := &cobra.Command{Use: "probe", RunE: func(*cobra.Command, []string) error { return nil }}
		// Copied, not shared: AddFlagSet shares each *Flag, so a value parsed in one case
		// would stay set on simCmd itself and leak into the next.
		simCmd.PersistentFlags().VisitAll(func(f *pflag.Flag) {
			if f.Value.Type() == "string" {
				c.Flags().String(f.Name, f.DefValue, f.Usage)
			}
		})
		if err := c.Flags().Parse(argv); err != nil {
			t.Fatal(err)
		}
		c.SetContext(context.Background())
		return c
	}
	var asked []string
	stub := func(result string, err error) {
		orig := readSuperuserPassword
		t.Cleanup(func() { readSuperuserPassword = orig })
		readSuperuserPassword = func(_ context.Context, kubeContext, instance string) (string, error) {
			asked = append(asked, kubeContext+"|"+instance)
			return result, err
		}
	}

	t.Run("the flag wins and the cluster is not asked", func(t *testing.T) {
		asked = nil
		stub("from-the-secret", nil)
		t.Setenv(adminPasswordEnv, "from-the-env")
		got, err := resolveAdminPassword(newCmd(t, "--admin-password", "from-the-flag"), "acme")
		if err != nil || got != "from-the-flag" || len(asked) != 0 {
			t.Errorf("got %q, %v, cluster asked %v", got, err, asked)
		}
	})
	t.Run("then the environment", func(t *testing.T) {
		asked = nil
		stub("from-the-secret", nil)
		t.Setenv(adminPasswordEnv, "from-the-env")
		got, err := resolveAdminPassword(newCmd(t), "acme")
		if err != nil || got != "from-the-env" || len(asked) != 0 {
			t.Errorf("got %q, %v, cluster asked %v", got, err, asked)
		}
	})
	t.Run("then the instance's Secret, on the named context", func(t *testing.T) {
		asked = nil
		stub("from-the-secret", nil)
		t.Setenv(adminPasswordEnv, "")
		got, err := resolveAdminPassword(newCmd(t, "--kube-context", "kind-x"), "acme")
		if err != nil || got != "from-the-secret" {
			t.Errorf("got %q, %v", got, err)
		}
		if len(asked) != 1 || asked[0] != "kind-x|acme" {
			t.Errorf("the cluster was asked %v, want once for instance acme on kind-x", asked)
		}
	})
	t.Run("and a failed read names both ways to supply it", func(t *testing.T) {
		stub("", errors.New("no Secret dci-acme/dci-acme-superuser"))
		t.Setenv(adminPasswordEnv, "")
		got, err := resolveAdminPassword(newCmd(t), "acme")
		if err == nil || got != "" {
			t.Fatalf("a failed read returned %q, %v", got, err)
		}
		for _, want := range []string{"--admin-password", adminPasswordEnv, "dci-acme-superuser"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("the error does not mention %q: %v", want, err)
			}
		}
	})
}
