// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"fmt"
	"os"
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
status:

  0  every expectation held
  1  an expectation was judged and did not hold
  2  something could not be judged: a file did not load, the platform refused the evaluation,
     sign-in or the network failed, or an expectation was inconclusive (it names a rule that
     was not evaluated, or it claims an absence or a count on a partial result)

A directory with no test file is an error, because a run that checks nothing must not pass. An
expectation about a rule the evaluation did not run is an error, never a pass. When the platform
is busy, dcctl waits and retries a few times before reporting it.

The file formats are in the preview tier (apiVersion devicechain.io/ruletest/v1alpha1) and may
change in a minor release, with the change in the release notes.

The server authorizes the evaluation as the identity you sign in with (device:read). The password
may be given in the DCCTL_PASSWORD environment variable instead of --password, which keeps it out
of the shell history and the process list.`,
	Args:         cobra.ExactArgs(1),
	RunE:         runTest,
	SilenceUsage: true,
}

func init() {
	testCmd.Flags().StringVarP(&testTenant, "tenant", "t", "", "tenant whose rules are tested (required)")
	testCmd.Flags().StringP("server", "s", "localhost", "instance host for platform API calls")
	addInstanceTargetFlags(testCmd)
	testCmd.Flags().Bool("tls", false, "use https for platform endpoints")
	testCmd.Flags().StringVar(&testEmail, "email", "", "identity to authenticate as; it must be a member of the tenant (required)")
	testCmd.Flags().StringVar(&testPassword, "password", "", "password for the identity (required unless DCCTL_PASSWORD is set)")
	testCmd.Flags().String("otp", "", otpFlagHelp)
	testCmd.Flags().String("junit", "", "also write the results as JUnit XML to this file")
	rootCmd.AddCommand(testCmd)
}

// passwordEnv is the environment variable `dcctl test` reads the password from when --password is
// not given.
const passwordEnv = "DCCTL_PASSWORD"

func runTest(cmd *cobra.Command, args []string) error {
	server, tls, err := targetFlags(cmd)
	if err != nil {
		return err
	}
	junit, _ := cmd.Flags().GetString("junit")
	password := testPassword
	if password == "" {
		password = os.Getenv(passwordEnv)
	}
	if strings.TrimSpace(testTenant) == "" {
		return ruletestrun.Errored(fmt.Errorf("--tenant is required: rules are per-tenant, and there is no default"))
	}
	if strings.TrimSpace(testEmail) == "" || strings.TrimSpace(password) == "" {
		return ruletestrun.Errored(fmt.Errorf("--email and --password (or %s) are required: the evaluation is authorized as the identity that runs it", passwordEnv))
	}
	// Read and validate every file before signing in: a typo in a test file should cost no
	// round trip.
	plan, err := ruletest.Load(args[0])
	if err != nil {
		// A CI job that reads the JUnit report sees the failure there too, not a missing file.
		if junit != "" {
			if werr := ruletestrun.WriteJUnit(junit, []ruletest.Case{{Suite: args[0], Name: "load", Error: err.Error()}}); werr != nil {
				fmt.Fprintf(cmd.ErrOrStderr(), "writing the JUnit report: %v\n", werr)
			}
		}
		return ruletestrun.Errored(err)
	}
	endpoints := sim.ResolveEndpoints(server, "", "", tls)
	session := userclient.NewTenantSession(nil, endpoints.UserGraphQL, testEmail, password, testTenant).
		WithOTP(otpFunc(otpFlag(cmd)))
	cases, err := ruletestrun.Run(cmd.Context(), plan, ruletestrun.Options{
		Client: session, Endpoint: endpoints.EventProcessingGraphQL, Out: cmd.OutOrStdout(),
	})
	if err != nil {
		return ruletestrun.Errored(err)
	}
	if junit != "" {
		if err := ruletestrun.WriteJUnit(junit, cases); err != nil {
			return ruletestrun.Errored(fmt.Errorf("writing the JUnit report: %w", err))
		}
	}
	passed, failed, errored := ruletestrun.Summary(cases)
	fmt.Fprintln(cmd.OutOrStdout(), ruletestrun.Describe(passed, failed, errored))
	return ruletestrun.Outcome(failed, errored)
}
