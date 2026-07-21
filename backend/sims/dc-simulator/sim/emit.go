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
	"sync"
	"time"

	"github.com/devicechain-io/dc-event-sources/processor"
)

// maxIngressResponseBytes bounds how much of an unexpected error body is read.
const maxIngressResponseBytes = 4096

// MetricsFunc returns the metrics device d (the i-th in rt.Devices) emits on
// the current tick. It is pure per-device value generation — EmitAll owns the
// wire path, the concurrency, and the accounting.
type MetricsFunc func(i int, d DeviceInstance) map[string]float64

// EmitAll emits one Measurement per device concurrently, bounded to workers,
// and records the result in rt.Stats.
//
// It does NOT stop at the first failure. A load generator that halts a tick
// because one device got a 503 under-applies exactly the load the run is trying
// to measure, and does it most at the moment the platform is most stressed —
// which would bias the measurement toward looking cheaper than it is. Every
// device is attempted; failures are counted and summarized.
func EmitAll(ctx context.Context, rt *Runtime, workers int, metrics MetricsFunc) error {
	devices := rt.Devices
	if len(devices) == 0 {
		return nil
	}
	if workers < 1 {
		workers = 1
	}

	var (
		mu       sync.Mutex
		failed   int
		firstErr error
	)

	idx := make(chan int)
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range idx {
				err := EmitMeasurements(ctx, rt, devices[i], metrics(i, devices[i]))
				if err != nil {
					rt.Stats.Failed.Add(1)
					mu.Lock()
					failed++
					if firstErr == nil {
						firstErr = err
					}
					mu.Unlock()
					continue
				}
				rt.Stats.Emitted.Add(1)
			}
		}()
	}

	for i := range devices {
		select {
		case <-ctx.Done():
			// Stop feeding work; the workers drain and exit on the closed channel.
			close(idx)
			wg.Wait()
			return ctx.Err()
		case idx <- i:
		}
	}
	close(idx)
	wg.Wait()

	if failed > 0 {
		return fmt.Errorf("%d of %d emits failed (first: %w)", failed, len(devices), firstErr)
	}
	return nil
}

// EmitMeasurement posts one Measurement event carrying a single metric. It
// delegates to EmitMeasurements so devicepulse (one metric per device) and
// buildingpulse (four metrics in one event) share the exact same wire path.
func EmitMeasurement(ctx context.Context, rt *Runtime, d DeviceInstance, metricKey string, value float64) error {
	return EmitMeasurements(ctx, rt, d, map[string]float64{metricKey: value})
}

// EmitMeasurements posts one Measurement event for device d carrying every
// entry in metrics as a single measurements map — the "rich emit" shape
// (multiple metric keys in one entries[0].measurements object) rather than one
// event per metric. It uses the same real device-plane HTTP ingress route and
// JsonEvent shape any physical device uses (no sim-only backdoor),
// authenticated by the credential bootstrap.go provisioned for d. It expects
// HTTP 202 (accepted into the pipeline; persistence/resolution happen
// asynchronously downstream).
func EmitMeasurements(ctx context.Context, rt *Runtime, d DeviceInstance, metrics map[string]float64) error {
	// Sub-second precision is load-bearing, not cosmetic. The pipeline dedups on
	// the natural key (tenant, device, event_type, occurred_time), so two emits
	// from one device within the same wall-clock SECOND collapse to a single
	// persisted event. At the demo's 5s cadence that never bites, but a load run
	// emitting faster than 1/device/sec would see the platform correctly dedup
	// its second-identical events — indistinguishable from a drop to a
	// count-reconciling oracle. RFC3339Nano stamps every emit distinctly, so a
	// device may emit at any rate and each reading is its own event (which is
	// also just true — a device sampling sub-second carries sub-second times).
	now := time.Now().UTC().Format(time.RFC3339Nano)
	credType := credentialTypeAccessToken
	credId := d.CredentialId

	values := make(map[string]string, len(metrics))
	for key, value := range metrics {
		values[key] = fmt.Sprintf("%v", value)
	}
	entry := map[string]any{
		"measurements": values,
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
