// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/devicechain-io/dc-microservice/userclient"
	"github.com/devicechain-io/dcctl/importer"
	"github.com/devicechain-io/dcctl/sim"
	"github.com/fatih/color"
	"github.com/spf13/cobra"
)

var importCmd = &cobra.Command{
	Use:   "import <file-or-directory>",
	Short: "Load another platform's JSON entity export (EXPERIMENTAL)",
	Long: `Load the JSON entity exports other IoT platforms produce into a tenant: device profiles,
devices, asset profiles, assets, their relations and their attributes.

EXPERIMENTAL. This is a spike. It converts only what maps cleanly and tells you, line by line,
what it did not. Read the report before you trust the result.

By default nothing is sent: the command reads the export, prints the plan and the report, and
stops. Pass --apply (with --tenant, --email and --password) to write. Applying is create-only and
safe to repeat: anything that already exists is left alone and counted as existing, and a profile
is published only by the run that created it.

The input is one JSON file or a directory of them; a file may hold one record, an array of
records, or {"entities": [...]}. A record is {"entityType", "entity", "relations", "attributes",
"credentials"}. Source ids become tokens, so a re-run matches what the first run made.

Credentials are not carried unless you pass --carry-access-tokens, and then only access tokens.
Devices still need to speak this platform's wire to connect.

Not converted: customers and users, alarm rules (classified in the report), rule chains,
dashboards, entity views, update packages, telemetry history.`,
	Args:         cobra.ExactArgs(1),
	RunE:         runImport,
	SilenceUsage: true,
}

var (
	importTenant   string
	importEmail    string
	importPassword string
)

func init() {
	f := importCmd.Flags()
	f.Bool("apply", false, "write to the tenant (default: print the plan and report only)")
	f.Bool("carry-access-tokens", false, "import each device's access-token credential with the device")
	f.StringP("output", "o", "text", "report format: text or json")
	f.StringVarP(&importTenant, "tenant", "t", "", "tenant to import into (required with --apply)")
	f.StringP("server", "s", "localhost", "instance host for platform API calls")
	f.Bool("tls", false, "use https for platform endpoints")
	f.StringVar(&importEmail, "email", "", "identity to authenticate as (required with --apply)")
	f.StringVar(&importPassword, "password", "", "password for the identity (required with --apply)")
	f.String("otp", "", otpFlagHelp)
	rootCmd.AddCommand(importCmd)
}

func runImport(cmd *cobra.Command, args []string) error {
	asJSON := false
	switch out, _ := cmd.Flags().GetString("output"); out {
	case "text", "":
	case "json":
		asJSON = true
	default:
		return fmt.Errorf("unknown output format %q: expected text or json", out)
	}
	ex, err := importer.Load(args[0])
	if err != nil {
		return err
	}
	carry, _ := cmd.Flags().GetBool("carry-access-tokens")
	plan := importer.Build(ex, importer.Options{CarryAccessTokens: carry})
	apply, _ := cmd.Flags().GetBool("apply")

	if !asJSON {
		importer.PrintReport(cmd.OutOrStdout(), plan)
	}
	if !apply {
		if asJSON {
			return writeJSON(cmd, map[string]any{"applied": false, "plan": plan})
		}
		fmt.Fprintf(cmd.OutOrStdout(), "\n%s nothing was sent. Re-run with --apply to write.\n", color.YellowString("Dry run:"))
		return nil
	}
	if strings.TrimSpace(importTenant) == "" {
		return fmt.Errorf("--tenant is required with --apply: the import is per-tenant, and there is no default")
	}
	if strings.TrimSpace(importEmail) == "" || strings.TrimSpace(importPassword) == "" {
		return fmt.Errorf("--email and --password are required with --apply: the import is authorized as the identity that runs it")
	}
	server, _ := cmd.Flags().GetString("server")
	tls, _ := cmd.Flags().GetBool("tls")
	endpoints := sim.ResolveEndpoints(server, "", "", tls)
	session := userclient.NewTenantSession(nil, endpoints.UserGraphQL, importEmail, importPassword, importTenant).
		WithOTP(otpFunc(otpFlag(cmd)))
	tallies, applyErr := importer.Apply(cmd.Context(), session, endpoints.DeviceMgmtGraphQL, plan)
	if asJSON {
		if err := writeJSON(cmd, map[string]any{"applied": true, "results": tallies, "report": plan.Report}); err != nil {
			return err
		}
		return applyErr
	}
	fmt.Fprintf(cmd.OutOrStdout(), "\n%s\n", color.GreenString("Applied:"))
	for _, t := range tallies {
		fmt.Fprintf(cmd.OutOrStdout(), "  %-20s created %d, existing %d, failed %d\n", t.Kind, t.Created, t.Exists, t.Failed)
		for _, e := range t.Errors {
			fmt.Fprintf(cmd.OutOrStdout(), "      %s\n", e)
		}
	}
	return applyErr
}

func writeJSON(cmd *cobra.Command, v any) error {
	enc := json.NewEncoder(cmd.OutOrStdout())
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}
