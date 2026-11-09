// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"context"
	"fmt"
	"os"

	"github.com/devicechain-io/dcctl/bootstrap"
	"github.com/spf13/cobra"
)

// The selector flags of a command that talks to an instance's platform API.
//
// 🔴 ONE SET, NOT A PER-COMMAND CHOICE. `sim` always took -s/--server AND -i/--instance, while
// the other platform-API commands (events, dead-letters, attr, presence, product,
// tenant-deletion, test, device register) took only --server, so a script written for one failed
// on the next with "unknown shorthand flag". TestEveryInstanceTargetingCommandAcceptsTheSameSelectorFlags
// walks the command tree and holds every command that accepts --server to the full set.
//
// On `sim` --instance keeps its original meaning (the literal event-ingress path segment) and
// is untouched. Everywhere else it is the CONVENIENT spelling of --server: name the instance
// and dcctl uses the host and TLS setting the instance was declared with.

// readRecordedTarget is the cluster read of the host and TLS setting an instance was declared
// with, indirected so selection can be tested without a cluster. ("", false, nil) means the
// declaration holds no host.
var readRecordedTarget = func(ctx context.Context, kubeContext, instance string) (string, bool, error) {
	binding, source := bootstrap.ResolveBinding(bootstrap.Options{Instance: instance, KubeContext: kubeContext})
	if source == bootstrap.BindingUnreadable || source == bootstrap.BindingConflict {
		return "", false, fmt.Errorf("the record of which cluster instance %q lives on cannot be used", instance)
	}
	if source == bootstrap.BindingGuessed {
		fmt.Fprintf(os.Stderr, "note: no record of which cluster instance %q lives on; guessing context %s\n", instance, binding.KubeContext)
	}
	inst, err := bootstrap.ReadInstanceCR(ctx, binding.KubeContext, instance)
	if err != nil {
		return "", false, err
	}
	if inst == nil {
		return "", false, nil
	}
	return inst.Spec.Host, inst.Spec.TLS, nil
}

// addInstanceTargetFlags gives a command that already has --server the instance spelling.
func addInstanceTargetFlags(c *cobra.Command) {
	c.PersistentFlags().StringP("instance", "i", "",
		"instance id: when --server is not given, the host (and TLS setting, unless --tls is given) the instance was declared with is used, read from its declaration on the cluster")
}

// targetFlags settles where a platform-API command sends its calls.
//
// --server wins when given, and --tls wins when given; otherwise --instance reads the
// declaration. Neither given is what it always was (localhost, plain HTTP). An instance whose
// host cannot be read is an error naming --server, never a quiet fall back to localhost: that
// would send an operator's credentials to whatever listens there.
func targetFlags(cmd *cobra.Command) (server string, tls bool, err error) {
	server, _ = cmd.Flags().GetString("server")
	tls, _ = cmd.Flags().GetBool("tls")
	instance, _ := cmd.Flags().GetString("instance")
	if instance == "" || cmd.Flags().Changed("server") {
		return server, tls, nil
	}
	host, declaredTLS, err := readRecordedTarget(cmd.Context(), "", instance)
	if err != nil {
		return "", false, fmt.Errorf("--instance %s: could not read the host the instance was declared with: %w. Pass --server <host> instead", instance, err)
	}
	if host == "" {
		return "", false, fmt.Errorf("--instance %s: no declared host was found for that instance (is it on this machine's cluster?). Pass --server <host> instead", instance)
	}
	if !cmd.Flags().Changed("tls") {
		tls = declaredTLS
	}
	return host, tls, nil
}
