// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"context"
	"fmt"
	"os"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dcctl/bootstrap"
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
scoped identity and the tenant back down.

The superuser password comes from --admin-password, else $` + adminPasswordEnv + `, else the
instance's own Secret on the cluster (read through --kube-context, or the context the
instance was bootstrapped on).`,
}

// adminPasswordEnv supplies the superuser password without putting it on a command
// line, where every process listing would show it.
const adminPasswordEnv = "DC_ADMIN_PASSWORD"

func init() {
	simCmd.PersistentFlags().StringP("server", "s", "localhost", "instance host for platform API calls")
	simCmd.PersistentFlags().StringP("instance", "i", "devicechain", "instance id (the literal event-ingress path segment; must match the deployed instance.id)")
	simCmd.PersistentFlags().String("admin-email", auth.DefaultSuperuserEmail, "superuser identity that mints the scoped sim identity")
	// 🔴 NO DEFAULT. It used to be the literal every instance was seeded with; dcctl now
	// generates the password per instance, so the only true default is that instance's
	// own Secret — see resolveAdminPassword.
	simCmd.PersistentFlags().String("admin-password", "", "superuser password (default: $"+adminPasswordEnv+", else read from the instance's Secret on the cluster)")
	simCmd.PersistentFlags().String("kube-context", "", "kube-context to read the superuser password from when none is given (default: the one the instance was bootstrapped on)")
	simCmd.PersistentFlags().Bool("tls", false, "use https/wss for platform endpoints")
	simCmd.PersistentFlags().String("control-addr", "http://localhost:8090", "the dc-simulator process's control API address")

	rootCmd.AddCommand(simCmd)
}

// readSuperuserPassword is the cluster read, indirected so the resolution order can be
// tested without one.
var readSuperuserPassword = func(ctx context.Context, kubeContext, instance string) (string, error) {
	binding, source := bootstrap.ResolveBinding(bootstrap.Options{Instance: instance, KubeContext: kubeContext})
	if source == bootstrap.BindingUnreadable {
		return "", fmt.Errorf("the record of which cluster instance %q lives on could not be read; pass --kube-context", instance)
	}
	return bootstrap.ReadSuperuserPassword(ctx, binding.KubeContext, instance)
}

// resolveAdminPassword settles the superuser password a sim command signs in with:
// the flag, else the environment, else the instance's generated Secret.
//
// It never returns an empty password. A failure to read the Secret names both ways of
// supplying the password instead, and never echoes a value.
func resolveAdminPassword(cmd *cobra.Command, instance string) (string, error) {
	if pw, _ := cmd.Flags().GetString("admin-password"); pw != "" {
		return pw, nil
	}
	if pw := os.Getenv(adminPasswordEnv); pw != "" {
		return pw, nil
	}
	kubeContext, _ := cmd.Flags().GetString("kube-context")
	pw, err := readSuperuserPassword(cmd.Context(), kubeContext, instance)
	if err != nil {
		return "", fmt.Errorf("no superuser password was given (--admin-password or $%s) and it could not be "+
			"read from the cluster: %w", adminPasswordEnv, err)
	}
	return pw, nil
}
