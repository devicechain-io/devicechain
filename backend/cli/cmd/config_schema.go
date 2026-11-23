// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"github.com/devicechain-io/dc-microservice/config"
	"github.com/spf13/cobra"
)

var configSchemaCmd = &cobra.Command{
	Use:   "config-schema",
	Short: "Print this release's instance configuration JSON Schema",
	Long: `Print the embedded JSON Schema for the instance document mounted at
/etc/dci-config/instance. Output is JSON only and works offline. This describes
this binary's build; use the exact target release's dcctl or download
instance-config.schema.json from that GitHub Release. It is not a Helm values
or per-service config schema. Startup additionally checks semantic and
cross-field constraints.`,
	Example: `  dcctl config-schema > instance-config.schema.json`,
	Args:    cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		_, err := cmd.OutOrStdout().Write(config.InstanceJSONSchema())
		return err
	},
}

func init() { rootCmd.AddCommand(configSchemaCmd) }
