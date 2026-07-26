// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"fmt"
	"os"
	"time"

	"github.com/devicechain-io/dcctl/bootstrap"
	"github.com/fatih/color"
	"github.com/spf13/cobra"
)

var (
	haInstanceId  string
	haKubeContext string
	haReplicas    int
	haNatsUrl     string
	haTimeout     time.Duration
	haExpectFail  bool
)

var haCmd = &cobra.Command{
	Use:   "ha",
	Short: "Inspect an instance's high-availability posture",
}

var haVerifyCmd = &cobra.Command{
	Use:   "verify",
	Short: "Assert an instance's broker actually holds the replication it declares",
	Long: `Assert, from live broker state, that an instance's JetStream streams, KV
buckets, durable consumers and NATS pods are replicated the way the instance
declares (ADR-020 A0).

This reads the broker, not the deployment. The failure it exists to catch is an
instance that looks highly available from every rendered artifact -- a three-node
NATS cluster, three healthy pods, the HA toggle on -- while every stream and
bucket on it is single-replica: three times the compute, zero node failures
survived. Nothing about that state is visible from Helm values, OpenTofu state,
or pod health, because all three describe what was asked for.

The declared replica factor comes from the instance-config Secret the pods mount,
so the comparison is between what this instance claims and what its broker holds.

Exit status is the result: 0 when every assertion holds, 1 when any does not.

  dcctl ha verify --instance default

Use --expect-fail to invert that, for the negative control. A check suite that
cannot fail asserts nothing, so the drill runs the SAME command against a
single-node instance and requires it to report failures:

  dcctl ha verify --instance default --replicas 3 --expect-fail`,
	RunE: func(cmd *cobra.Command, args []string) error {
		rep, err := bootstrap.VerifyReplication(cmd.Context(), bootstrap.HaVerifyOptions{
			KubeContext: haKubeContext,
			InstanceId:  haInstanceId,
			Replicas:    haReplicas,
			NatsUrl:     haNatsUrl,
			Timeout:     haTimeout,
		})
		if err != nil {
			// A collection error is NOT a verdict. Printing it through the same
			// pass/fail channel would let "we could not reach the broker" be recorded
			// as either answer, which is how a drill ends up reporting on a check it
			// never ran.
			return err
		}
		fmt.Print(rep.Format())

		if haExpectFail {
			if rep.OK() {
				fmt.Println(color.RedString(
					"NEGATIVE CONTROL FAILED: this instance PASSED a check it was expected to " +
						"fail. The suite is not detecting the state it exists to detect, so every " +
						"green run of it means nothing."))
				os.Exit(1)
			}
			fmt.Println(color.GreenString(
				"negative control held: the suite failed the instance it was expected to fail."))
			return nil
		}
		if !rep.OK() {
			os.Exit(1)
		}
		return nil
	},
}

func init() {
	rootCmd.AddCommand(haCmd)
	haCmd.AddCommand(haVerifyCmd)

	haVerifyCmd.Flags().StringVar(&haInstanceId, "instance", "default",
		"instance id (also its namespace)")
	haVerifyCmd.Flags().StringVar(&haKubeContext, "kube-context", "",
		"kubeconfig context (default: current context)")
	haVerifyCmd.Flags().IntVar(&haReplicas, "replicas", 0,
		"override the declared replica factor (default: read from the instance's deployed configuration)")
	haVerifyCmd.Flags().StringVar(&haNatsUrl, "nats-url", "",
		"dial this NATS URL instead of opening a port-forward")
	haVerifyCmd.Flags().DurationVar(&haTimeout, "timeout", 2*time.Minute,
		"bound the whole check")
	haVerifyCmd.Flags().BoolVar(&haExpectFail, "expect-fail", false,
		"invert the exit status: succeed only if the check FAILS (the negative control)")
}
