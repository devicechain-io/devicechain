// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/devicechain-io/dc-microservice/userclient"
	"github.com/devicechain-io/dcctl/physpr"
	"github.com/devicechain-io/dcctl/sim"
	"github.com/spf13/cobra"
)

// experimentalPhysicalPREnv gates the `rules` commands. They are registered only when it is exactly
// "true", so a default dcctl neither lists them nor can run them, and the experiment stays off the
// served command surface until it is promoted.
const experimentalPhysicalPREnv = "DC_EXPERIMENTAL_PHYSICAL_PR"

var (
	rxTenant, rxEmail, rxPassword string
	rxLedger, rxBase, rxHead      string
	rxHeadFile, rxStart, rxEnd    string
	rxShift                       time.Duration
	rxDevice, rxAt                string
	rxSlack                       time.Duration
	rxLookback                    time.Duration
	rxOutput                      string
	rxFailOnChange                bool
)

var rulesCmd = &cobra.Command{
	Use:   "rules",
	Short: "EXPERIMENTAL: review rule changes by replaying recorded traffic",
	Long: `EXPERIMENTAL: git's verbs for how a deployed fleet behaves.

A change to a detection rule is judged by what it would have done to real, recorded traffic,
not by how it reads. 'diff' replays the base and the head over the same window and reports the
alarms the change adds, suppresses or moves in time. 'bisect' replays a rule's published history
and names the version that changed an outcome, and the principal (a person or an agent) who
published it.

Both work from a ledger file: the ordered versions of one profile's rule, each with its
author. Both are read-only replays: nothing is saved, published or sent to a device.

Enabled only when ` + experimentalPhysicalPREnv + `=true is set, here and on the event-processing
service. This surface may change or be removed in any release.`,
}

var rulesDiffCmd = &cobra.Command{
	Use:   "diff",
	Short: "Replay two rule versions over recorded traffic and report the difference",
	Long: `Replay two versions of a rule over the same recorded window and print the review report.

--base names a ledger commit (default: the newest). The head is either another ledger commit
(--head) or a draft rule file (--head-file), the pull-request case. Use --fail-on-change to exit
non-zero when the head behaves differently, so a pipeline can require a human to look.`,
	Example: `  dcctl rules diff --ledger pump.ledger.json --base v3 --head-file draft.json \
      -t acme --email ops@acme.example --password ... --start 24h
  dcctl rules diff --ledger pump.ledger.json --base v3 --head v4 -t acme --email ... --password ...`,
	Args:         cobra.NoArgs,
	RunE:         runRulesDiff,
	SilenceUsage: true,
}

var rulesBisectCmd = &cobra.Command{
	Use:   "bisect",
	Short: "Name the rule version that changed whether an alarm fired on a device at a moment",
	Long: `Search a rule's published history for the version that changed the outcome.

Asks "was an alarm raised on --device within --slack of --at?" under the oldest and the newest
version, then bisects between them: about log2(n) replays rather than n. Prints the commit and
who published it.`,
	Example: `  dcctl rules bisect --ledger pump.ledger.json --device pump-17 --at 2026-11-01T14:20:00Z \
      -t acme --email ops@acme.example --password ...`,
	Args:         cobra.NoArgs,
	RunE:         runRulesBisect,
	SilenceUsage: true,
}

func init() {
	if os.Getenv(experimentalPhysicalPREnv) != "true" {
		return
	}
	rulesCmd.PersistentFlags().StringP("server", "s", "localhost", "instance host for platform API calls")
	rulesCmd.PersistentFlags().Bool("tls", false, "use https for platform endpoints")
	rulesCmd.PersistentFlags().StringVarP(&rxTenant, "tenant", "t", "", "tenant whose recorded traffic is replayed (required)")
	rulesCmd.PersistentFlags().StringVar(&rxEmail, "email", "", "identity to authenticate as; it must be a member of the tenant (required)")
	rulesCmd.PersistentFlags().StringVar(&rxPassword, "password", "", "password for the identity (required)")
	rulesCmd.PersistentFlags().String("otp", "", otpFlagHelp)
	rulesCmd.PersistentFlags().StringVar(&rxLedger, "ledger", "", "ledger file: the ordered published versions of one profile's rule (required)")
	rulesCmd.PersistentFlags().StringVarP(&rxOutput, "output", "o", "text", "output format: text or json")

	f := rulesDiffCmd.Flags()
	f.StringVar(&rxBase, "base", "", "ledger commit to change from (default: the newest)")
	f.StringVar(&rxHead, "head", "", "ledger commit to change to")
	f.StringVar(&rxHeadFile, "head-file", "", "a draft rule definition file to change to")
	f.StringVar(&rxStart, "start", "24h", "window start: an RFC3339 time, or how long before --end")
	f.StringVar(&rxEnd, "end", "", "window end as an RFC3339 time (default: now)")
	f.DurationVar(&rxShift, "shift-window", 0, "how far apart two alarms on a device may be and still be the same alarm moved (default 1h)")
	f.BoolVar(&rxFailOnChange, "fail-on-change", false, "exit non-zero when the head behaves differently from the base")

	b := rulesBisectCmd.Flags()
	b.StringVar(&rxDevice, "device", "", "device token the question is about (required)")
	b.StringVar(&rxAt, "at", "", "the moment in question, an RFC3339 time (required)")
	b.DurationVar(&rxSlack, "slack", 5*time.Minute, "how near the moment an alarm still counts")
	b.DurationVar(&rxLookback, "lookback", 6*time.Hour, "how much history before the moment each replay covers (1h to 24h)")

	rulesCmd.AddCommand(rulesDiffCmd, rulesBisectCmd)
	rootCmd.AddCommand(rulesCmd)
}

func rulesSession(cmd *cobra.Command) (*userclient.TenantSession, string, error) {
	server, _ := cmd.Flags().GetString("server")
	tls, _ := cmd.Flags().GetBool("tls")
	switch {
	case rxTenant == "":
		return nil, "", fmt.Errorf("--tenant is required: recorded traffic is per-tenant, and there is no default")
	case rxEmail == "" || rxPassword == "":
		return nil, "", fmt.Errorf("--email and --password are required")
	case rxLedger == "":
		return nil, "", fmt.Errorf("--ledger is required")
	case rxOutput != "text" && rxOutput != "json":
		return nil, "", fmt.Errorf("--output must be text or json, not %q", rxOutput)
	}
	endpoints := sim.ResolveEndpoints(server, "", "", tls)
	s := userclient.NewTenantSession(nil, endpoints.UserGraphQL, rxEmail, rxPassword, rxTenant).WithOTP(otpFunc(otpFlag(cmd)))
	return s, endpoints.EventProcessingGraphQL, nil
}

func runRulesDiff(cmd *cobra.Command, _ []string) error {
	session, endpoint, err := rulesSession(cmd)
	if err != nil {
		return err
	}
	ledger, err := physpr.LoadLedger(rxLedger)
	if err != nil {
		return err
	}
	if (rxHead == "") == (rxHeadFile == "") {
		return fmt.Errorf("give exactly one of --head (a ledger commit) and --head-file (a draft rule file)")
	}
	baseID := rxBase
	if baseID == "" {
		baseID = ledger.Versions[len(ledger.Versions)-1].Commit
	}
	bv, err := ledger.Find(baseID)
	if err != nil {
		return fmt.Errorf("--base: %w", err)
	}
	head := physpr.Side{Label: "draft"}
	if rxHead != "" {
		hv, err := ledger.Find(rxHead)
		if err != nil {
			return fmt.Errorf("--head: %w", err)
		}
		head = physpr.SideOf(hv)
	} else {
		raw, err := os.ReadFile(rxHeadFile)
		if err != nil {
			return err
		}
		if !json.Valid(raw) {
			return fmt.Errorf("%s is not valid JSON", rxHeadFile)
		}
		head.Definition = raw
		head.Label = rxHeadFile
	}

	end := timeNow().UTC()
	if rxEnd != "" {
		if end, err = parseWhen("end", rxEnd); err != nil {
			return err
		}
	}
	start, err := parseSince(rxStart, end)
	if err != nil {
		return err
	}
	res, err := physpr.Diff(cmd.Context(), session, physpr.DiffOptions{
		Endpoint: endpoint, Profile: ledger.Profile, Base: physpr.SideOf(bv), Head: head,
		Start: start, End: end, ShiftSeconds: int(rxShift / time.Second),
	})
	if err != nil {
		return err
	}
	out := cmd.OutOrStdout()
	if rxOutput == "json" {
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		if err := enc.Encode(res); err != nil {
			return err
		}
	} else {
		fmt.Fprint(out, res.Report)
		if res.Degraded != nil {
			fmt.Fprintf(out, "\nPartial result: %s\n", *res.Degraded)
		}
	}
	if rxFailOnChange && !res.Empty {
		return fmt.Errorf("the head behaves differently from the base over the window")
	}
	return nil
}

// parseSince reads --start: an RFC3339 instant, or a duration meaning that long before end.
func parseSince(v string, end time.Time) (time.Time, error) {
	if t, err := time.Parse(time.RFC3339, v); err == nil {
		return t.UTC(), nil
	}
	if d, err := time.ParseDuration(v); err == nil && d > 0 {
		return end.Add(-d), nil
	}
	return time.Time{}, fmt.Errorf("--start must be an RFC3339 timestamp or a positive duration such as 24h")
}

func runRulesBisect(cmd *cobra.Command, _ []string) error {
	session, endpoint, err := rulesSession(cmd)
	if err != nil {
		return err
	}
	ledger, err := physpr.LoadLedger(rxLedger)
	if err != nil {
		return err
	}
	if rxDevice == "" || rxAt == "" {
		return fmt.Errorf("--device and --at are required")
	}
	at, err := time.Parse(time.RFC3339, rxAt)
	if err != nil {
		return fmt.Errorf("--at must be an RFC3339 timestamp: %w", err)
	}
	if rxSlack < 0 {
		return fmt.Errorf("--slack must not be negative")
	}
	res, err := physpr.Bisect(cmd.Context(), session, physpr.BisectOptions{
		Endpoint: endpoint, Ledger: ledger, Series: rxDevice, At: at,
		SlackSeconds: int(rxSlack / time.Second), LookbackHours: int(rxLookback / time.Hour),
	})
	if err != nil {
		return err
	}
	out := cmd.OutOrStdout()
	if rxOutput == "json" {
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		return enc.Encode(res)
	}
	fmt.Fprint(out, res.Report)
	if res.Degraded != nil {
		fmt.Fprintf(out, "\nPartial result: %s\n", *res.Degraded)
	}
	return nil
}
