// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"

	"github.com/devicechain-io/dc-microservice/auth"

	"github.com/devicechain-io/dc-device-management/model"
)

// DeviceMetricVocabularyResolver exposes the metrics a device's measurements are
// validated against.
type DeviceMetricVocabularyResolver struct {
	M model.MetricVocabulary
}

func (r *DeviceMetricVocabularyResolver) Metrics() []*PublishedMetricResolver {
	result := make([]*PublishedMetricResolver, 0, len(r.M.Metrics))
	for i := range r.M.Metrics {
		result = append(result, &PublishedMetricResolver{M: r.M.Metrics[i]})
	}
	return result
}

// PublishedMetricResolver exposes one metric of a published profile version.
type PublishedMetricResolver struct {
	M model.ResolvedMetric
}

func (r *PublishedMetricResolver) MetricKey() string { return r.M.Key() }

func (r *PublishedMetricResolver) DataType() string { return r.M.DataType() }

// Calculated reports whether the platform computes this metric from a formula, so a
// picker can mark it and a device-facing example can leave it out.
func (r *PublishedMetricResolver) Calculated() bool { return r.M.Calculated() }

func (r *PublishedMetricResolver) Unit() *string {
	if unit, ok := r.M.Unit(); ok {
		return &unit
	}
	return nil
}

func (r *PublishedMetricResolver) MinValue() *float64 {
	if v, ok := r.M.MinValue(); ok {
		return &v
	}
	return nil
}

func (r *PublishedMetricResolver) MaxValue() *float64 {
	if v, ok := r.M.MaxValue(); ok {
		return &v
	}
	return nil
}

func (r *PublishedMetricResolver) AllowedValues() *[]string {
	n := r.M.AllowedValueCount()
	if n == 0 {
		return nil
	}
	allowed := make([]string, n)
	for i := range allowed {
		allowed[i] = r.M.AllowedValue(i)
	}
	return &allowed
}

// DeviceMetricVocabulary lists the metrics a device's measurements are validated
// against, from the same profile resolution ingest reads (model.DeviceMetricVocabulary).
//
// Gated on device:read, like deviceCommandVocabulary: it is profile metadata, which a
// caller who may read the device may already read in its draft form.
//
// An unresolvable device token returns null rather than an error, for the reason
// deviceCommandVocabulary gives: a stale token is a state a client legitimately holds.
func (r *SchemaResolver) DeviceMetricVocabulary(ctx context.Context, args struct {
	DeviceToken string
}) (*DeviceMetricVocabularyResolver, error) {
	if err := auth.Authorize(ctx, auth.DeviceRead); err != nil {
		return nil, err
	}

	vocab, err := r.GetApi(ctx).DeviceMetricVocabulary(ctx, args.DeviceToken)
	if err != nil {
		return nil, err
	}
	if !vocab.DeviceExists {
		return nil, nil
	}
	return &DeviceMetricVocabularyResolver{M: *vocab}, nil
}
