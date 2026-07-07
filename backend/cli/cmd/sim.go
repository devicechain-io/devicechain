// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"github.com/spf13/cobra"
)

// simCmd is the parent command for creating and driving DeviceChain simulations
// (ADR-035). dcctl mints each sim a scoped identity via the instance admin surface
// and then drives its lifecycle through the sim process's own control API — dcctl
// is the only admin caller; the sim runner never touches /admin/graphql.
var simCmd = &cobra.Command{
	Use:   "sim",
	Short: "Create and drive DeviceChain simulations",
	Long: `Create and drive standalone DeviceChain simulations (ADR-035).

'sim create' mints a scoped per-sim identity + tenant on the instance and writes a
handshake file the dc-simulator process reads to come up. 'sim start/stop/status'
drive an already-running sim through its control API; 'sim destroy' tears the
scoped identity (and, with --purge, the tenant) back down.`,
}

func init() {
	simCmd.PersistentFlags().StringP("server", "s", "localhost", "instance host for platform API calls")
	simCmd.PersistentFlags().StringP("instance", "i", "devicechain", "instance id (the literal event-ingress path segment; must match the deployed instance.id)")
	simCmd.PersistentFlags().String("admin-email", "superuser@devicechain.local", "superuser identity that mints the scoped sim identity")
	simCmd.PersistentFlags().String("admin-password", "devicechain", "superuser password")
	simCmd.PersistentFlags().Bool("tls", false, "use https/wss for platform endpoints")
	simCmd.PersistentFlags().String("control-addr", "http://localhost:8090", "the dc-simulator process's control API address")

	rootCmd.AddCommand(simCmd)
}
