// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"context"
	"fmt"
	"time"

	"github.com/devicechain-io/dc-microservice/userclient"
	"github.com/devicechain-io/dcctl/deletions"
	"github.com/devicechain-io/dcctl/sim"
	"github.com/spf13/cobra"
)

var (
	tdEmail    string
	tdPassword string
	tdWait     bool
	tdUntil    string
	tdTimeout  time.Duration
	tdEvery    time.Duration
)

var tenantDeletionCmd = &cobra.Command{
	Use:   "tenant-deletion",
	Short: "Check whether a deleted tenant's data is established as erased",
}

var tenantDeletionStatusCmd = &cobra.Command{
	Use:   "status <token>",
	Short: "Report a tenant deletion's outcome; exit non-zero unless the data is erased",
	Long: `Report the outcome of a tenant deletion, and exit non-zero unless the tenant's data is
established as erased.

"Erased" is the platform's own claim, read from the deletion record: every storage system has
reported clean and has stayed clean for the settle window. It is not inferred here from an empty
list of blockers. It comes before the deletion completes, because the tenant's token is then held
for a further period (12 hours by default) before it can be reused. Use --until complete to wait
for that release instead.

With --wait the command polls until the target is reached, and exits non-zero if --timeout passes
first. A tenant with no deletion record is an error, never a success.

This reads the operator plane and authenticates as an identity, not as a tenant.`,
	Args:         cobra.ExactArgs(1),
	RunE:         runTenantDeletionStatus,
	SilenceUsage: true,
}

func init() {
	tenantDeletionCmd.PersistentFlags().StringP("server", "s", "localhost", "instance host for platform API calls")
	tenantDeletionCmd.PersistentFlags().Bool("tls", false, "use https for platform endpoints")
	tenantDeletionCmd.PersistentFlags().StringVar(&tdEmail, "email", "", "identity to authenticate as (required)")
	tenantDeletionCmd.PersistentFlags().StringVar(&tdPassword, "password", "", "password for the identity (required)")
	tenantDeletionCmd.PersistentFlags().String("otp", "", otpFlagHelp)

	tenantDeletionStatusCmd.Flags().BoolVar(&tdWait, "wait", false, "poll until the target is reached")
	tenantDeletionStatusCmd.Flags().StringVar(&tdUntil, "until", string(deletions.TargetErased),
		"what to wait for and require: erased (data established as gone) or complete (token released)")
	tenantDeletionStatusCmd.Flags().DurationVar(&tdTimeout, "timeout", 30*time.Minute, "how long --wait polls before giving up")
	tenantDeletionStatusCmd.Flags().DurationVar(&tdEvery, "interval", 10*time.Second, "how often --wait polls")

	tenantDeletionCmd.AddCommand(tenantDeletionStatusCmd)
	rootCmd.AddCommand(tenantDeletionCmd)
}

func runTenantDeletionStatus(cmd *cobra.Command, args []string) error {
	server, _ := cmd.Flags().GetString("server")
	tls, _ := cmd.Flags().GetBool("tls")
	if tdEmail == "" || tdPassword == "" {
		return fmt.Errorf("--email and --password are required")
	}
	target := deletions.Target(tdUntil)
	if target != deletions.TargetErased && target != deletions.TargetComplete {
		return fmt.Errorf("--until must be %q or %q", deletions.TargetErased, deletions.TargetComplete)
	}
	if tdEvery <= 0 || tdTimeout <= 0 {
		return fmt.Errorf("--interval and --timeout must be positive")
	}
	adminURL := sim.AdminURL(server, tls)
	endpoints := sim.ResolveEndpoints(server, "", "", tls)
	session := userclient.NewAdminSession(nil, endpoints.UserGraphQL, tdEmail, tdPassword).WithOTP(otpFunc(otpFlag(cmd)))
	return tenantDeletionStatus(cmd.Context(), cmd, session, adminURL, args[0], target)
}

// tenantDeletionStatus is the command body over an injectable client.
func tenantDeletionStatus(ctx context.Context, cmd *cobra.Command, c deletions.Client,
	adminURL, token string, target deletions.Target) error {
	if ctx == nil {
		ctx = context.Background()
	}
	out := cmd.OutOrStdout()
	var d *deletions.Deletion
	var err error
	if tdWait {
		wctx, cancel := context.WithTimeout(ctx, tdTimeout)
		defer cancel()
		d, err = deletions.Wait(wctx, c, adminURL, token, target, tdEvery, func(p *deletions.Deletion) {
			fmt.Fprintf(out, "waiting: %s\n", p.Awaiting)
		})
	} else {
		d, err = deletions.Find(ctx, c, adminURL, token)
		if err == nil && d == nil {
			err = fmt.Errorf("no deletion record for tenant %q: it was never deleted, or the token is not spelled as stored", token)
		}
	}
	if d != nil {
		deletions.Print(out, d)
	}
	if err != nil {
		return err
	}
	if !deletions.Reached(d, target) {
		return fmt.Errorf("tenant %q is not %s", token, target)
	}
	return nil
}
