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
	haProbeMqtt   bool
	haSettle      time.Duration
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
			ProbeMqtt:   haProbeMqtt,
			Settle:      haSettle,
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

var (
	dbNamespace    string
	dbClusterName  string
	dbAliasService string
	dbInstances    int
	dbRequireSync  bool
)

var haVerifyDbCmd = &cobra.Command{
	Use:   "verify-db",
	Short: "Assert a database store actually holds the replication it declares",
	Long: `Assert, from live PostgreSQL and Kubernetes state, that a CloudNativePG
database store is replicated the way it is expected to be (ADR-020 A2.3).

This is the database sibling of "ha verify", and it exists for the same reason:
every artifact that describes the store describes what was ASKED for. A Cluster
with three ready pods, a green apply and a healthy operator can still be
replicating asynchronously -- Helm accepts a misspelled field and the Kubernetes
API server prunes it silently, so a one-character slip in a chart template
removes synchronous replication without removing anything visible.

So the checks run against the running server:

  the instance count and pod placement  <- the Kubernetes API
  the alias Service and its endpoints   <- the Kubernetes API
  synchronous_standby_names             <- PostgreSQL
  connected + synchronous standbys      <- PostgreSQL
  the app role's CREATEDB attribute     <- PostgreSQL

The last one is not about replication at all, and it is here because its failure
is invisible from the database: CloudNativePG's application role has no CREATEDB
by default, and every DeviceChain service issues CREATE DATABASE at startup. A
store missing that grant looks perfectly healthy while nothing can start.

Exit status is the result: 0 when every assertion holds, 1 when any does not.

  dcctl ha verify-db --cluster dc-rdb --require-synchronous

Use --expect-fail for the negative control, stating the topology the store is
expected NOT to hold:

  dcctl ha verify-db --cluster dc-rdb --instances 3 --require-synchronous --expect-fail`,
	RunE: func(cmd *cobra.Command, args []string) error {
		rep, err := bootstrap.VerifyDatabaseReplication(cmd.Context(), bootstrap.DbVerifyOptions{
			KubeContext:        haKubeContext,
			Namespace:          dbNamespace,
			ClusterName:        dbClusterName,
			AliasService:       dbAliasService,
			Instances:          dbInstances,
			RequireSynchronous: dbRequireSync,
			Settle:             haSettle,
			Timeout:            haTimeout,
		})
		if err != nil {
			// A collection error is NOT a verdict, for the same reason it is not one
			// in the broker check: "we could not reach the database" must never be
			// recorded as either answer.
			return err
		}
		fmt.Print(rep.Format())

		if haExpectFail {
			if rep.OK() {
				fmt.Println(color.RedString(
					"NEGATIVE CONTROL FAILED: this store PASSED a check it was expected to " +
						"fail. The suite is not detecting the state it exists to detect, so every " +
						"green run of it means nothing."))
				os.Exit(1)
			}
			fmt.Println(color.GreenString(
				"negative control held: the suite failed the store it was expected to fail."))
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
	haCmd.AddCommand(haVerifyDbCmd)

	haVerifyDbCmd.Flags().StringVar(&haKubeContext, "kube-context", "",
		"kubeconfig context (default: current context)")
	haVerifyDbCmd.Flags().StringVar(&dbNamespace, "namespace", "dc-system",
		"namespace holding the CloudNativePG Cluster")
	haVerifyDbCmd.Flags().StringVar(&dbClusterName, "cluster", "dc-rdb",
		"the CloudNativePG Cluster object to check")
	haVerifyDbCmd.Flags().StringVar(&dbAliasService, "alias-service", "dc-postgresql",
		"the Service name clients use, whose selector must be the operator-maintained "+
			"primary alias; empty skips that check (and says so in the report)")
	haVerifyDbCmd.Flags().IntVar(&dbInstances, "instances", 0,
		"expected instance count (default: read from the live Cluster spec). State it "+
			"explicitly for the negative control — read from the spec, a single-instance "+
			"store expects one instance and duly has one, so it can never fail")
	haVerifyDbCmd.Flags().BoolVar(&dbRequireSync, "require-synchronous", false,
		"demand synchronous replication. Deliberately NOT derived from the Cluster spec: "+
			"a pruned or misspelled field leaves a spec that asks for nothing, which a "+
			"spec-derived check would happily confirm")
	haVerifyDbCmd.Flags().DurationVar(&haSettle, "settle", 90*time.Second,
		"keep re-checking for this long while assertions fail, for the interval in which "+
			"a failover is completing or a standby rejoining. Never turns a failure into a "+
			"pass: on expiry the LAST report is returned in full. 0 disables it")
	haVerifyDbCmd.Flags().DurationVar(&haTimeout, "timeout", 5*time.Minute,
		"bound the whole check")
	haVerifyDbCmd.Flags().BoolVar(&haExpectFail, "expect-fail", false,
		"invert the exit status: succeed only if the check FAILS (the negative control)")

	haVerifyCmd.Flags().StringVar(&haInstanceId, "instance", "default",
		"instance id (also its namespace)")
	haVerifyCmd.Flags().StringVar(&haKubeContext, "kube-context", "",
		"kubeconfig context (default: current context)")
	haVerifyCmd.Flags().IntVar(&haReplicas, "replicas", 0,
		"override the declared replica factor (default: read from the instance's deployed configuration)")
	haVerifyCmd.Flags().StringVar(&haNatsUrl, "nats-url", "",
		"dial this NATS URL instead of opening a port-forward")
	haVerifyCmd.Flags().DurationVar(&haSettle, "settle", 90*time.Second,
		"keep re-checking for this long while assertions fail, for the interval in which a "+
			"RAFT peer set is reconfiguring or a consumer group is remapping. Never turns a "+
			"failure into a pass: on expiry the LAST report is returned in full. 0 disables it")
	haVerifyCmd.Flags().DurationVar(&haTimeout, "timeout", 5*time.Minute,
		"bound the whole check")
	haVerifyCmd.Flags().BoolVar(&haExpectFail, "expect-fail", false,
		"invert the exit status: succeed only if the check FAILS (the negative control)")
	haVerifyCmd.Flags().BoolVar(&haProbeMqtt, "probe-mqtt", false,
		"open one MQTT connection first so the broker's $MQTT_* streams exist and their "+
			"replica factor can be observed. MUTATES the broker (it creates those streams "+
			"if no client has ever connected), so it is off by default")
}
