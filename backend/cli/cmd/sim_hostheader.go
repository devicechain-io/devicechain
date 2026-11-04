// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/devicechain-io/dcctl/bootstrap"
	"github.com/devicechain-io/dcctl/sim"
	"github.com/spf13/cobra"
)

// readRecordedHost is the cluster read of the host the instance was declared on,
// indirected so host selection can be tested without one. ("", nil) means there is
// nothing to say.
var readRecordedHost = func(ctx context.Context, kubeContext, instance string) (string, error) {
	binding, source := bootstrap.ResolveBinding(bootstrap.Options{Instance: instance, KubeContext: kubeContext})
	if source == bootstrap.BindingUnreadable || source == bootstrap.BindingConflict {
		return "", fmt.Errorf("the record of which cluster instance %q lives on cannot be used", instance)
	}
	inst, err := bootstrap.ReadInstanceCR(ctx, binding.KubeContext, instance)
	if err != nil || inst == nil {
		return "", err
	}
	return inst.Spec.Host, nil
}

// resolveHostHeader settles the Host header sim calls present to the Gateway: the
// --host-header flag, else (only when --server is an address that could never match a
// listener hostname) the host the instance was declared on. Best effort: when the
// declaration cannot be read it returns "" and the request keeps --server as its
// Host, which is what dcctl did before; withHostHint names the fix if that fails.
func resolveHostHeader(cmd *cobra.Command, server, instance string) string {
	explicit, _ := cmd.Flags().GetString("host-header")
	if strings.TrimSpace(explicit) != "" || !sim.NeedsHostHeader(server) {
		return sim.PickHostHeader(server, explicit, "")
	}
	kubeContext, _ := cmd.Flags().GetString("kube-context")
	recorded, err := readRecordedHost(cmd.Context(), kubeContext, instance)
	if err != nil {
		fmt.Fprintf(cmd.ErrOrStderr(), "note: could not read instance %q's host from the cluster (%v); "+
			"calling the Gateway with Host %q\n", instance, err, server)
		return ""
	}
	return sim.PickHostHeader(server, "", recorded)
}

// newSimAdmin builds the admin client, presenting hostHeader as the Host when set.
func newSimAdmin(hostHeader, userGraphQL, adminURL, email, password string) *sim.Admin {
	var httpc *http.Client
	if hostHeader != "" {
		httpc = sim.HostHeaderClient(hostHeader)
	}
	return sim.NewAdminWithClient(httpc, userGraphQL, adminURL, email, password)
}

// withHostHint names the fix when a call that went to an address, with no Host
// override, fails: an instance's Gateway only routes the host it was bootstrapped
// under, and the failure it gives (a 404) does not say so.
func withHostHint(err error, server, hostHeader string) error {
	if err == nil || hostHeader != "" || !sim.NeedsHostHeader(server) {
		return err
	}
	return fmt.Errorf("%w\nhint: --server %q is an address, but the instance Gateway only routes the host it was "+
		"bootstrapped with (devicechain.local unless `dcctl bootstrap --host` said otherwise). Pass "+
		"--host-header <that host>, or use --server <that host> with it resolving to this address", err, server)
}
