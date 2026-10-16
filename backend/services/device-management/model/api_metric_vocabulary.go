// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import "context"

// MetricVocabulary is what a device's measurements are validated against when they
// arrive: the metrics of its type's profile's active PUBLISHED version.
type MetricVocabulary struct {
	// DeviceExists reports whether the token resolved to a live device. False makes
	// Metrics meaningless.
	DeviceExists bool
	// Metrics is the published vocabulary, empty when the device's type has no profile,
	// the profile was never published, or the version declares no metric. Empty does not
	// mean the device may send nothing: an undeclared key is accepted unvalidated.
	Metrics []ResolvedMetric
}

// DeviceMetricVocabulary resolves device → type → the profile's active published metrics.
//
// It reads ProfileResolutionByDeviceType, the value the resolver validates every inbound
// measurement against (validateMeasurements) and stamps it from, so what a client is told
// a device may send and what ingest then accepts are one reading. The console's
// connectivity helper builds its example payload from this, and an example built from the
// draft instead would be refused the moment a draft edit had not been published.
func (api *Api) DeviceMetricVocabulary(ctx context.Context, deviceToken string) (*MetricVocabulary, error) {
	devices, err := api.DevicesByToken(ctx, []string{deviceToken})
	if err != nil {
		return nil, err
	}
	if len(devices) == 0 {
		return &MetricVocabulary{DeviceExists: false}, nil
	}
	resolution, err := api.ProfileResolutionByDeviceType(ctx, devices[0].DeviceTypeId)
	if err != nil {
		return nil, err
	}
	metrics := make([]ResolvedMetric, resolution.MetricCount())
	for i := range metrics {
		metrics[i] = resolution.MetricAt(i)
	}
	return &MetricVocabulary{DeviceExists: true, Metrics: metrics}, nil
}
