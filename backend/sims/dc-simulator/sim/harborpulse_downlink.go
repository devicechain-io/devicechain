// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package sim

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/devicechain-io/dc-simulator/lorawan"
)

func (s *harborpulse) pollGaugeQueue(ctx context.Context, rt *Runtime, eui string) ([]lorawan.PendingDownlink, error) {
	endpoint := harborSinkURL() + "/chirpstack/" + url.PathEscape(rt.Tenant) + "/pending/" + eui
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+HarborpulseQueueKey(rt.Tenant))
	resp, err := rt.HTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("poll queue: HTTP %d", resp.StatusCode)
	}
	var pending []lorawan.PendingDownlink
	err = json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&pending)
	return pending, err
}

func (s *harborpulse) answerGaugeQueue(ctx context.Context, rt *Runtime, items []lorawan.PendingDownlink, elapsed time.Duration, at time.Time) error {
	endpoint := strings.TrimRight(rt.Endpoints.Integrations, "/") + "/" + rt.Tenant + "/" + HarborpulseIntegration
	for _, item := range items {
		s.mu.Lock()
		seen := s.s3b.seen[item.ID]
		s.mu.Unlock()
		if seen {
			continue
		}
		raw, err := base64.StdEncoding.Strict().DecodeString(item.Data)
		accepted := err == nil && item.DevEUI == HarborpulseSamplingDevEUI && item.FPort == HarborpulseSamplingPort && bytes.Equal(raw, []byte{0, HarborpulseSamplingSeconds}) && !s.defects.downlinkNack
		// Device action precedes its acknowledgement. A NACK keeps the original sampling cadence.
		if accepted {
			s.mu.Lock()
			s.s3b.interval = HarborpulseSamplingSeconds * time.Second
			s.mu.Unlock()
		}
		for _, report := range lorawan.ReportRequests(endpoint, item, accepted, at) {
			req, err := http.NewRequestWithContext(ctx, http.MethodPost, report.URL, bytes.NewReader(report.Body))
			if err != nil {
				return err
			}
			req.Header.Set("Content-Type", report.ContentType)
			req.Header.Set("Authorization", "Bearer "+rt.IntegrationTokens[HarborpulseIntegration])
			resp, err := rt.HTTPClient.Do(req)
			if err != nil {
				return err
			}
			_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
			resp.Body.Close()
			if resp.StatusCode != http.StatusAccepted {
				return fmt.Errorf("downlink report: HTTP %d", resp.StatusCode)
			}
		}
		s.mu.Lock()
		if s.s3b.seen == nil {
			s.s3b.seen = map[string]bool{}
		}
		s.s3b.seen[item.ID] = true
		s.mu.Unlock()
	}
	return nil
}
