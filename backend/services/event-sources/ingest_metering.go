// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"fmt"
	"strings"
)

// IngestMeteringEnv names the switch that selects how the ingest gate meters a message against its
// tenant's ceiling. The chart renders it from eventSources.ingestMetering.
//
// 🔴 It is an environment variable and deliberately NOT a key of the instance configuration. The
// configuration document is decoded strictly, so a key an older pod does not know crashes that pod,
// and a rolling upgrade restarts pods one at a time against a document the not-yet-upgraded ones
// still read. An environment variable is invisible to them.
const IngestMeteringEnv = "EVENT_SOURCES_INGEST_METERING"

// IngestMetering is the gate's metering mode.
type IngestMetering string

const (
	// IngestMeteringLegacy is the gate as it has always run: an age cutoff routes a message to the live
	// bucket at now or to the backlog bucket at its append stamp. The default.
	IngestMeteringLegacy IngestMetering = "legacy"
	// IngestMeteringLagCredit meters fresh traffic at pod time in a bucket whose capacity carries a
	// platform-measured lag credit, so a compliant tenant is not shed for a platform stall.
	IngestMeteringLagCredit IngestMetering = "lagCredit"
)

// parseIngestMetering reads the switch. Unset or blank is legacy. An unrecognised value is an error:
// a typo must stop the pod, never quietly run the other gate.
func parseIngestMetering(value string) (IngestMetering, error) {
	switch v := IngestMetering(strings.TrimSpace(value)); v {
	case "", IngestMeteringLegacy:
		return IngestMeteringLegacy, nil
	case IngestMeteringLagCredit:
		return IngestMeteringLagCredit, nil
	default:
		return "", fmt.Errorf("%s=%q is not a metering mode; use %q or %q", IngestMeteringEnv, value,
			IngestMeteringLegacy, IngestMeteringLagCredit)
	}
}

// resolveIngestMetering returns the mode the environment asks for.
func resolveIngestMetering(getenv func(string) string) (IngestMetering, error) {
	return parseIngestMetering(getenv(IngestMeteringEnv))
}
