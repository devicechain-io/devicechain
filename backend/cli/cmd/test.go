// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"fmt"
	"strings"

	"github.com/devicechain-io/dc-microservice/ruletest"
	"github.com/devicechain-io/dc-microservice/userclient"
	"github.com/devicechain-io/dcctl/ruletestrun"
	"github.com/devicechain-io/dcctl/sim"
	"github.com/spf13/cobra"
)

var (
	testTenant   string
	testEmail    string
	testPassword string
)

var testCmd = &cobra.Command{
	Use:   "test <path>",
	Short: "Run rule unit tests against a tenant's detection rules (preview)",
	Long: `Run rule unit tests: check what a tenant's detection rules would do over events you describe,
without waiting for them to happen and without touching live traffic.

<path> is a directory, searched for *.test.yaml files, or one test file. Each test file runs a
scenario (scenario.yaml in the same directory unless the file names another) and checks the
result against its expect blocks. A scenario says what happens: devices, the readings each
reports (a constant, a bounded random walk, a step schedule, or a recorded CSV or NDJSON series),
and the faults delivery suffers (a gap, duplicates, reordering, an offline window), all from a
seed, so a failing run fails the same way every time. A test file says what should follow:
which alarms are raised or cleared, within what time, which never are, and which side effect
(a command, a webhook, a published message) is requested.

The platform evaluates the rules, not dcctl: the same detection engine and the same REACT
rendering that run them in production, in event time, over exactly the events the scenario
generates. A rule that waits seven days is tested by advancing seven days, which takes
milliseconds. Nothing is executed: an intent in the result is what a rule ASKS for. No alarm is
raised, no command sent, no request made, and live detection is not touched.

The scenario evaluates the profile's PUBLISHED rules, or a DRAFT given as a rule definition or a
canvas graph file, so a rule can be tested before it is published.

The result is a report on standard output, optionally JUnit XML (--junit) for CI, and an exit
status: 0 when every expectation held, non-zero when one failed or a file could not be run. A
directory with no test file is an error, because a run that checks nothing must not pass.

The file formats are in the preview tier (apiVersion devicechain.io/ruletest/v1alpha1) and may
change in a minor release, with the change in the release notes.

The server authorizes the evaluation as the identity you sign in with (device:read).`,
	Args:         cobra.ExactArgs(1),
	RunE:         runTest,
	SilenceUsage: true,
}

func init() {
	testCmd.Flags().StringVarP(&testTenant, "tenant", "t", "", "tenant whose rules are tested (required)")
	testCmd.Flags().StringP("server", "s", "localhost", "instance host for platform API calls")
	testCmd.Flags().Bool("tls", false, "use https for platform endpoints")
	testCmd.Flags().StringVar(&testEmail, "email", "", "identity to authenticate as; it must be a member of the tenant (required)")
	testCmd.Flags().StringVar(&testPassword, "password", "", "password for the identity (required)")
	testCmd.Flags().String("otp", "", otpFlagHelp)
	testCmd.Flags().String("junit", "", "also write the results as JUnit XML to this file")
	rootCmd.AddCommand(testCmd)
}

func runTest(cmd *cobra.Command, args []string) error {
	server, _ := cmd.Flags().GetString("server")
	tls, _ := cmd.Flags().GetBool("tls")
	junit, _ := cmd.Flags().GetString("junit")
	if strings.TrimSpace(testTenant) == "" {
		return fmt.Errorf("--tenant is required: rules are per-tenant, and there is no default")
	}
	if strings.TrimSpace(testEmail) == "" || strings.TrimSpace(testPassword) == "" {
		return fmt.Errorf("--email and --password are required: the evaluation is authorized as the identity that runs it")
	}
	// Read and validate every file before signing in: a typo in a test file should cost no
	// round trip.
	plan, err := ruletest.Load(args[0])
	if err != nil {
		return err
	}
	endpoints := sim.ResolveEndpoints(server, "", "", tls)
	session := userclient.NewTenantSession(nil, endpoints.UserGraphQL, testEmail, testPassword, testTenant).
		WithOTP(otpFunc(otpFlag(cmd)))
	cases, err := ruletestrun.Run(cmd.Context(), plan, ruletestrun.Options{
		Client: session, Endpoint: endpoints.EventProcessingGraphQL, Out: cmd.OutOrStdout(),
	})
	if err != nil {
		return err
	}
	if junit != "" {
		if err := ruletestrun.WriteJUnit(junit, cases); err != nil {
			return fmt.Errorf("writing the JUnit report: %w", err)
		}
	}
	passed, failed, errored := ruletestrun.Summary(cases)
	fmt.Fprintln(cmd.OutOrStdout(), ruletestrun.Describe(passed, failed, errored))
	if failed > 0 || errored > 0 {
		return ruletestrun.ErrFailed
	}
	return nil
}
