// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"encoding/json"
	"fmt"
	"io"
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
	haLeadersJSON bool
	haJSON        bool
)

// haReport is what verify and verify-db share: a verdict and a human rendering.
type haReport interface {
	OK() bool
	Format() string
}

// concludeHaReport prints a verify report and returns the process exit code.
//
// With asJSON the report is the ONLY thing written to out, so a script can pipe stdout
// straight into a parser; the negative-control sentence moves to errOut, as it does for
// `ha leaders --json`. --expect-fail inverts the status identically in both modes: a
// check that passes when it was expected to fail is still a red control.
func concludeHaReport(out, errOut io.Writer, rep haReport, asJSON, expectFail bool, failedControl, heldControl string) int {
	msgOut := out
	if asJSON {
		raw, err := json.MarshalIndent(rep, "", "  ")
		if err != nil {
			fmt.Fprintln(errOut, "could not encode the report:", err)
			return 1
		}
		fmt.Fprintln(out, string(raw))
		msgOut = errOut
	} else {
		fmt.Fprint(out, rep.Format())
	}
	if expectFail {
		if rep.OK() {
			fmt.Fprintln(msgOut, color.RedString("NEGATIVE CONTROL FAILED: "+failedControl+
				". The suite is not detecting the state it exists to detect, so every "+
				"green run of it means nothing."))
			return 1
		}
		fmt.Fprintln(msgOut, color.GreenString("negative control held: "+heldControl))
		return 0
	}
	if !rep.OK() {
		return 1
	}
	return 0
}

var haCmd = &cobra.Command{
	Use:   "ha",
	Short: "Inspect an instance's high-availability posture",
}

var haVerifyCmd = &cobra.Command{
	Use:   "verify",
	Short: "Assert an instance's broker actually holds the replication it declares",
	Long: `Assert, from live broker state, that an instance's JetStream streams, KV
buckets, durable consumers and NATS pods are replicated the way the instance
declares.

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

Use --json to print the report as JSON (the only thing written to stdout). Use
--expect-fail to invert that, for the negative control. A check suite that
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
		if code := concludeHaReport(os.Stdout, os.Stderr, rep, haJSON, haExpectFail,
			"this instance PASSED a check it was expected to fail",
			"the suite failed the instance it was expected to fail."); code != 0 {
			os.Exit(code)
		}
		return nil
	},
}

var haLeadersCmd = &cobra.Command{
	Use:   "leaders",
	Short: "Report where the broker's per-event stream leaders sit, and whether they could be separated further",
	Long: `Report, from live broker state, which broker server leads each stream every device event passes
through (the capture stream, the inbound stream, the resolved stream and the broker's MQTT message
store), which server leads each consumer of the resolved stream, and how many streams and consumers
each server leads.

Each of those streams costs its leader a replicated write per event. When one server leads several
of them it becomes the broker's bottleneck long before the others are busy, and nothing in the
deployment shows it.

The verdict is a dry run of ` + "`dcctl broker rebalance`" + `, under the same rules, and moves nothing:

  PASS       no leader move the rebalance may make would separate them further
  FAIL       it would move the resolved stream or one of its consumers to a server leading fewer
             of these streams; the moves are listed. ` + "`dcctl broker rebalance`" + ` makes them
  UNSETTLED  one of the instance's per-event streams or a leader is missing, or a replica is
             offline or behind where the rebalance would wait on it, so it cannot be judged now

The capture stream, the inbound stream and the MQTT message store are never moved: a leader change
on them can lose messages already acknowledged to a device. When two of them share a server, the
report says so; it is not a failure.

Exit status is the result: 0 on PASS, 1 otherwise. A failure to read the broker is an error, never a
verdict. --json prints the report as JSON. --expect-fail inverts the status for the negative
control, and succeeds only on FAIL (never on UNSETTLED).

  dcctl ha leaders --instance default`,
	RunE: func(cmd *cobra.Command, args []string) error {
		rep, err := bootstrap.InspectLeadership(cmd.Context(), bootstrap.LeadersOptions{
			KubeContext: haKubeContext,
			InstanceId:  haInstanceId,
			NatsUrl:     haNatsUrl,
			Settle:      haSettle,
			Timeout:     haTimeout,
		})
		if err != nil {
			return err
		}
		if haLeadersJSON {
			raw, err := json.MarshalIndent(rep, "", "  ")
			if err != nil {
				return err
			}
			fmt.Println(string(raw))
		} else {
			fmt.Print(rep.Format())
		}
		if haExpectFail {
			if rep.Verdict != bootstrap.LeadersFail {
				fmt.Fprintln(os.Stderr, color.RedString(
					"NEGATIVE CONTROL FAILED: the check reported %s on a broker it was expected to FAIL.", rep.Verdict))
				os.Exit(1)
			}
			fmt.Fprintln(os.Stderr, color.GreenString("negative control held: the check failed the broker it was expected to fail."))
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
	dbDurability   string
	dbTimescale    bool
)

var haVerifyDbCmd = &cobra.Command{
	Use:   "verify-db",
	Short: "Assert a database store actually holds the replication it declares",
	Long: `Assert, from live PostgreSQL and Kubernetes state, that a CloudNativePG
database store is replicated the way it is expected to be.

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

Exit status is the result: 0 when every assertion holds, 1 when any does not.

  dcctl ha verify-db --cluster dc-rdb --require-synchronous

Use --json to print the report as JSON (the only thing written to stdout). Use
--expect-fail for the negative control, stating the topology the store is
expected NOT to hold:

  dcctl ha verify-db --cluster dc-rdb --instances 3 --require-synchronous --expect-fail`,
	RunE: func(cmd *cobra.Command, args []string) error {
		// A stated expectation that is never tested is the failure this whole
		// command is about, so refuse the combination rather than ignoring it.
		// --durability is only consulted under --require-synchronous, so setting
		// it alone produces a green report that never checked the thing the
		// caller asked about.
		if dbDurability != "" && !dbRequireSync {
			return fmt.Errorf("--durability %q was given without --require-synchronous, "+
				"and the durability check only runs under it — so this would have "+
				"reported success without testing what you asked about. Add "+
				"--require-synchronous, or drop --durability", dbDurability)
		}

		rep, err := bootstrap.VerifyDatabaseReplication(cmd.Context(), bootstrap.DbVerifyOptions{
			KubeContext:        haKubeContext,
			Namespace:          dbNamespace,
			ClusterName:        dbClusterName,
			AliasService:       dbAliasService,
			Instances:          dbInstances,
			RequireSynchronous: dbRequireSync,
			Durability:         dbDurability,
			TimescaleJobs:      dbTimescale,
			Settle:             haSettle,
			Timeout:            haTimeout,
		})
		if err != nil {
			// A collection error is NOT a verdict, for the same reason it is not one
			// in the broker check: "we could not reach the database" must never be
			// recorded as either answer.
			return err
		}
		if code := concludeHaReport(os.Stdout, os.Stderr, rep, haJSON, haExpectFail,
			"this store PASSED a check it was expected to fail",
			"the suite failed the store it was expected to fail."); code != 0 {
			os.Exit(code)
		}
		return nil
	},
}

func init() {
	rootCmd.AddCommand(haCmd)
	haCmd.AddCommand(haVerifyCmd)
	haCmd.AddCommand(haVerifyDbCmd)
	haCmd.AddCommand(haLeadersCmd)

	haLeadersCmd.Flags().StringVar(&haInstanceId, "instance", "default",
		"instance id; its services and broker run in namespace dci-<instance>")
	haLeadersCmd.Flags().StringVar(&haKubeContext, "kube-context", "",
		"kubeconfig context (default: current context)")
	haLeadersCmd.Flags().StringVar(&haNatsUrl, "nats-url", "",
		"dial this NATS URL instead of opening a port-forward")
	haLeadersCmd.Flags().DurationVar(&haSettle, "settle", 30*time.Second,
		"keep re-reading for this long while the broker is UNSETTLED (an election under way, a replica "+
			"behind). A FAIL is reported at once. 0 disables it")
	haLeadersCmd.Flags().DurationVar(&haTimeout, "timeout", 2*time.Minute,
		"bound the whole check")
	haLeadersCmd.Flags().BoolVar(&haExpectFail, "expect-fail", false,
		"invert the exit status: succeed only if the check FAILS (the negative control)")
	haLeadersCmd.Flags().BoolVar(&haLeadersJSON, "json", false,
		"print the report as JSON")

	haVerifyDbCmd.Flags().StringVar(&haKubeContext, "kube-context", "",
		"kubeconfig context (default: current context)")
	haVerifyDbCmd.Flags().StringVar(&dbNamespace, "namespace", "dc-system",
		"namespace holding the CloudNativePG Cluster: dc-system for the shared relational store (dc-rdb), dci-<instance> for that instance's event store (dc-tsdb)")
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
	haVerifyDbCmd.Flags().StringVar(&dbDurability, "durability", "",
		"the dataDurability this store is expected to declare, checked only with "+
			"--require-synchronous. Empty means \"required\". The event store runs "+
			"\"preferred\" so a lost standby degrades it to asynchronous replication "+
			"instead of applying backpressure to ingest, so checking it against "+
			"\"required\" would report a correct configuration as broken")
	haVerifyDbCmd.Flags().BoolVar(&dbTimescale, "timescale-jobs", false,
		"also assert TimescaleDB background-job health across every database on the "+
			"server. Continuous aggregates, retention and compression are all background "+
			"jobs: if the scheduler stops, the database stays up, replicates, fails over "+
			"and passes every other check here while silently no longer aggregating")
	haVerifyDbCmd.Flags().DurationVar(&haSettle, "settle", 90*time.Second,
		"keep re-checking for this long while assertions fail, for the interval in which "+
			"a failover is completing or a standby rejoining. Never turns a failure into a "+
			"pass: on expiry the last assertion report is returned in full (if the last attempt could not collect state at all, that collection error is returned instead). 0 disables it")
	haVerifyDbCmd.Flags().DurationVar(&haTimeout, "timeout", 5*time.Minute,
		"bound the whole check")
	haVerifyDbCmd.Flags().BoolVar(&haExpectFail, "expect-fail", false,
		"invert the exit status: succeed only if the check FAILS (the negative control)")
	haVerifyDbCmd.Flags().BoolVar(&haJSON, "json", false,
		"print the report as JSON")

	haVerifyCmd.Flags().StringVar(&haInstanceId, "instance", "default",
		"instance id; its services and broker run in namespace dci-<instance>")
	haVerifyCmd.Flags().StringVar(&haKubeContext, "kube-context", "",
		"kubeconfig context (default: current context)")
	haVerifyCmd.Flags().IntVar(&haReplicas, "replicas", 0,
		"override the declared replica factor (default: read from the instance's deployed configuration)")
	haVerifyCmd.Flags().StringVar(&haNatsUrl, "nats-url", "",
		"dial this NATS URL instead of opening a port-forward")
	haVerifyCmd.Flags().DurationVar(&haSettle, "settle", 90*time.Second,
		"keep re-checking for this long while assertions fail, for the interval in which a "+
			"RAFT peer set is reconfiguring or a consumer group is remapping. Never turns a "+
			"failure into a pass: on expiry the last assertion report is returned in full (if the last attempt could not collect state at all, that collection error is returned instead). 0 disables it")
	haVerifyCmd.Flags().DurationVar(&haTimeout, "timeout", 5*time.Minute,
		"bound the whole check")
	haVerifyCmd.Flags().BoolVar(&haExpectFail, "expect-fail", false,
		"invert the exit status: succeed only if the check FAILS (the negative control)")
	haVerifyCmd.Flags().BoolVar(&haJSON, "json", false,
		"print the report as JSON")
	haVerifyCmd.Flags().BoolVar(&haProbeMqtt, "probe-mqtt", false,
		"open one MQTT connection first so the broker's $MQTT_* streams exist and their "+
			"replica factor can be observed. MUTATES the broker (it creates those streams "+
			"if no client has ever connected), so it is off by default")
}
