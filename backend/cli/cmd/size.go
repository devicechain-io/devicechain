// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"strings"

	"github.com/devicechain-io/dcctl/bootstrap"
)

// sizeUsage is --size on dcctl bootstrap.
var sizeUsage = "size preset for this instance: " + strings.Join(bootstrap.SizeNames(), ", ") +
	". base (the default) is the standard sizing; perf-12k and perf-16k are the measured high-throughput shapes " +
	"(one cluster without --ha, 12,000 and 16,000 events a second with event envelopes on) and need a " +
	"multi-node cluster; perf-20k targets 20,000 events a second on the same shape with more broker CPU " +
	"(throughput sizing, latency unvalidated); compact is the cluster's own setting (dcctl install --size compact), " +
	"accepted here only on a compact cluster. Recorded on the instance and re-applied by every upgrade. " +
	"A --values overlay, and an OpenTofu variable you state yourself, win over the preset"

// sizeInstallUsage is --size on dcctl install: the cluster's size, which today is base or compact.
var sizeInstallUsage = "cluster size: base (the default) or compact, which is the same as --compact. " +
	"The perf sizes are chosen per instance with dcctl bootstrap --size"

// shardingUsage is --sharding's help.
var shardingUsage = "sharding level for this instance: " + strings.Join(bootstrap.ShardingNames(), ", ") +
	". off (the default) is one unsharded event path. scale splits inbound and resolved events into three " +
	"groups, one per broker server, and is not yet available: it requires the r34 qualification. Chosen once, at " +
	"install, recorded on the instance and never changed. Leave it out to install a raw shardGroups block from " +
	"--values (a custom configuration)"
