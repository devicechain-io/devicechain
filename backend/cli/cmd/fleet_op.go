// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"fmt"
	"strings"

	"github.com/devicechain-io/dc-microservice/userclient"
	"github.com/devicechain-io/dcctl/bulkchange"
	"github.com/devicechain-io/dcctl/sim"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

var bulkChangeCmd, bulkChangeReadinessCmd = newBulkChangeCommand("bulk-change")
var fleetOpCmd, fleetOpReadinessCmd = newBulkChangeCommand("fleet-op")

func newBulkChangeCommand(name string) (*cobra.Command, *cobra.Command) {
	parent := &cobra.Command{
		Use:   name,
		Short: "Preview. Plan, run and inspect device bulk changes",
		Long:  "Preview. Plan, run and inspect device bulk changes using your tenant identity.\n\nThe commands probe this instance for support. The deprecated dcctl fleet-op readiness alias keeps working and prints a one-line deprecation notice to stderr.",
	}
	readiness := &cobra.Command{
		Use:   "readiness",
		Short: "Preview. Report whether this instance can run bulk changes",
		Long: `Preview. Report the writer protocol version, fresh database enforcement state,
executable operation kinds and orphaned device attribute and generation rows in your tenant.
Requires fleet:operate; scoped members are refused.

Exit status is zero only when enforcement is ENFORCED and orphanedAttributeRows is zero; that
says the database enforces the writer protocol.
From v1.10, an upgraded instance with enforcement ENFORCED and zero orphaned rows lists
config.set as executable. In v1.9 enforcement is not installed and no kind is executable, so
the command exits non-zero.
If the orphan count cannot be read the command reports the error and exits non-zero. The query
is an expensive diagnostic (it counts rows); run it when you need the answer, do not poll it.
Use -o json for the four facts as JSON.`,
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE:         runFleetOpReadiness,
	}
	if name == "fleet-op" {
		parent.Deprecated = "Use dcctl bulk-change."
		parent.Short = "Preview. Inspect bulk change capabilities"
		parent.Long = "Preview. Inspect bulk change capabilities. This deprecated alias keeps readiness alone; use dcctl bulk-change for new commands."
	}
	flags := parent.PersistentFlags()
	flags.StringP("tenant", "t", "", "tenant to inspect (required)")
	flags.StringP("server", "s", "localhost", "instance host for platform API calls")
	addInstanceTargetFlags(parent)
	flags.Bool("tls", false, "use https for platform endpoints")
	flags.String("email", "", "identity to authenticate as; it must be a member of the tenant (required)")
	flags.String("password", "", "password for the identity (required)")
	flags.String("otp", "", otpFlagHelp)
	flags.BoolP("help", "h", false, "help for bulk changes")
	flags.VisitAll(func(f *pflag.Flag) { f.Usage = "Preview. " + f.Usage })
	readiness.Flags().StringP("output", "o", "table", "Preview. Output format: table or json")
	parent.AddCommand(readiness)
	if name == "bulk-change" {
		addBulkChangeCommands(parent)
	}
	return parent, readiness
}

// Kept as a client seam so command wiring, output and process status can be tested.
var fleetOpSession = newFleetOpSession

func newFleetOpSession(cmd *cobra.Command) (bulkchange.Client, string, error) {
	sessions, endpoint, err := tenantSessions(cmd)
	if err != nil {
		return nil, "", err
	}
	return sessions(otpFunc(otpFlag(cmd))), endpoint, nil
}

// tenantSessions reads the command's identity and target once and answers a builder of
// not-yet-signed-in sessions for them, each answering a second-factor challenge with the otp it
// is given. Sessions that must never offer the same --otp twice share one otp.
func tenantSessions(cmd *cobra.Command) (func(userclient.OTPFunc) *userclient.TenantSession, string, error) {
	server, tls, err := targetFlags(cmd)
	if err != nil {
		return nil, "", err
	}
	tenant, _ := cmd.Flags().GetString("tenant")
	email, _ := cmd.Flags().GetString("email")
	password, _ := cmd.Flags().GetString("password")
	if strings.TrimSpace(tenant) == "" {
		return nil, "", fmt.Errorf("--tenant is required: bulk changes are per-tenant, and there is no default")
	}
	if strings.TrimSpace(email) == "" || strings.TrimSpace(password) == "" {
		return nil, "", fmt.Errorf("--email and --password are required: bulk changes are authorized as the identity that requests them")
	}
	endpoints := sim.ResolveEndpoints(server, "", "", tls)
	return func(otp userclient.OTPFunc) *userclient.TenantSession {
		return userclient.NewTenantSession(nil, endpoints.UserGraphQL, email, password, tenant).WithOTP(otp)
	}, endpoints.DeviceMgmtGraphQL, nil
}

func runFleetOpReadiness(cmd *cobra.Command, _ []string) error {
	legacy := cmd.Parent() != nil && cmd.Parent().Name() == "fleet-op"
	if legacy {
		fmt.Fprintln(cmd.ErrOrStderr(), "dcctl fleet-op is deprecated; use dcctl bulk-change.")
	}
	format, _ := cmd.Flags().GetString("output")
	if format != "table" && format != "json" {
		return fmt.Errorf("unknown output format %q: expected table or json", format)
	}
	client, endpoint, err := fleetOpSession(cmd)
	if err != nil {
		return err
	}
	if legacy {
		return bulkchange.RunLegacyReadiness(cmd.Context(), client, endpoint, cmd.OutOrStdout(), format == "json")
	}
	return bulkchange.RunReadiness(cmd.Context(), client, endpoint, cmd.OutOrStdout(), format == "json")
}

func init() {
	rootCmd.AddCommand(bulkChangeCmd, fleetOpCmd)
}
