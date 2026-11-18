// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package manageropts

import (
	"flag"

	ctrl "sigs.k8s.io/controller-runtime"
)

// BindManagerFlags constructs manager options and binds startup flags to them.
// Parse the flag set before passing the returned options to a manager.
func BindManagerFlags(flags *flag.FlagSet) *ctrl.Options {
	opts := &ctrl.Options{
		LeaderElectionID: "27be0819.devicechain.io",
		// Hand the lease over on a clean shutdown so a rolling update's new pod does
		// not wait out the old lease. Safe because main exits as soon as the manager
		// returns; nothing runs after the lease is released.
		LeaderElectionReleaseOnCancel: true,
	}
	flags.StringVar(&opts.Metrics.BindAddress, "metrics-bind-address", ":8080", "The address the metric endpoint binds to.")
	flags.StringVar(&opts.HealthProbeBindAddress, "health-probe-bind-address", ":8081", "The address the probe endpoint binds to.")
	flags.BoolVar(&opts.LeaderElection, "leader-elect", false, "Enable leader election for the controller manager.")
	return opts
}
