// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"errors"
	"os"
	"os/exec"
	"testing"

	"github.com/spf13/cobra"

	"github.com/devicechain-io/dcctl/bootstrap"
	"github.com/devicechain-io/dcctl/internal/secretenv"
)

const (
	secretEnvChildEnv      = "DCCTL_TEST_SECRET_ENV_CHILD"
	secretEnvGrandchildEnv = "DCCTL_TEST_SECRET_ENV_GRANDCHILD"
	secretEnvValue         = "s3cret-passphrase"
)

// 🔴 A PASSPHRASE IN dcctl's ENVIRONMENT IS IN EVERY CHILD'S. It used to be unset only where
// it was read, which is after kind, docker, ko and tofu had been started, and never on a path
// that did not read it (a passphrase file that won, --no-escrow, `dcctl install`). This runs
// dcctl's real entry point in a process of its own, with the passphrase exported the way a CI
// job exports it, and has the command it runs start a child the ordinary way, inheriting the
// environment: the child must not see it, and dcctl must still have it.
func TestExecuteKeepsTheEscrowPassphraseFromEveryChild(t *testing.T) {
	cmd := exec.Command(os.Args[0], "-test.run=^TestSecretEnvChild$", "-test.count=1")
	cmd.Env = append(os.Environ(), secretEnvChildEnv+"=1", bootstrap.EscrowPassphraseEnv+"="+secretEnvValue)
	out, err := cmd.CombinedOutput()
	var exit *exec.ExitError
	switch {
	case err == nil:
	case errors.As(err, &exit) && exit.ExitCode() == 3:
		t.Fatalf("a child dcctl started inherited %s:\n%s", bootstrap.EscrowPassphraseEnv, out)
	case errors.As(err, &exit) && exit.ExitCode() == 4:
		t.Fatalf("dcctl lost the passphrase it was given:\n%s", out)
	default:
		t.Fatalf("the child did not run to a verdict: %v\n%s", err, out)
	}
}

// TestSecretEnvChild is dcctl, run by the test above. Not a skip outside it: CI refuses
// unallowed skips, and there is nothing to do.
func TestSecretEnvChild(t *testing.T) {
	if os.Getenv(secretEnvGrandchildEnv) != "" {
		if _, ok := os.LookupEnv(bootstrap.EscrowPassphraseEnv); ok {
			os.Exit(3)
		}
		os.Exit(0)
	}
	if os.Getenv(secretEnvChildEnv) == "" {
		return
	}
	probe := &cobra.Command{Use: "secret-env-probe", Hidden: true, RunE: func(*cobra.Command, []string) error {
		child := exec.Command(os.Args[0], "-test.run=^TestSecretEnvChild$", "-test.count=1")
		child.Env = append(os.Environ(), secretEnvGrandchildEnv+"=1")
		if err := child.Run(); err != nil {
			var exit *exec.ExitError
			if errors.As(err, &exit) {
				os.Exit(exit.ExitCode())
			}
			os.Exit(5)
		}
		// And the plain form, with no Env set: exactly what kind and docker are started with.
		bare := exec.Command(os.Args[0], "-test.run=^TestSecretEnvChild$", "-test.count=1")
		os.Setenv(secretEnvGrandchildEnv, "1")
		if err := bare.Run(); err != nil {
			var exit *exec.ExitError
			if errors.As(err, &exit) {
				os.Exit(exit.ExitCode())
			}
			os.Exit(5)
		}
		if v, ok := secretenv.LookupEscrowPassphrase(); !ok || v != secretEnvValue {
			os.Exit(4)
		}
		os.Exit(0)
		return nil
	}}
	rootCmd.AddCommand(probe)
	rootCmd.SetArgs([]string{"secret-env-probe"})
	Execute()
	os.Exit(6)
}
