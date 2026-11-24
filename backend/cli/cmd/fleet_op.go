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
)

var bulkChangeCmd, bulkChangeReadinessCmd = newBulkChangeCommand("bulk-change")
var fleetOpCmd, fleetOpReadinessCmd = newBulkChangeCommand("fleet-op")

func newBulkChangeCommand(name string) (*cobra.Command, *cobra.Command) {
	parent := &cobra.Command{
		Use:   name,
		Short: "Preview. Inspect bulk change capabilities",
		Long:  "Preview. Inspect this instance's bulk change capabilities using your tenant identity.\n\nIn v1.10, device fleet operations are called bulk changes. The deprecated dcctl fleet-op readiness alias keeps working and prints a one-line deprecation notice to stderr.",
	}
	readiness := &cobra.Command{
		Use:   "readiness",
		Short: "Preview. Report whether this instance can run bulk changes",
		Long: `Preview. Report the writer protocol version, fresh database enforcement state,
executable operation kinds and orphaned device attribute and generation rows in your tenant.
Requires fleet:operate; scoped members are refused.

Exit status is zero only when enforcement is ENFORCED and orphanedAttributeRows is zero; that
says the database enforces the writer protocol, not that any operation kind is executable. If the
orphan count cannot be read the command reports the error and exits non-zero. The query is an
expensive diagnostic (it counts rows); run it when you need the answer, do not poll it.
In v1.9 enforcement is not installed and no kind is executable. The non-zero exit is
expected until the release that installs enforcement. Use -o json for the four facts as JSON.`,
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE:         runFleetOpReadiness,
	}
	if name == "fleet-op" {
		parent.Deprecated = "Use dcctl bulk-change."
	}
	flags := parent.PersistentFlags()
	flags.StringP("tenant", "t", "", "tenant to inspect (required)")
	flags.StringP("server", "s", "localhost", "instance host for platform API calls")
	addInstanceTargetFlags(parent)
	flags.Bool("tls", false, "use https for platform endpoints")
	flags.String("email", "", "identity to authenticate as; it must be a member of the tenant (required)")
	flags.String("password", "", "password for the identity (required)")
	flags.String("otp", "", otpFlagHelp)
	readiness.Flags().StringP("output", "o", "table", "output format: table or json")
	parent.AddCommand(readiness)
	return parent, readiness
}

// Kept as a client seam so command wiring, output and process status can be tested.
var fleetOpSession = newFleetOpSession

func newFleetOpSession(cmd *cobra.Command) (bulkchange.Client, string, error) {
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
