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
	"github.com/spf13/cobra"
)

var (
	brokerPermissionsInstance string
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

func init() {
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
