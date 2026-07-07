// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package sim

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/devicechain-io/dc-event-sources/processor"
)

// EmitInterval is the sim's telemetry cadence (contract: "~5s").
const EmitInterval = 5 * time.Second

// maxIngressResponseBytes bounds how much of an unexpected error body is read.
const maxIngressResponseBytes = 4096

// EmitMeasurement posts one Measurement event for device d over the real
// device-plane HTTP ingress (the same route and JsonEvent shape any physical
// device uses — no sim-only backdoor), authenticated by the credential
// bootstrap.go provisioned for it. It expects HTTP 202 (accepted into the
// pipeline; persistence/resolution happen asynchronously downstream).
func EmitMeasurement(ctx context.Context, rt *Runtime, d DeviceInstance, metricKey string, value float64) error {
	now := time.Now().UTC().Format(time.RFC3339)
	credType := credentialTypeAccessToken
	credId := d.CredentialId

	entry := map[string]any{
		"measurements": map[string]string{
			metricKey: fmt.Sprintf("%v", value),
		},
		"occurredTime": now,
	}
	payload, err := jsonRoundTrip(map[string]any{"entries": []any{entry}})
	if err != nil {
		return fmt.Errorf("build measurement payload: %w", err)
	}

	jevent := processor.JsonEvent{
		Device:         d.Token,
		EventType:      "Measurement",
		OccurredTime:   &now,
		Payload:        payload,
		CredentialType: &credType,
		CredentialId:   &credId,
	}
	body, err := json.Marshal(jevent)
	if err != nil {
		return fmt.Errorf("marshal event: %w", err)
	}

	url := fmt.Sprintf("%s/%s/%s/events", strings.TrimRight(rt.Endpoints.Ingress, "/"), rt.InstanceId, rt.Tenant)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build ingress request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := rt.HTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("post to ingress %s: %w", url, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusAccepted {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, maxIngressResponseBytes))
		return fmt.Errorf("ingress %s returned %d: %s", url, resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	return nil
}

// jsonRoundTrip marshals v and unmarshals it back into a map[string]interface{}
// — JsonEvent.Payload's declared type — so callers can build the payload with
// typed Go literals instead of hand-assembling a map.
func jsonRoundTrip(v any) (map[string]interface{}, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	var out map[string]interface{}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	return out, nil
}
