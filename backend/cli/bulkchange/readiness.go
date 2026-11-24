// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package bulkchange implements the Preview bulk change readiness client.
package bulkchange

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

type Client interface {
	Query(context.Context, string, string, map[string]any, any) error
}

const readinessQuery = `query { fleetOperationReadiness: bulkChangeReadiness { protocolVersion enforcement executableKinds orphanedAttributeRows } }`
const legacyReadinessQuery = `query { fleetOperationReadiness { protocolVersion enforcement executableKinds orphanedAttributeRows } }`

type Readiness struct {
	ProtocolVersion int      `json:"protocolVersion"`
	Enforcement     string   `json:"enforcement"`
	ExecutableKinds []string `json:"executableKinds"`
	// OrphanedAttributeRows is nil when the server could not count them.
	OrphanedAttributeRows *int `json:"orphanedAttributeRows"`
}

// RunReadiness prints the four facts before returning a non-ready refusal. The
// ordinary command error path exits non-zero, including for unknown future states.
func RunReadiness(ctx context.Context, client Client, endpoint string, w io.Writer, asJSON bool) error {
	return runReadiness(ctx, client, endpoint, w, asJSON, readinessQuery)
}

// RunLegacyReadiness preserves the v1.9 query for the deprecated command spelling.
func RunLegacyReadiness(ctx context.Context, client Client, endpoint string, w io.Writer, asJSON bool) error {
	return runReadiness(ctx, client, endpoint, w, asJSON, legacyReadinessQuery)
}

func runReadiness(ctx context.Context, client Client, endpoint string, w io.Writer, asJSON bool, query string) error {
	var out struct {
		Readiness *Readiness `json:"fleetOperationReadiness"`
	}
	if err := client.Query(ctx, endpoint, query, nil, &out); err != nil {
		return err
	}
	if out.Readiness == nil {
		return errors.New("bulk change readiness response is missing")
	}
	ready := out.Readiness
	if asJSON {
		encoder := json.NewEncoder(w)
		encoder.SetIndent("", "  ")
		if err := encoder.Encode(ready); err != nil {
			return err
		}
	} else {
		orphans := "unknown (could not be counted)"
		if ready.OrphanedAttributeRows != nil {
			orphans = fmt.Sprint(*ready.OrphanedAttributeRows)
		}
		if _, err := fmt.Fprintf(w, "protocolVersion: %d\nenforcement: %s\nexecutableKinds: %v\norphanedAttributeRows: %s\n",
			ready.ProtocolVersion, ready.Enforcement, ready.ExecutableKinds, orphans); err != nil {
			return err
		}
	}
	if ready.Enforcement != "ENFORCED" || ready.OrphanedAttributeRows == nil || *ready.OrphanedAttributeRows != 0 {
		if ready.Enforcement == "NOT_INSTALLED" {
			return errors.New("bulk changes are not ready: enforcement is not installed; this is expected until the release that installs enforcement")
		}
		return errors.New("bulk changes are not ready: enforcement must be ENFORCED and orphanedAttributeRows must be zero")
	}
	return nil
}
