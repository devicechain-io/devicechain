// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"fmt"
	"strings"

	"github.com/devicechain-io/dc-microservice/userclient"
	"github.com/devicechain-io/dcctl/fleetop"
	"github.com/devicechain-io/dcctl/sim"
	"github.com/spf13/cobra"
)

var fleetOpCmd = &cobra.Command{
	Use:   "fleet-op",
	Short: "Preview. Inspect fleet operation capabilities",
	Long:  "Preview. Inspect this instance's fleet operation capabilities using your tenant identity.",
}

var fleetOpReadinessCmd = &cobra.Command{
	Use:   "readiness",
	Short: "Preview. Report whether this instance can run fleet operations",
	Long: `Preview. Report the writer protocol version, fresh database enforcement state,
executable operation kinds and orphaned device attribute and generation rows in your tenant.
Requires fleet:operate; scoped members are refused.

Exit status is zero only when enforcement is ENFORCED and orphanedAttributeRows is zero.
In v1.9 enforcement is not installed and no kind is executable. The non-zero exit is
expected until the release that installs enforcement. Use -o json for the four facts as JSON.`,
	Args:         cobra.NoArgs,
	SilenceUsage: true,
	RunE:         runFleetOpReadiness,
}

// Kept as a client seam so command wiring, output and process status can be tested.
var fleetOpSession = newFleetOpSession

func newFleetOpSession(cmd *cobra.Command) (fleetop.Client, string, error) {
	server, tls, err := targetFlags(cmd)
	if err != nil {
		return nil, "", err
	}
	tenant, _ := cmd.Flags().GetString("tenant")
	email, _ := cmd.Flags().GetString("email")
	password, _ := cmd.Flags().GetString("password")
	if strings.TrimSpace(tenant) == "" {
		return nil, "", fmt.Errorf("--tenant is required: readiness is per-tenant, and there is no default")
	}
	if strings.TrimSpace(email) == "" || strings.TrimSpace(password) == "" {
		return nil, "", fmt.Errorf("--email and --password are required: readiness is authorized as the identity that requests it")
	}
	endpoints := sim.ResolveEndpoints(server, "", "", tls)
	session := userclient.NewTenantSession(nil, endpoints.UserGraphQL, email, password, tenant).
		WithOTP(otpFunc(otpFlag(cmd)))
	return session, endpoints.DeviceMgmtGraphQL, nil
}

func runFleetOpReadiness(cmd *cobra.Command, _ []string) error {
	format, _ := cmd.Flags().GetString("output")
	if format != "table" && format != "json" {
		return fmt.Errorf("unknown output format %q: expected table or json", format)
	}
	client, endpoint, err := fleetOpSession(cmd)
	if err != nil {
		return err
	}
	return fleetop.RunReadiness(cmd.Context(), client, endpoint, cmd.OutOrStdout(), format == "json")
}

func init() {
	flags := fleetOpCmd.PersistentFlags()
	flags.StringP("tenant", "t", "", "tenant to inspect (required)")
	flags.StringP("server", "s", "localhost", "instance host for platform API calls")
	addInstanceTargetFlags(fleetOpCmd)
	flags.Bool("tls", false, "use https for platform endpoints")
	flags.String("email", "", "identity to authenticate as; it must be a member of the tenant (required)")
	flags.String("password", "", "password for the identity (required)")
	flags.String("otp", "", otpFlagHelp)
	fleetOpReadinessCmd.Flags().StringP("output", "o", "table", "output format: table or json")
	fleetOpCmd.AddCommand(fleetOpReadinessCmd)
	rootCmd.AddCommand(fleetOpCmd)
}
