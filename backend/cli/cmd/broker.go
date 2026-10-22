// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/devicechain-io/dc-k8s/functionalarea"
	"github.com/devicechain-io/dc-microservice/natsauth"
	"github.com/devicechain-io/dcctl/bootstrap"
	"github.com/spf13/cobra"
)

var (
	brokerReconcileKubeContext string
	brokerPermissionsInstance  string
	brokerPermissionsProfile  string
	brokerPermissionsAreas    []string
)

// brokerCmd is the parent for the message broker's operator commands.
var brokerCmd = &cobra.Command{
	Use:   "broker",
	Short: "Message broker operations",
	Long:  `Operate on the message broker an instance runs on.`,
}

// brokerPermissionsCmd prints the broker permissions dcctl generates.
var brokerPermissionsCmd = &cobra.Command{
	Use:   "permissions",
	Short: "Print the broker permissions every platform login is held to",
	Long: `Print the message broker permissions of every platform login, as JSON.

An instance dcctl installs never needs this: dcctl generates the same document at install and
upgrade time and gives it to the broker. It exists for an installation that runs the chart
WITHOUT dcctl and brings its own broker, which has to hold each service to the same grants.

The document is generated from the same declarations the services are held to (which streams and
buckets each area writes, reads and administers, and under which consumer names), so it cannot
drift from what a service does. Its shape is:

  {"APP": {"<username>": <permissions>, ...}, "SYS": {"dc_sys": <permissions>}}

where each <permissions> is a nats-server permissions block (publish and subscribe allow and deny
lists, and allow_responses). Merge each block into the user of the same name in the broker's APP
account (and dc_sys's into the system account). Usernames are dc-<area>-g<generation> for an area's
own login, dc-broker-admin-g<generation> for the broker administrator and dc_service for the shared
login an upgraded instance keeps until it is retired.

--profile (or --enable-area) limits the output to the areas that installation deploys. A login with
no block must not be created: nats-server reads a user with no permissions as unrestricted.`,
	Example: `  dcctl broker permissions --instance-id acme > permissions.json
  dcctl broker permissions --instance-id acme --profile telemetry`,
	Args:         cobra.NoArgs,
	RunE:         runBrokerPermissions,
	SilenceUsage: true,
}

// brokerReconcileCmd runs the broker administrator again.
var brokerReconcileCmd = &cobra.Command{
	Use:   "reconcile <instance>",
	Short: "Create and reconcile the instance's message broker streams and buckets",
	Long: `Run the broker administrator again for an installed instance and wait for it.

The platform's services do not create or change the broker's streams and buckets. They verify what
they open and stop, naming what differs, when it is missing or does not match what the release
declares. One login, the broker administrator, creates and reconciles them, as a Job the chart runs
before every install and upgrade. This runs that same Job on demand, for recovery:

  - a pod that stops with "does not match what this build declares" or "does not exist";
  - a stream or bucket lost at runtime or deleted by hand;
  - a ceiling or replica count changed in the instance configuration and applied without a release.

The Job is the one the instance's last install or upgrade ran, read back from its Helm release, so
it uses the same image, the same areas and the same broker login. It is idempotent: on a broker that
already matches it changes nothing. The Job's log is printed. When it fails, the Job is left in the
instance's namespace to read and the command exits non-zero.

A stream that an older release created with a different retention is drained by its reader before
it is recreated as declared, which can take a few minutes.`,
	Example: `  dcctl broker reconcile devicechain
  dcctl broker reconcile devicechain --kube-context kind-devicechain`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return bootstrap.ReconcileBroker(cmd.Context(), bootstrap.ReconcileBrokerOptions{
			Options: bootstrap.Options{Instance: args[0], KubeContext: brokerReconcileKubeContext},
			Out:     cmd.OutOrStdout(),
		})
	},
	SilenceUsage: true,
}

func init() {
	brokerReconcileCmd.Flags().StringVar(&brokerReconcileKubeContext, "kube-context", "", "kube-context to target (default: the cluster recorded at bootstrap; a different context is refused)")
	brokerCmd.AddCommand(brokerReconcileCmd)
	brokerPermissionsCmd.Flags().StringVar(&brokerPermissionsInstance, "instance-id", "", "the instance id the services run under (their DC_INSTANCE_ID); it is part of every subject and stream name (required)")
	brokerPermissionsCmd.Flags().StringVar(&brokerPermissionsProfile, "profile", "", "limit the output to the areas of this deployment profile (default: every area)")
	brokerPermissionsCmd.Flags().StringArrayVar(&brokerPermissionsAreas, "enable-area", nil, "also include this area's login on top of --profile; repeat for more")
	brokerCmd.AddCommand(brokerPermissionsCmd)
	rootCmd.AddCommand(brokerCmd)
}

func runBrokerPermissions(cmd *cobra.Command, _ []string) error {
	if strings.TrimSpace(brokerPermissionsInstance) == "" {
		return fmt.Errorf("--instance-id is required: it is part of every subject and stream name, so there is no default")
	}
	doc, err := brokerPermissionsDocument(brokerPermissionsInstance, brokerPermissionsProfile, brokerPermissionsAreas)
	if err != nil {
		return err
	}
	_, err = cmd.OutOrStdout().Write(doc)
	return err
}

// brokerPermissionsDocument renders the document for an instance and the areas a deployment runs
// (every area when neither profile nor extra areas are given), indented for a reader.
func brokerPermissionsDocument(instance, profile string, extra []string) ([]byte, error) {
	var areas []string
	if strings.TrimSpace(profile) != "" || len(extra) > 0 {
		resolved, err := functionalarea.ResolveEnabled(profile, nil)
		if err != nil {
			return nil, fmt.Errorf("resolving deployment areas (--profile/--enable-area): %w", err)
		}
		for _, a := range extra {
			if a = strings.TrimSpace(a); a != "" {
				resolved = append(resolved, functionalarea.FunctionalArea(a))
			}
		}
		if err := functionalarea.Validate(resolved); err != nil {
			return nil, fmt.Errorf("resolving deployment areas (--profile/--enable-area): %w", err)
		}
		for _, a := range resolved {
			areas = append(areas, string(a))
		}
	}
	var (
		users map[string]map[string]natsauth.Permissions
		err   error
	)
	if areas == nil {
		users, err = natsauth.ServiceUsers(instance)
	} else {
		users, err = natsauth.ServiceUsersFor(instance, areas)
	}
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(users); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
