// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/devicechain-io/dc-microservice/userclient"
	"github.com/devicechain-io/dcctl/apiclient"
	"github.com/devicechain-io/dcctl/bulkchange"
	"github.com/spf13/cobra"
)

var bulkChangeSession = newBulkChangeSession

func newBulkChangeSession(cmd *cobra.Command) (bulkchange.Client, string, error) {
	sessions, endpoint, err := tenantSessions(cmd)
	if err != nil {
		return nil, "", err
	}
	versions := recordedEndpointVersions(cmd, endpoint)
	// One otp for every sign-in: a step-up never offers the --otp code a sign-in already used,
	// it asks a terminal for a new one or stops with an error that names --otp.
	otp := otpFunc(otpFlag(cmd))
	session := func() *userclient.TenantSession {
		s := sessions(otp).WithSchemaNegotiation(apiclient.LegacySchema)
		for _, version := range versions {
			s.WithLegacyEndpointVersion(version.Endpoint, version.Version)
		}
		return s
	}
	return bulkchange.NewStepUpClient(session(), session), endpoint, nil
}

func addBulkChangeCommands(parent *cobra.Command) {
	for _, name := range []string{"plan", "create", "start", "status", "lines", "pause", "resume", "abort", "reverse", "resolve"} {
		c := &cobra.Command{Use: name + " TOKEN", Args: cobra.ExactArgs(1), SilenceUsage: true, RunE: runBulkChange}
		switch name {
		case "plan", "create":
			c.Use, c.Args = name, cobra.NoArgs
			c.Short = "Preview. " + strings.ToUpper(name[:1]) + name[1:] + " a device configuration bulk change"
			c.Long = c.Short + ".\n\nUse --kind config.set and --key with either --value and --type or --unset.\nTargets are repeated --device tokens or a UTF-8 --devices-file with one token per nonempty line.\nThe default token is SHA-256 of tenant, exact file bytes (or the ordered JSON array of --device values), kind and canonical intent, concatenated in that order. Reusing the token with a changed request is refused.\nPlanning writes nothing. Creating freezes a READY bulk change; review its contract and present the server's contractDigest to start it."
			c.Flags().String("kind", "", "Preview. Change kind: config.set (required)")
			c.Flags().String("key", "", "Preview. Declared SHARED configuration key (required)")
			c.Flags().String("value", "", "Preview. Value as text; use with --type, including for an empty value")
			c.Flags().String("type", "", "Preview. Value type: STRING, LONG, DOUBLE, BOOLEAN or JSON")
			c.Flags().Bool("unset", false, "Preview. Remove the key instead of setting a value")
			c.Flags().StringArray("device", nil, "Preview. Device token; repeat for each device, preserving order")
			c.Flags().String("devices-file", "", "Preview. UTF-8 file containing one device token per nonempty line")
			c.Flags().String("token", "", "Preview. Idempotency token; defaults to the input-derived SHA-256")
			c.Flags().String("name", "", "Preview. Display name; defaults to the bulk change token")
			addBulkChangeLimits(c)
			c.Flags().String("settle-deadline", "24h", "Preview. Evidence deadline: duration such as 24h or integer seconds, from 60s to 168h")
		case "start":
			c.Short = "Preview. Start a reviewed READY bulk change"
			c.Long = c.Short + ".\n\nPresent --contract-digest exactly as returned by the server after reviewing its contract. The client never computes an approval digest."
			c.Flags().String("contract-digest", "", "Preview. Exact server contract digest you reviewed (required)")
		case "status":
			c.Short = "Preview. Inspect dispatch and evidence for a bulk change"
			c.Long = c.Short + ".\n\nExit 0 only for DISPATCHED with CLOSED evidence, 3 for PAUSED, 4 for ABORTED, and 1 for other states or errors. DISPATCHED with OPEN evidence is still awaiting verdicts.\n--watch polls until completion, pause, abort or interruption. JSON watch output is one object per line."
			c.Flags().Bool("watch", false, "Preview. Wait for DISPATCHED+CLOSED, pause or abort; interrupt to stop waiting")
		case "lines":
			c.Short = "Preview. List every page of a bulk change's lines"
			c.Long = c.Short + ".\n\nFilter by posting or settlement. JSON includes the full line evidence; CSV quotes embedded commas, quotes and newlines."
			c.Flags().String("posting", "", "Preview. Filter by posting state, for example POSTED")
			c.Flags().String("settlement", "", "Preview. Filter by settlement state, for example AMBIGUOUS")
			c.Flags().String("devices-file", "", "Preview. Original UTF-8 device input file to join by ordinal; erased identifiers stay erased")
		case "pause", "abort":
			c.Short = "Preview. " + strings.ToUpper(name[:1]) + name[1:] + " a bulk change"
			c.Long = c.Short + ".\n\nPause stops new posts until resume. Abort cancels unposted lines permanently; posted lines keep settling. A fence timeout is reported as an error."
		case "resume":
			c.Short = "Preview. Resume a paused bulk change within its approved contract"
			c.Long = c.Short + ".\n\nOmitted limits remain unchanged. Without --contract-digest, the client asks the server for a resume preview and uses its digest. A wider contract is refused in this release."
			addBulkChangeLimits(c)
			c.Flags().String("contract-digest", "", "Preview. Reviewed server contract digest; default: request a server resume preview")
		case "reverse":
			c.Short = "Preview. Preview or create the reversal of a DISPATCHED or ABORTED bulk change"
			c.Long = c.Short + ".\n\nA reversal is a new bulk change that puts back, line by line, the value each device held before the original.\nWithout --create nothing is written: the server reports how many lines would be reversed, how many changed after the original was applied, how many were erased, already reversed or never applied, and the digests.\nWith --create the reversal is created in READY; review its contract and start it like any bulk change.\nA line whose value changed after the original is refused with CHANGED_SINCE unless --override-changed (every selected line) or --override-ordinals overrides it. Creating with an override needs a second factor verified within the last five minutes: when the server asks for a fresher one, the command signs in again and asks for a new authenticator code (a recovery code does not count).\nRepeating --create with the same --token and the same request returns the first reversal."
			c.Flags().String("token", "", "Preview. Token of the reversal, unique within the tenant (required)")
			c.Flags().String("name", "", "Preview. Display name; defaults to the reversal token")
			c.Flags().IntSlice("ordinals", nil, "Preview. Positions of the original's lines to reverse, starting at 1; default: every applied line")
			c.Flags().Bool("override-changed", false, "Preview. Apply every selected line even where the value changed after the original; needs a recent second factor")
			c.Flags().IntSlice("override-ordinals", nil, "Preview. Apply these lines even where the value changed after the original; needs a recent second factor")
			c.Flags().Bool("create", false, "Preview. Create the reversal in READY; without it nothing is written")
			addBulkChangeLimits(c)
			c.Flags().String("settle-deadline", "24h", "Preview. Evidence deadline: duration such as 24h or integer seconds, from 60s to 168h")
		case "resolve":
			c.Short = "Preview. Resolve ambiguous lines of a bulk change as confirmed or failed"
			c.Long = c.Short + ".\n\nSelect lines with --ordinals or every ambiguous line with --all-ambiguous. Lines that are not ambiguous are left unchanged.\nA note of 1 to 1000 characters records why. The verdict is a person's, not device evidence: a later report that contradicts it sets the line's evidence flag.\nResolving needs a person's own sign-in with a second factor verified within the last five minutes. When the server asks for a fresher one, the command signs in again and asks for a new authenticator code (a recovery code does not count)."
			c.Flags().IntSlice("ordinals", nil, "Preview. Positions of the lines to resolve, starting at 1")
			c.Flags().Bool("all-ambiguous", false, "Preview. Resolve every ambiguous line instead of selecting ordinals")
			c.Flags().String("verdict", "", "Preview. Verdict: confirmed or failed (required)")
			c.Flags().String("note", "", "Preview. Why, from 1 to 1000 characters (required)")
		}
		formats := "Preview. Output format: table or json"
		if name == "lines" {
			formats = "Preview. Output format: table, json or csv"
		}
		c.Flags().StringP("output", "o", "table", formats)
		parent.AddCommand(c)
	}
}

func addBulkChangeLimits(c *cobra.Command) {
	c.Flags().Int("max-posts-per-second", 10, "Preview. Maximum posts per second, from 1 to 50; omitted on resume: unchanged")
	c.Flags().Int("failure-threshold", 0, "Preview. Positive failed/refused line count that pauses dispatch; omitted on create: no limit")
	c.Flags().Int("ambiguity-threshold", 0, "Preview. Positive ambiguous line count that pauses dispatch; omitted on create: no limit")
}

func bulkChangeLimits(cmd *cobra.Command) (map[string]any, error) {
	limits := map[string]any{}
	for flag, field := range map[string]string{"max-posts-per-second": "maxPostsPerSecond", "failure-threshold": "failureThreshold", "ambiguity-threshold": "ambiguityThreshold"} {
		if !cmd.Flags().Changed(flag) {
			continue
		}
		n, _ := cmd.Flags().GetInt(flag)
		if n < 1 || n > 2147483647 || flag == "max-posts-per-second" && n > 50 {
			return nil, fmt.Errorf("--%s is outside its supported range", flag)
		}
		limits[field] = n
	}
	return limits, nil
}

// bulkChangeCreateSafety is BulkChangeCreateSafety from the limit flags and --settle-deadline;
// a flag left out is left out of the request.
func bulkChangeCreateSafety(cmd *cobra.Command) (map[string]any, error) {
	safety, err := bulkChangeLimits(cmd)
	if err != nil {
		return nil, err
	}
	if cmd.Flags().Changed("settle-deadline") {
		deadline, _ := cmd.Flags().GetString("settle-deadline")
		seconds, intErr := strconv.ParseInt(deadline, 10, 32)
		if intErr != nil {
			duration, durationErr := time.ParseDuration(deadline)
			if durationErr != nil || duration%time.Second != 0 {
				return nil, fmt.Errorf("--settle-deadline must be a whole number of seconds or a duration")
			}
			seconds = int64(duration / time.Second)
		}
		if seconds < 60 || seconds > 604800 {
			return nil, fmt.Errorf("--settle-deadline must be from 60s to 168h")
		}
		safety["settleDeadlineSeconds"] = seconds
	}
	return safety, nil
}

// bulkChangeReversalRequest reads reverse's flags. Ordinal flags left out stay nil, which the
// server reads as "every applied line" and "no override".
func bulkChangeReversalRequest(cmd *cobra.Command, original string) (bulkchange.ReversalRequest, error) {
	flags := cmd.Flags()
	request := bulkchange.ReversalRequest{Original: original}
	request.Token, _ = flags.GetString("token")
	request.Name, _ = flags.GetString("name")
	if request.Name == "" {
		request.Name = request.Token
	}
	if flags.Changed("ordinals") {
		request.Ordinals, _ = flags.GetIntSlice("ordinals")
		if request.Ordinals == nil {
			request.Ordinals = []int{}
		}
	}
	request.OverrideChanged, _ = flags.GetBool("override-changed")
	if flags.Changed("override-ordinals") {
		request.OverrideOrdinals, _ = flags.GetIntSlice("override-ordinals")
		if len(request.OverrideOrdinals) == 0 {
			return request, fmt.Errorf("--override-ordinals must name at least one line")
		}
	}
	var err error
	if request.Safety, err = bulkChangeCreateSafety(cmd); err != nil {
		return request, err
	}
	if len(request.Safety) == 0 {
		request.Safety = nil
	}
	return request, request.Validate()
}

func bulkChangeResolution(cmd *cobra.Command, token string) (bulkchange.Resolution, error) {
	flags := cmd.Flags()
	resolution := bulkchange.Resolution{Token: token}
	if flags.Changed("ordinals") {
		resolution.Ordinals, _ = flags.GetIntSlice("ordinals")
		// An explicitly empty --ordinals is dropped from the request (omitempty), which the
		// server reads as "every ambiguous line": refuse it rather than widen the selection.
		if len(resolution.Ordinals) == 0 {
			return resolution, fmt.Errorf("--ordinals must name at least one line")
		}
	}
	resolution.AllAmbiguous, _ = flags.GetBool("all-ambiguous")
	resolution.Verdict, _ = flags.GetString("verdict")
	resolution.Note, _ = flags.GetString("note")
	return resolution, resolution.Validate()
}

func bulkChangeRequest(cmd *cobra.Command) (bulkchange.Request, error) {
	var request bulkchange.Request
	flags := cmd.Flags()
	request.Kind, _ = flags.GetString("kind")
	request.ConfigSet.Key, _ = flags.GetString("key")
	request.ConfigSet.Key = strings.TrimSpace(request.ConfigSet.Key)
	if request.Kind != "config.set" || request.ConfigSet.Key == "" {
		return request, fmt.Errorf("--kind config.set and --key are required")
	}
	request.ConfigSet.Unset, _ = flags.GetBool("unset")
	if request.ConfigSet.Unset {
		if flags.Changed("value") || flags.Changed("type") {
			return request, fmt.Errorf("--unset cannot be combined with --value or --type")
		}
	} else {
		if !flags.Changed("value") || !flags.Changed("type") {
			return request, fmt.Errorf("--value and --type are required unless --unset is given")
		}
		value, _ := flags.GetString("value")
		request.ConfigSet.Value = &value
		request.ConfigSet.ValueType, _ = flags.GetString("type")
		switch request.ConfigSet.ValueType {
		case "STRING", "LONG", "DOUBLE", "BOOLEAN", "JSON":
		default:
			return request, fmt.Errorf("--type must be STRING, LONG, DOUBLE, BOOLEAN or JSON")
		}
	}
	path, _ := flags.GetString("devices-file")
	devices, _ := flags.GetStringArray("device")
	if flags.Changed("devices-file") == flags.Changed("device") {
		return request, fmt.Errorf("use exactly one of --devices-file or repeated --device")
	}
	var raw []byte
	var err error
	if flags.Changed("devices-file") {
		devices, raw, err = bulkchange.ReadDevices(path)
		if err != nil {
			return request, err
		}
	} else {
		for _, device := range devices {
			if strings.TrimSpace(device) == "" {
				return request, fmt.Errorf("--device must not be empty")
			}
		}
		raw = bulkchange.DeviceBytes(devices)
	}
	request.DeviceTokens = devices
	request.Safety, err = bulkChangeCreateSafety(cmd)
	if err != nil {
		return request, err
	}
	request.Token, _ = flags.GetString("token")
	if request.Token == "" {
		tenant, _ := flags.GetString("tenant")
		request.Token, err = bulkchange.DefaultToken(tenant, raw, request.Kind, request.ConfigSet)
		if err != nil {
			return request, err
		}
	}
	request.Name, _ = flags.GetString("name")
	if request.Name == "" {
		request.Name = request.Token
	}
	return request, nil
}

func runBulkChange(cmd *cobra.Command, args []string) error {
	format, _ := cmd.Flags().GetString("output")
	if format != "table" && format != "json" && (cmd.Name() != "lines" || format != "csv") {
		return fmt.Errorf("unsupported output format %q", format)
	}
	var request bulkchange.Request
	var err error
	if cmd.Name() == "plan" || cmd.Name() == "create" {
		request, err = bulkChangeRequest(cmd)
		if err != nil {
			return err
		}
	}
	digest, _ := cmd.Flags().GetString("contract-digest")
	if cmd.Name() == "start" && strings.TrimSpace(digest) == "" {
		return fmt.Errorf("--contract-digest is required: present the server digest you reviewed")
	}
	var reversal bulkchange.ReversalRequest
	var resolution bulkchange.Resolution
	switch cmd.Name() {
	case "reverse":
		if reversal, err = bulkChangeReversalRequest(cmd, args[0]); err != nil {
			return err
		}
	case "resolve":
		if resolution, err = bulkChangeResolution(cmd, args[0]); err != nil {
			return err
		}
	}
	var limits map[string]any
	if cmd.Name() == "resume" {
		limits, err = bulkChangeLimits(cmd)
		if err != nil {
			return err
		}
	}
	client, endpoint, err := bulkChangeSession(cmd)
	if err != nil {
		return err
	}
	api := bulkchange.API{Client: client, Endpoint: endpoint}
	ctx, w := cmd.Context(), cmd.OutOrStdout()
	switch cmd.Name() {
	case "plan":
		plan, err := api.Plan(ctx, request)
		if err != nil {
			return err
		}
		if format == "json" {
			return bulkchange.PrintJSON(w, plan)
		}
		_, err = fmt.Fprintf(w, "token: %s\nadmitted: %d\nrefused: %d\nrequestDigest: %s\nplanDigest: %s\ncontractDigest: %s\nrefusalCounts: %v\nrefusals: %v\n", request.Token, plan.AdmittedCount, plan.RefusedCount, plan.RequestDigest, plan.PlanDigest, plan.ContractDigest, plan.RefusalCounts, plan.Refusals)
		return err
	case "create":
		change, err := api.Create(ctx, request)
		if err != nil {
			return err
		}
		return bulkchange.PrintChange(w, change, format == "json")
	case "status":
		watch, _ := cmd.Flags().GetBool("watch")
		return api.Status(ctx, args[0], w, watch, format == "json")
	case "lines":
		posting, _ := cmd.Flags().GetString("posting")
		settlement, _ := cmd.Flags().GetString("settlement")
		lines, err := api.Lines(ctx, args[0], posting, settlement)
		if err != nil {
			return err
		}
		path, _ := cmd.Flags().GetString("devices-file")
		if path != "" {
			devices, _, err := bulkchange.ReadDevices(path)
			if err != nil {
				return err
			}
			bulkchange.JoinDevices(lines, devices)
		}
		return bulkchange.PrintLines(w, lines, format)
	case "reverse":
		if create, _ := cmd.Flags().GetBool("create"); create {
			change, err := api.Reverse(ctx, reversal)
			if err != nil {
				return err
			}
			return bulkchange.PrintChange(w, change, format == "json")
		}
		preview, err := api.PreviewReversal(ctx, reversal)
		if err != nil {
			return err
		}
		if err := bulkchange.PrintReversalPreview(w, preview, format == "json"); err != nil {
			return err
		}
		fmt.Fprintln(cmd.ErrOrStderr(), "Nothing was written. Add --create to create this reversal in READY.")
		return nil
	case "resolve":
		change, err := api.Resolve(ctx, resolution)
		if err != nil {
			return err
		}
		return bulkchange.PrintChange(w, change, format == "json")
	default:
		change, err := api.Control(ctx, cmd.Name(), args[0], digest, limits)
		if err != nil {
			return err
		}
		return bulkchange.PrintChange(w, change, format == "json")
	}
}
