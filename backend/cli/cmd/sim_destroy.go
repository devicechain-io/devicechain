// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"fmt"

	"github.com/devicechain-io/dcctl/sim"
	"github.com/spf13/cobra"
)

var simDestroyPurge bool

var simDestroyCmd = &cobra.Command{
	Use:   "destroy <name>",
	Short: "Delete a sim's scoped identity (and, with --purge, its tenant)",
	Long: `Tear a sim back down. By default this removes the scoped identity (and its
membership) but KEEPS the tenant and its data for inspection — deleting the sim is
distinct from deleting its tenant (ADR-035). --purge also deletes the tenant and all
its data.

Teardown order is enforced: the identity (and its membership) is deleted first, so
the tenant delete is not rejected for still having memberships.`,
	Args:         cobra.ExactArgs(1),
	RunE:         runSimDestroy,
	SilenceUsage: true,
}

func init() {
	simDestroyCmd.Flags().BoolVar(&simDestroyPurge, "purge", false, "also delete the tenant and ALL its data")
	simCmd.AddCommand(simDestroyCmd)
}

func runSimDestroy(cmd *cobra.Command, args []string) error {
	name := args[0]
	rec, err := sim.Load(name)
	if err != nil {
		return err
	}

	adminEmail, _ := cmd.Flags().GetString("admin-email")
	adminPassword, _ := cmd.Flags().GetString("admin-password")

	// Tear down against the SAME host the sim was created on: both the login base
	// and the admin URL come from the record, so destroy is correct even when
	// --server is not repeated. Fall back to a flag-derived admin URL for records
	// written before adminURL was persisted.
	adminURL := rec.AdminURL
	if adminURL == "" {
		server, _ := cmd.Flags().GetString("server")
		tls, _ := cmd.Flags().GetBool("tls")
		adminURL = sim.AdminURL(server, tls)
	}
	admin := sim.NewAdmin(rec.Endpoints.UserGraphQL, adminURL, adminEmail, adminPassword)
	ctx := cmd.Context()
	if err := admin.EnsureSuperuser(ctx); err != nil {
		return err
	}

	// deleteIdentity removes the identity AND its memberships, so a subsequent
	// deleteTenant is not rejected for still having a membership.
	removed, err := admin.DeleteIdentity(ctx, rec.SimEmail)
	if err != nil {
		return err
	}
	fmt.Printf("identity %q removed: %t\n", rec.SimEmail, removed)

	if simDestroyPurge {
		tRemoved, err := admin.DeleteTenant(ctx, rec.Tenant)
		if err != nil {
			return err
		}
		fmt.Printf("tenant %q removed: %t\n", rec.Tenant, tRemoved)
	} else {
		fmt.Printf("tenant %q kept — use --purge to delete it and its data\n", rec.Tenant)
	}

	if err := sim.Delete(name); err != nil {
		return err
	}
	fmt.Printf("✅ sim %q destroyed\n", name)
	return nil
}
