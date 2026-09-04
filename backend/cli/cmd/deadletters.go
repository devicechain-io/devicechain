// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/devicechain-io/dc-microservice/deadletter"
	"github.com/devicechain-io/dc-microservice/userclient"
	"github.com/devicechain-io/dcctl/deadletters"
	"github.com/devicechain-io/dcctl/sim"
	"github.com/spf13/cobra"
)

var (
	dlEmail    string
	dlPassword string
	dlTenant   string
	dlKind     string
	dlSource   string
	dlSince    string
	dlPageSize int
	dlPages    int
)

var deadLettersCmd = &cobra.Command{
	Use:   "dead-letters",
	Short: "Inspect work the platform accepted and then gave up on",
	Long: `Inspect work the platform accepted and then gave up on.

Four consumers record a dead letter when they have retried a message to its delivery
cap and it still cannot be completed: a detection whose actions could not be
dispatched, an alarm that reached nobody, a device's answer that could not be
recorded against its command, and an alarm edge that could not be applied.

NOTHING REPLAYS THESE. The record exists so a failure is visible and diagnosable
instead of being a log line nobody read; the consequences of the failure itself stand
either way. An alarm that was not raised was not raised.

This reads the operator plane and authenticates as an identity, not as a tenant.`,
}

var deadLettersListCmd = &cobra.Command{
	Use:   "list",
	Short: "List dead letters, newest first",
	Long: `List dead letters, newest first.

Narrow with --tenant, --kind, --source and --since. Reading is bounded by --pages:
when more records exist than were fetched, the output says so rather than letting a
truncated list read as a complete one.`,
	Args:         cobra.NoArgs,
	RunE:         runDeadLettersList,
	SilenceUsage: true,
}

func init() {
	deadLettersCmd.PersistentFlags().StringP("server", "s", "localhost", "instance host for platform API calls")
	deadLettersCmd.PersistentFlags().Bool("tls", false, "use https for platform endpoints")
	deadLettersCmd.PersistentFlags().StringVar(&dlEmail, "email", "", "identity to authenticate as (required)")
	deadLettersCmd.PersistentFlags().StringVar(&dlPassword, "password", "", "password for the identity (required)")

	deadLettersListCmd.Flags().StringVarP(&dlTenant, "tenant", "t", "", "only this tenant's records")
	// 🔑 THE VOCABULARY IS DERIVED, NOT TYPED OUT. This help used to carry its own copy of
	// the kind list, which was already one short the moment a fourth kind was added — and
	// the symptom is an operator being told a real filter value does not exist, on the one
	// surface whose job is to help them find what failed.
	deadLettersListCmd.Flags().StringVar(&dlKind, "kind", "",
		"only this kind: "+strings.Join(deadletter.Kinds(), ", "))
	deadLettersListCmd.Flags().StringVar(&dlSource, "source", "", "only records written by this functional area")
	deadLettersListCmd.Flags().StringVar(&dlSince, "since", "",
		"only records from this RFC3339 time onward (e.g. 2026-09-04T00:00:00Z)")
	deadLettersListCmd.Flags().IntVar(&dlPageSize, "page", 50, "records per server round-trip")
	deadLettersListCmd.Flags().IntVar(&dlPages, "pages", 4, "how many pages to fetch before stopping")

	deadLettersCmd.AddCommand(deadLettersListCmd)
	rootCmd.AddCommand(deadLettersCmd)
}

func runDeadLettersList(cmd *cobra.Command, args []string) error {
	server, _ := cmd.Flags().GetString("server")
	tls, _ := cmd.Flags().GetBool("tls")
	if dlEmail == "" || dlPassword == "" {
		return fmt.Errorf("--email and --password are required")
	}
	if dlPageSize < 1 || dlPages < 1 {
		return fmt.Errorf("--page and --pages must both be at least 1")
	}

	opts := deadletters.Options{
		AdminURL: sim.AdminURL(server, tls),
		Tenant:   dlTenant, Kind: dlKind, Source: dlSource,
		PageSize: dlPageSize, Pages: dlPages,
	}
	if dlSince != "" {
		t, err := time.Parse(time.RFC3339, dlSince)
		if err != nil {
			return fmt.Errorf("--since must be an RFC3339 timestamp (e.g. 2026-09-04T00:00:00Z): %w", err)
		}
		utc := t.UTC()
		opts.Since = &utc
	}

	endpoints := sim.ResolveEndpoints(server, "", "", tls)
	session := userclient.NewAdminSession(nil, endpoints.UserGraphQL, dlEmail, dlPassword)

	// 🔴 NO SUPERUSER PRE-CHECK HERE, and an earlier version had one. The server gates
	// this on audit:read, which is deliberately grantable on a non-superuser system role —
	// "an operator who may read the instance audit journal" — so a superuser check would
	// refuse people the server would authorize, with a message telling them something
	// untrue about why. The server's own refusal is the right one; it is also the only one
	// that stays correct when the authority is regranted.
	ctx := context.Background()
	sum, err := deadletters.Run(ctx, session, opts)
	if err != nil {
		return err
	}
	deadletters.Print(cmd.OutOrStdout(), opts, sum)
	return nil
}
