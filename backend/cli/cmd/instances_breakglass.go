// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/devicechain-io/dcctl/bootstrap"
	"github.com/fatih/color"
	"github.com/spf13/cobra"
)

var (
	breakGlassInstance      string
	breakGlassEmail         string
	breakGlassKubeContext   string
	breakGlassPasswordStdin bool
	breakGlassAssumeYes     bool
)

var instancesBreakGlassCmd = &cobra.Command{
	Use:   "break-glass",
	Short: "Recover an operator account without signing in, with cluster credentials",
}

var instancesBreakGlassMfaResetCmd = &cobra.Command{
	Use:   "mfa-reset [email]",
	Short: "Remove an operator's second factor and replace its password, from inside a pod",
	Long: `Removes the two-factor authentication from an identity that holds a system role, replaces
its password, and ends every session it has. For the operator who cannot sign in: an enrolled
superuser who lost the phone and the recovery codes has no one the admin API could reset them.

It does not talk to the admin API and needs no sign-in. It runs the service's own binary inside a
ready user-management pod of the instance (` + "`user-management break-glass mfa-reset`" + `), so it needs the
cluster credentials to exec into that pod, and nothing else. The reset is the store code the service
itself runs; this command carries no SQL.

The password is ALWAYS replaced, because this route has no caller to vouch for: removing the factor
alone would hand the account to whoever holds the old password. By default a new password is
generated in the pod and printed ONCE, here. With --password-stdin the first line of stdin is the
password instead.

It refuses an identity that holds no system role. Reset anyone else through the admin API
(resetIdentityMfa), where the log names the operator who did it. The reset is recorded as mfa_reset
by "break-glass:pod-exec".

dcctl sim commands read the seed superuser's password from the instance Secret; if you reset that
account, pass the new password with --admin-password.`,
	Args:         cobra.MaximumNArgs(1),
	RunE:         runBreakGlassMfaReset,
	SilenceUsage: true,
}

func init() {
	instancesBreakGlassMfaResetCmd.Flags().StringVar(&breakGlassInstance, "instance", "", "instance whose pod to run in (required)")
	instancesBreakGlassMfaResetCmd.Flags().StringVar(&breakGlassEmail, "email", "", "the operator identity's address (or give it as the argument)")
	instancesBreakGlassMfaResetCmd.Flags().StringVar(&breakGlassKubeContext, "kube-context", "",
		"kube context of the cluster the instance runs in (default: the one recorded for the instance, else the current one)")
	instancesBreakGlassMfaResetCmd.Flags().BoolVar(&breakGlassPasswordStdin, "password-stdin", false,
		"read the new password as one line from stdin instead of generating one")
	instancesBreakGlassMfaResetCmd.Flags().BoolVarP(&breakGlassAssumeYes, "yes", "y", false, "skip the confirmation prompt")
	instancesBreakGlassCmd.AddCommand(instancesBreakGlassMfaResetCmd)
	instancesCmd.AddCommand(instancesBreakGlassCmd)
}

// breakGlassReset is the cluster call, indirected so the command's decisions can be tested
// without a cluster.
var breakGlassReset = bootstrap.BreakGlassMfaReset

func runBreakGlassMfaReset(cmd *cobra.Command, args []string) error {
	email := strings.TrimSpace(breakGlassEmail)
	if len(args) == 1 {
		if email != "" && email != strings.TrimSpace(args[0]) {
			return errors.New("the address was given twice and they differ")
		}
		email = strings.TrimSpace(args[0])
	}
	return doBreakGlassMfaReset(cmd.Context(), cmd.OutOrStdout(), cmd.ErrOrStderr(), os.Stdin,
		breakGlassInstance, email, breakGlassKubeContext, breakGlassPasswordStdin, breakGlassAssumeYes)
}

func doBreakGlassMfaReset(ctx context.Context, out, errOut io.Writer, in io.Reader,
	instance, email, kubeContext string, passwordStdin, assumeYes bool) error {
	if instance == "" {
		return errors.New("--instance is required")
	}
	if email == "" {
		return errors.New("the operator's email address is required (--email or the argument)")
	}
	if err := bootstrap.ValidateInstanceName(instance); err != nil {
		return err
	}
	if kubeContext == "" {
		if rec, err := bootstrap.ReadInstanceRecord(instance); err == nil && rec.KubeContext != "" {
			kubeContext = rec.KubeContext
		}
	}

	if !assumeYes {
		// The prompt reads the same stdin a password would come from, so the two cannot be combined:
		// --yes is how a script says it means it.
		if passwordStdin {
			return errors.New("--password-stdin reads stdin, so there is nowhere to ask for confirmation; add --yes")
		}
		if !stdinIsTerminal() {
			return errors.New("this removes an account's two-factor authentication and there is no terminal to " +
				"confirm on; pass --yes to proceed non-interactively")
		}
		fmt.Fprintln(out, color.HiYellowString("This removes %s's two-factor authentication, REPLACES its password and ends every session it has.", email))
		fmt.Fprintf(out, "It runs inside a user-management pod of instance %q, authorized by your cluster credentials.\n", instance)
		fmt.Fprintf(out, "\nType the instance name to confirm:\n> ")
		answer, err := bufio.NewReader(in).ReadString('\n')
		if err != nil && answer == "" {
			return fmt.Errorf("reading confirmation: %w", err)
		}
		if strings.TrimSpace(answer) != instance {
			return fmt.Errorf("that does not match %q; nothing was changed", instance)
		}
	}

	var stdin io.Reader
	if passwordStdin {
		stdin = in
	}
	if err := breakGlassReset(ctx, kubeContext, instance, email, stdin, out, errOut); err != nil {
		return err
	}
	fmt.Fprintln(out, color.HiGreenString("done"))
	return nil
}
