// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"fmt"

	"github.com/devicechain-io/dc-microservice/blob"
	"github.com/devicechain-io/dc-microservice/config"
)

const (
	// DefaultMaxArtifactBytes is the largest single artifact a tenant may declare.
	DefaultMaxArtifactBytes int64 = 512 << 20 // 512 MiB
	// DefaultMaxTenantArtifactBytes is the most one tenant's artifacts may hold in total
	// (every artifact that has not been hard-deleted, retired ones included: their bytes
	// are still stored).
	DefaultMaxTenantArtifactBytes int64 = 5 << 30 // 5 GiB

	// S3BufferedPutCap is the largest object the S3 backend can store today: its Put
	// buffers the object to hand PutObject a known length, so a larger one would be held
	// in memory. Until the streaming multipart upload lands, an artifact bigger than this
	// is refused at create on that backend rather than at upload time, so a declared
	// size that can never be stored never produces an UPLOADING row that can never finish.
	S3BufferedPutCap int64 = 64 << 20 // 64 MiB
)

// UpdateManagementConfiguration is the typed configuration of the update-management
// service. It is FIXED configuration (a mounted file, rejected at startup when invalid),
// not a dynamic setting.
type UpdateManagementConfiguration struct {
	RdbConfiguration config.MicroserviceDatastoreConfiguration

	// Updates carries the artifact catalogue's ceilings.
	Updates UpdatesConfiguration
}

// UpdatesConfiguration bounds what the artifact catalogue will accept.
type UpdatesConfiguration struct {
	// MaxArtifactBytes caps one artifact's declared size. Zero selects the default; a
	// negative value is refused at startup.
	MaxArtifactBytes int64
	// MaxTenantArtifactBytes caps the summed size of one tenant's artifacts. Zero
	// selects the default; a negative value is refused at startup.
	MaxTenantArtifactBytes int64
}

// NewUpdateManagementConfiguration creates the default update-management configuration.
func NewUpdateManagementConfiguration() *UpdateManagementConfiguration {
	cfg := &UpdateManagementConfiguration{}
	cfg.ApplyDefaults()
	return cfg
}

// ApplyDefaults is the defaulting hook for this service (ADR-022 decision 1).
func (c *UpdateManagementConfiguration) ApplyDefaults() {
	if c.Updates.MaxArtifactBytes == 0 {
		c.Updates.MaxArtifactBytes = DefaultMaxArtifactBytes
	}
	if c.Updates.MaxTenantArtifactBytes == 0 {
		c.Updates.MaxTenantArtifactBytes = DefaultMaxTenantArtifactBytes
	}
}

// Validate is the validation hook for this service (ADR-022 decision 1).
func (c *UpdateManagementConfiguration) Validate() error {
	if c.Updates.MaxArtifactBytes < 0 {
		return fmt.Errorf("updates.maxArtifactBytes must not be negative (got %d)", c.Updates.MaxArtifactBytes)
	}
	if c.Updates.MaxTenantArtifactBytes < 0 {
		return fmt.Errorf("updates.maxTenantArtifactBytes must not be negative (got %d)", c.Updates.MaxTenantArtifactBytes)
	}
	// Checked after defaulting, so a one-sided override cannot slip a per-artifact
	// ceiling above the tenant total (no artifact could then ever be admitted at its
	// declared maximum).
	if c.Updates.MaxArtifactBytes > 0 && c.Updates.MaxTenantArtifactBytes > 0 &&
		c.Updates.MaxArtifactBytes > c.Updates.MaxTenantArtifactBytes {
		return fmt.Errorf("updates.maxArtifactBytes (%d) must not exceed updates.maxTenantArtifactBytes (%d)",
			c.Updates.MaxArtifactBytes, c.Updates.MaxTenantArtifactBytes)
	}
	return nil
}

// EffectiveMaxArtifactBytes is the per-artifact ceiling in force on the given blob
// backend: the configured ceiling, clamped to what the backend can store without
// buffering the object in memory.
func (c *UpdateManagementConfiguration) EffectiveMaxArtifactBytes(backend string) int64 {
	max := c.Updates.MaxArtifactBytes
	if backend == blob.BackendS3 && max > S3BufferedPutCap {
		return S3BufferedPutCap
	}
	return max
}
