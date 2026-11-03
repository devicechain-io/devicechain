// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"fmt"
	"strings"
	"time"

	"github.com/devicechain-io/dc-microservice/userclient"
	"github.com/devicechain-io/dcctl/failedevents"
	"github.com/devicechain-io/dcctl/sim"
	"github.com/spf13/cobra"
)

var (
	evTenant   string
	evEmail    string
	evPassword string
	evReason   string
	evDevice   string
	evSince    string
	evUntil    string
	evLimit    int
	evSummary  bool
	evOutput   string
)

// summaryDefaultWindow is how far back --summary looks when --since is not given. The
// summary's since is required by the server, and a day is the question an operator asks.
const summaryDefaultWindow = 24 * time.Hour

// timeNow is the clock the relative --since form reads, indirected for tests.
var timeNow = time.Now

var eventsCmd = &cobra.Command{
	Use:   "events",
	Short: "Inspect a tenant's events",
	Long: `Inspect events a tenant's devices sent to the platform.

These commands read the tenant plane as a member of the tenant, so they show what that
member's console shows, under the same grants.`,
}

var eventsFailedCmd = &cobra.Command{
	Use:   "failed",
	Short: "List the events the platform refused or could not store",
	Long: `List the events the platform refused or could not store for a tenant, newest first:
the answer to "why isn't my device's data showing up".

Each record names the reason, the device token the SENDER claimed (a claim, not proof
the device exists) and the platform's error. Narrow with --reason, --device, --since and
--until. Reading is bounded by --limit: when more records match than were fetched, the
output says so rather than letting a truncated list read as a complete one. The server
reads at most 31 days back; an older --since is treated as 31 days ago. Durations have no
day unit, so 31 days is 744h.

With --summary, print the count of failures per reason since --since (default 24h), with
the devices that account for the most of each. Counts include records the platform folded
into a count rather than itemizing.

This is a report, not a queue: nothing is re-sent or replayed, and the original event is
not fetched. The PAYLOAD column says only whether the platform still holds it.

It needs device:write on the tenant, and is refused for a member whose access is limited
to some customers.`,
	Example: `  dcctl events failed -t acme --email ops@acme.example --password ... --reason device-not-found
  dcctl events failed -t acme --email ops@acme.example --password ... --device sensor-17 --since 6h
  dcctl events failed -t acme --email ops@acme.example --password ... --summary -o json`,
	Args:         cobra.NoArgs,
	RunE:         runEventsFailed,
	SilenceUsage: true,
}

func init() {
	eventsCmd.PersistentFlags().StringP("server", "s", "localhost", "instance host for platform API calls")
	eventsCmd.PersistentFlags().Bool("tls", false, "use https for platform endpoints")
	eventsCmd.PersistentFlags().StringVarP(&evTenant, "tenant", "t", "", "tenant whose events are read (required)")
	eventsCmd.PersistentFlags().StringVar(&evEmail, "email", "", "identity to authenticate as; it must be a member of the tenant (required)")
	eventsCmd.PersistentFlags().StringVar(&evPassword, "password", "", "password for the identity (required)")
	eventsCmd.PersistentFlags().String("otp", "", otpFlagHelp)

	f := eventsFailedCmd.Flags()
	f.StringVar(&evReason, "reason", "", "only this reason: "+strings.Join(failedevents.Reasons, ", ")+" (any case; '-' for '_')")
	f.StringVar(&evDevice, "device", "", "only this device token, as the sender named it")
	f.StringVar(&evSince, "since", "",
		"only records from this RFC3339 time onward, or this long ago (e.g. 2026-09-04T00:00:00Z or 6h); the server reads at most 31 days back")
	f.StringVar(&evUntil, "until", "", "only records up to this RFC3339 time, or up to this long ago")
	f.IntVar(&evLimit, "limit", 100, "most records to fetch")
	f.BoolVar(&evSummary, "summary", false, "count failures per reason instead of listing them (default window 24h)")
	f.StringVarP(&evOutput, "output", "o", "table", "output format: table or json")

	eventsCmd.AddCommand(eventsFailedCmd)
	rootCmd.AddCommand(eventsCmd)
}

// parseWhen reads an RFC3339 instant, or a duration meaning that long before now.
func parseWhen(flag, v string) (time.Time, error) {
	if t, err := time.Parse(time.RFC3339, v); err == nil {
		return t.UTC(), nil
	}
	if d, err := time.ParseDuration(v); err == nil {
		if d < 0 {
			return time.Time{}, fmt.Errorf("--%s must not be negative", flag)
		}
		return timeNow().UTC().Add(-d), nil
	}
	return time.Time{}, fmt.Errorf("--%s must be an RFC3339 timestamp (e.g. 2026-09-04T00:00:00Z) or a duration such as 6h", flag)
}

func runEventsFailed(cmd *cobra.Command, args []string) error {
	server, _ := cmd.Flags().GetString("server")
	tls, _ := cmd.Flags().GetBool("tls")
	if strings.TrimSpace(evTenant) == "" {
		return fmt.Errorf("--tenant is required: failed events are per-tenant, and there is no default")
	}
	if evEmail == "" || evPassword == "" {
		return fmt.Errorf("--email and --password are required")
	}
	if evOutput != "table" && evOutput != "json" {
		return fmt.Errorf("--output must be table or json, not %q", evOutput)
	}
	if evLimit < 1 {
		return fmt.Errorf("--limit must be at least 1")
	}
	if evSummary {
		// The summary is counts per reason over a window; every list narrowing would be
		// silently ignored, and a filter that is ignored reads as one that was applied.
		for _, n := range []string{"reason", "device", "until", "limit"} {
			if cmd.Flags().Changed(n) {
				return fmt.Errorf("--%s narrows the list and does not apply to --summary, which takes only --since", n)
			}
		}
	}

	opts := failedevents.Options{Endpoint: sim.EventManagementGraphQL(server, tls), Device: evDevice,
		Limit: evLimit, Now: timeNow}
	if evReason != "" {
		r, err := failedevents.NormalizeReason(evReason)
		if err != nil {
			return fmt.Errorf("--reason: %w", err)
		}
		opts.Reason = r
	}
	if evSince != "" {
		t, err := parseWhen("since", evSince)
		if err != nil {
			return err
		}
		opts.Since = &t
	}
	if evUntil != "" {
		t, err := parseWhen("until", evUntil)
		if err != nil {
			return err
		}
		opts.Until = &t
	}
	// The server never reads past 31 days back, so what is echoed in the output is the
	// instant it will really use, not the one that was typed.
	if opts.Since != nil {
		c := failedevents.ClampSince(timeNow(), *opts.Since)
		opts.Since = &c
	}
	if opts.Since != nil && opts.Until != nil && opts.Since.After(*opts.Until) {
		return fmt.Errorf("--since (%s) is later than --until (%s): that window is empty",
			opts.Since.Format(time.RFC3339), opts.Until.Format(time.RFC3339))
	}

	endpoints := sim.ResolveEndpoints(server, "", "", tls)
	session := userclient.NewTenantSession(nil, endpoints.UserGraphQL, evEmail, evPassword, evTenant).
		WithOTP(otpFunc(otpFlag(cmd)))
	out := cmd.OutOrStdout()

	if evSummary {
		since := timeNow().UTC().Add(-summaryDefaultWindow)
		if opts.Since != nil {
			since = *opts.Since
		}
		rows, err := failedevents.Summarize(cmd.Context(), session, opts.Endpoint, since)
		if err != nil {
			return err
		}
		if evOutput == "json" {
			return failedevents.PrintJSON(out, rows)
		}
		failedevents.PrintSummary(out, since, rows)
		return nil
	}

	l, err := failedevents.List(cmd.Context(), session, opts)
	if err != nil {
		return err
	}
	if evOutput == "json" {
		return failedevents.PrintJSON(out, l)
	}
	failedevents.PrintList(out, opts, l)
	return nil
}
