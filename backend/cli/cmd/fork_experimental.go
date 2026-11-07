// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/devicechain-io/dc-microservice/userclient"
	"github.com/devicechain-io/dcctl/sim"
	"github.com/spf13/cobra"
)

// experimentalForkTimelineEnv gates `dcctl fork`. The command is registered only when it is exactly
// "true", so a default dcctl neither lists nor can run it, and the experiment stays off the served
// command surface until it is promoted.
const experimentalForkTimelineEnv = "DC_EXPERIMENTAL_FORK_TIMELINE"

var (
	fkTenant, fkEmail, fkPassword, fkProfile string
	fkStart, fkEnd, fkOutput                 string
	fkPerturb                                []string
	fkTolerance                              time.Duration
)

const forkQuery = `query ($input: ExperimentalForkTimelineInput!) {
  experimentalForkTimeline(input: $input) {
    ok error degraded perturbations ruleCount
    baseline { occurredAt series signal }
    fork { occurredAt series signal }
    divergence { kind series signal at baselineAt deltaSeconds }
  }
}`

var forkCmd = &cobra.Command{
	Use:   "fork",
	Short: "EXPERIMENTAL: replay recorded history under a what-if and report how it diverges from reality",
	Long: `EXPERIMENTAL: fork the timeline.

Replays a profile's recorded events through the tenant's deployed detection rules twice: once as
they happened, and once after perturbations are applied to the recording (a device lost, a sensor
offset or scaled, a gateway dark for a while). It prints the alarms that appear, vanish or move
because of the perturbation. Read-only: nothing is saved, published or sent to a device.

A perturbation is --perturb "<op>,key=value,...". Operators:
  drop-device    device=<token>[,from=<RFC3339>][,to=<RFC3339>]       a device failed
  metric-offset  metric=<name>[,device=<token>],offset=<n>[,scale=<n>][,from=][,to=]
                                                                       a sensor offset, or a load scaled
  metric-freeze  metric=<name>[,device=<token>][,value=<n>][,from=][,to=]   a stuck sensor
  blackout       [device=<token>],from=<RFC3339>,to=<RFC3339>          a gateway dark for a window

Enabled only when ` + experimentalForkTimelineEnv + `=true is set, here and on the event-processing
service. This surface may change or be removed in any release.`,
	Example: `  dcctl fork -t acme --email ops@acme.example --password ... --profile chiller \
      --start 2026-11-01T12:00:00Z --end 2026-11-01T13:00:00Z \
      --perturb "metric-offset,metric=temperature,offset=5" \
      --perturb "drop-device,device=chiller-3,from=2026-11-01T12:30:00Z"`,
	Args:         cobra.NoArgs,
	RunE:         runFork,
	SilenceUsage: true,
}

func init() {
	if os.Getenv(experimentalForkTimelineEnv) != "true" {
		return
	}
	f := forkCmd.Flags()
	f.StringP("server", "s", "localhost", "instance host for platform API calls")
	f.Bool("tls", false, "use https for platform endpoints")
	f.StringVarP(&fkTenant, "tenant", "t", "", "tenant whose recorded history is forked (required)")
	f.StringVar(&fkEmail, "email", "", "identity to authenticate as; it must be a member of the tenant (required)")
	f.StringVar(&fkPassword, "password", "", "password for the identity (required)")
	f.String("otp", "", otpFlagHelp)
	f.StringVar(&fkProfile, "profile", "", "token of the device profile whose live rules are replayed (required)")
	f.StringVar(&fkStart, "start", "", "window start, RFC3339 (required)")
	f.StringVar(&fkEnd, "end", "", "window end, RFC3339 (required)")
	f.StringArrayVar(&fkPerturb, "perturb", nil, "a perturbation, repeatable (see the operators above)")
	f.DurationVar(&fkTolerance, "tolerance", 0, "how far apart the same alarm edge may sit on the two timelines and still count as moved (default 5m)")
	f.StringVarP(&fkOutput, "output", "o", "text", "output format: text or json")
	rootCmd.AddCommand(forkCmd)
}

// parsePerturbation reads one --perturb value into the GraphQL input object.
func parsePerturbation(v string) (map[string]any, error) {
	parts := strings.Split(v, ",")
	op := strings.TrimSpace(parts[0])
	if op == "" || strings.Contains(op, "=") {
		return nil, fmt.Errorf("--perturb %q: start with the operator name, e.g. drop-device,device=pump-7", v)
	}
	out := map[string]any{"op": op}
	for _, kv := range parts[1:] {
		k, val, ok := strings.Cut(kv, "=")
		k, val = strings.TrimSpace(k), strings.TrimSpace(val)
		if !ok || k == "" || val == "" {
			return nil, fmt.Errorf("--perturb %q: %q is not key=value", v, kv)
		}
		switch k {
		case "device", "metric":
			out[k] = val
		case "from", "to":
			if _, err := time.Parse(time.RFC3339, val); err != nil {
				return nil, fmt.Errorf("--perturb %q: %s must be RFC3339: %w", v, k, err)
			}
			out[k] = val
		case "offset", "scale", "value":
			n, err := strconv.ParseFloat(val, 64)
			if err != nil {
				return nil, fmt.Errorf("--perturb %q: %s must be a number: %w", v, k, err)
			}
			out[k] = n
		default:
			return nil, fmt.Errorf("--perturb %q: unknown key %q (want device, metric, offset, scale, value, from, to)", v, k)
		}
	}
	return out, nil
}

type forkFiring struct {
	OccurredAt string `json:"occurredAt"`
	Series     string `json:"series"`
	Signal     string `json:"signal"`
}

type forkResult struct {
	OK            bool         `json:"ok"`
	Error         *string      `json:"error"`
	Degraded      *string      `json:"degraded"`
	Perturbations []string     `json:"perturbations"`
	RuleCount     int          `json:"ruleCount"`
	Baseline      []forkFiring `json:"baseline"`
	Fork          []forkFiring `json:"fork"`
	Divergence    []struct {
		Kind         string  `json:"kind"`
		Series       string  `json:"series"`
		Signal       string  `json:"signal"`
		At           string  `json:"at"`
		BaselineAt   *string `json:"baselineAt"`
		DeltaSeconds float64 `json:"deltaSeconds"`
	} `json:"divergence"`
}

// renderFork writes the transcript an operator reads: reality, the fork, and the divergence.
func renderFork(w *strings.Builder, r *forkResult) {
	fmt.Fprintf(w, "Fork the timeline (%d live rule(s) replayed)\n", r.RuleCount)
	for _, p := range r.Perturbations {
		fmt.Fprintf(w, "  perturb  %s\n", p)
	}
	fmt.Fprintf(w, "  reality  %d firing(s)\n  fork     %d firing(s)\n", len(r.Baseline), len(r.Fork))
	if len(r.Divergence) == 0 {
		fmt.Fprintln(w, "No divergence: the fork behaves exactly like reality.")
		return
	}
	fmt.Fprintf(w, "Divergence (%d):\n", len(r.Divergence))
	for _, d := range r.Divergence {
		extra := ""
		if d.Kind == "shifted" && d.BaselineAt != nil {
			extra = fmt.Sprintf("  (%+.0fs vs reality %s)", d.DeltaSeconds, *d.BaselineAt)
		}
		fmt.Fprintf(w, "  %s  %-8s %-14s %s%s\n", d.At, d.Kind, d.Series, d.Signal, extra)
	}
}

func runFork(cmd *cobra.Command, _ []string) error {
	switch {
	case fkTenant == "":
		return fmt.Errorf("--tenant is required: recorded history is per-tenant, and there is no default")
	case fkEmail == "" || fkPassword == "":
		return fmt.Errorf("--email and --password are required")
	case fkProfile == "" || fkStart == "" || fkEnd == "":
		return fmt.Errorf("--profile, --start and --end are required")
	case len(fkPerturb) == 0:
		return fmt.Errorf("give at least one --perturb")
	case fkOutput != "text" && fkOutput != "json":
		return fmt.Errorf("--output must be text or json, not %q", fkOutput)
	}
	perts := make([]map[string]any, 0, len(fkPerturb))
	for _, p := range fkPerturb {
		m, err := parsePerturbation(p)
		if err != nil {
			return err
		}
		perts = append(perts, m)
	}
	server, _ := cmd.Flags().GetString("server")
	tls, _ := cmd.Flags().GetBool("tls")
	endpoints := sim.ResolveEndpoints(server, "", "", tls)
	session := userclient.NewTenantSession(nil, endpoints.UserGraphQL, fkEmail, fkPassword, fkTenant).WithOTP(otpFunc(otpFlag(cmd)))
	in := map[string]any{"profileToken": fkProfile, "start": fkStart, "end": fkEnd, "perturbations": perts}
	if fkTolerance > 0 {
		in["shiftToleranceSeconds"] = int(fkTolerance / time.Second)
	}
	var out struct {
		Fork forkResult `json:"experimentalForkTimeline"`
	}
	if err := session.Query(cmd.Context(), endpoints.EventProcessingGraphQL, forkQuery, map[string]any{"input": in}, &out); err != nil {
		return err
	}
	if !out.Fork.OK {
		msg := "refused"
		if out.Fork.Error != nil {
			msg = *out.Fork.Error
		}
		return fmt.Errorf("fork refused: %s", msg)
	}
	w := cmd.OutOrStdout()
	if fkOutput == "json" {
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(out.Fork)
	}
	var b strings.Builder
	renderFork(&b, &out.Fork)
	fmt.Fprint(w, b.String())
	if out.Fork.Degraded != nil {
		fmt.Fprintf(w, "\nPartial result: %s\n", *out.Fork.Degraded)
	}
	return nil
}
