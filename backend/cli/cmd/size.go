// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"fmt"
	"strings"

	"github.com/devicechain-io/dcctl/bootstrap"
)

// sizeUsage is --size on dcctl bootstrap.
var sizeUsage = "size preset for this instance: " + strings.Join(bootstrap.SizeNames(), ", ") +
	". base (the default) is the standard sizing; perf-12k is the measured high-throughput shape " +
	"(one cluster without --ha, 12,000 events a second with event envelopes on) and needs a " +
	"multi-node cluster; compact is the cluster's own setting (dcctl install --size compact), " +
	"accepted here only on a compact cluster. Recorded on the instance and re-applied by every upgrade. " +
	"A --values overlay, and an OpenTofu variable you state yourself, win over the preset"

// sizeInstallUsage is --size on dcctl install: the cluster's size, which today is base or compact.
var sizeInstallUsage = "cluster size: base (the default) or compact, which is the same as --compact. " +
	"The perf sizes are chosen per instance with dcctl bootstrap --size"

// resolveInstallSize folds `--size` into the --compact flag, which stays a permanent alias: compact
// is true for either spelling, and a --size that contradicts --compact is refused.
func resolveInstallSize(compact, compactGiven bool, size string) (bool, error) {
	if err := bootstrap.ValidateSize(size); err != nil {
		return false, fmt.Errorf("--size: %w", err)
	}
	switch size {
	case "", bootstrap.SizeBase:
		if compact && size == bootstrap.SizeBase {
			return false, fmt.Errorf("--compact and --size base contradict each other")
		}
		return compact, nil
	case bootstrap.SizeCompact:
		if compactGiven && !compact {
			return false, fmt.Errorf("--compact=false and --size compact contradict each other")
		}
		return true, nil
	default:
		return false, fmt.Errorf("--size %s is chosen per instance: run `dcctl bootstrap --size %s`. "+
			"dcctl install takes %s or %s", size, size, bootstrap.SizeBase, bootstrap.SizeCompact)
	}
}
