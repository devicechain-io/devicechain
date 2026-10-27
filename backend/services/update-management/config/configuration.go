// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"fmt"
	"net"
	"net/url"
	"strings"
	"time"

	"github.com/devicechain-io/dc-microservice/blob"
	"github.com/devicechain-io/dc-microservice/config"
	"github.com/devicechain-io/dc-microservice/ota"
)

const (
	// DefaultMaxArtifactBytes is the largest single artifact a tenant may declare.
	DefaultMaxArtifactBytes int64 = 512 << 20 // 512 MiB
	// DefaultMaxTenantArtifactBytes is the most one tenant's artifacts may hold in total:
	// every artifact that holds or is about to hold bytes (a retired artifact whose content
	// is stored still counts; one retired before any content arrived does not).
	//
	// 2 GiB, deliberately BELOW the chart's default object-store volume (5Gi, shared by every
	// tenant and by branding assets): a per-tenant ceiling equal to the volume lets a single
	// tenant fill it. Size the volume as tenants x this ceiling, plus headroom, or lower the
	// ceiling to fit the volume you have.
	DefaultMaxTenantArtifactBytes int64 = 2 << 30 // 2 GiB
	// DefaultMaxConcurrentUploads is how many artifact uploads one replica streams at a
	// time. Each holds a connection and writes to the object store, so the cap bounds the
	// disk and bandwidth a burst of uploads can take; more are refused 503 before their body
	// is read.
	DefaultMaxConcurrentUploads = 4
	// DefaultMaxConcurrentDownloads is how many artifact downloads one replica streams at a
	// time. Each holds a connection and reads from the object store; more are refused 503
	// before anything is looked up.
	DefaultMaxConcurrentDownloads = 16
	// DefaultCapabilityTTLSeconds and MaxCapabilityTTLSeconds bound how long a download
	// capability lives: fifteen minutes is the ceiling, and the default is the ceiling.
	DefaultCapabilityTTLSeconds = 15 * 60
	MaxCapabilityTTLSeconds     = 15 * 60
	// DefaultSweepIntervalSeconds is how often the deadline sweep looks for stalled attempts.
	DefaultSweepIntervalSeconds = 30

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
	// MaxConcurrentUploads caps the uploads one replica streams at once. Zero selects the
	// default; a negative value is refused at startup.
	MaxConcurrentUploads int

	// AllowUnverifiedArtifacts lets an artifact with no signature by an ACTIVE signer (derived
	// trust UNVERIFIED) be assigned. With this false only a SIGNED artifact can be. The default is false;
	// development and simulator installs set it, and the value is visible in
	// updateManagementInfo so an instance running with it can be seen to be.
	AllowUnverifiedArtifacts bool
	// DownloadBaseURL is the public URL a device reaches this service's HTTP surface at, for
	// example https://dc.example.com/api/update-management, with no query or fragment. A
	// capability URL is this plus /updates/download/{artifact}?cap=... Empty means no
	// capability can be issued, and a device's download request is refused and counted.
	DownloadBaseURL string
	// CapabilityTTLSeconds is how long a download capability lives. Zero selects the default;
	// above the ceiling is refused at startup.
	CapabilityTTLSeconds int
	// MaxConcurrentDownloads caps the downloads one replica streams at once. Zero selects the
	// default; a negative value is refused at startup.
	MaxConcurrentDownloads int
	// SweepIntervalSeconds is the cadence of the deadline sweep. Zero selects the default.
	SweepIntervalSeconds int
	// Deadlines are how long the platform waits in each phase of an update, measured from the
	// last accepted device report. Zero selects each default; negative is refused.
	Deadlines DeadlinesConfiguration
	// ObjectStore says whether this service uses the instance object store: ObjectStoreInstance
	// (the default) keeps artifact content in it, ObjectStoreNone runs the service with NO store
	// whatever the instance configures, so every artifact declaration and upload is refused with
	// OBJECT_STORE_NOT_CONFIGURED. It is for an instance whose store volume this service cannot
	// share; the deploy tooling sets it, an operator does not. Anything else is refused at startup.
	ObjectStore string
}

const (
	// ObjectStoreInstance is the default: artifact content lives in the instance object store.
	ObjectStoreInstance = "instance"
	// ObjectStoreNone runs the service without an object store (see UpdatesConfiguration.ObjectStore).
	ObjectStoreNone = "none"
)

// DeadlinesConfiguration is the per-phase wait of an update attempt, in seconds.
type DeadlinesConfiguration struct {
	// AcknowledgeSeconds covers QUEUED and INITIATED: an offline device may take a long time to
	// come back, so the default is a day.
	AcknowledgeSeconds int
	// DownloadSeconds covers DOWNLOADING and DOWNLOADED, measured from the last progress report.
	DownloadSeconds int
	// VerifySeconds covers VERIFIED.
	VerifySeconds int
	// InstallSeconds covers UPDATING; silence here is UNKNOWN rather than failure.
	InstallSeconds int
	// ConfirmSeconds covers REBOOTING; silence here is UNKNOWN rather than failure.
	ConfirmSeconds int
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
	if c.Updates.MaxConcurrentUploads == 0 {
		c.Updates.MaxConcurrentUploads = DefaultMaxConcurrentUploads
	}
	if c.Updates.MaxConcurrentDownloads == 0 {
		c.Updates.MaxConcurrentDownloads = DefaultMaxConcurrentDownloads
	}
	if c.Updates.CapabilityTTLSeconds == 0 {
		c.Updates.CapabilityTTLSeconds = DefaultCapabilityTTLSeconds
	}
	if c.Updates.SweepIntervalSeconds == 0 {
		c.Updates.SweepIntervalSeconds = DefaultSweepIntervalSeconds
	}
	if c.Updates.ObjectStore == "" {
		c.Updates.ObjectStore = ObjectStoreInstance
	}
	d := &c.Updates.Deadlines
	for _, f := range []struct {
		v   *int
		def int
	}{{&d.AcknowledgeSeconds, 24 * 3600}, {&d.DownloadSeconds, 6 * 3600}, {&d.VerifySeconds, 30 * 60},
		{&d.InstallSeconds, 3600}, {&d.ConfirmSeconds, 3600}} {
		if *f.v == 0 {
			*f.v = f.def
		}
	}
}

// Policy is the attempt deadlines as the update contract takes them.
func (c *UpdateManagementConfiguration) Policy() ota.Policy {
	d := c.Updates.Deadlines
	sec := func(n int) time.Duration { return time.Duration(n) * time.Second }
	return ota.Policy{Acknowledge: sec(d.AcknowledgeSeconds), Download: sec(d.DownloadSeconds),
		Verify: sec(d.VerifySeconds), Install: sec(d.InstallSeconds), Confirm: sec(d.ConfirmSeconds)}
}

// Validate is the validation hook for this service (ADR-022 decision 1).
func (c *UpdateManagementConfiguration) Validate() error {
	if c.Updates.MaxArtifactBytes < 0 {
		return fmt.Errorf("updates.maxArtifactBytes must not be negative (got %d)", c.Updates.MaxArtifactBytes)
	}
	if c.Updates.MaxTenantArtifactBytes < 0 {
		return fmt.Errorf("updates.maxTenantArtifactBytes must not be negative (got %d)", c.Updates.MaxTenantArtifactBytes)
	}
	if c.Updates.MaxConcurrentUploads < 0 {
		return fmt.Errorf("updates.maxConcurrentUploads must not be negative (got %d)", c.Updates.MaxConcurrentUploads)
	}
	if c.Updates.MaxConcurrentDownloads < 0 {
		return fmt.Errorf("updates.maxConcurrentDownloads must not be negative (got %d)", c.Updates.MaxConcurrentDownloads)
	}
	if c.Updates.CapabilityTTLSeconds < 0 || c.Updates.CapabilityTTLSeconds > MaxCapabilityTTLSeconds {
		return fmt.Errorf("updates.capabilityTtlSeconds must be between 1 and %d (got %d)",
			MaxCapabilityTTLSeconds, c.Updates.CapabilityTTLSeconds)
	}
	if c.Updates.SweepIntervalSeconds < 0 {
		return fmt.Errorf("updates.sweepIntervalSeconds must not be negative (got %d)", c.Updates.SweepIntervalSeconds)
	}
	if err := c.Updates.validateBaseURL(); err != nil {
		return err
	}
	if c.Updates.ObjectStore != ObjectStoreInstance && c.Updates.ObjectStore != ObjectStoreNone {
		return fmt.Errorf("updates.objectStore must be %q or %q (got %q)", ObjectStoreInstance, ObjectStoreNone, c.Updates.ObjectStore)
	}
	if err := c.Policy().Validate(); err != nil {
		return fmt.Errorf("updates.deadlines: %w", err)
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

// DownloadBaseIsInsecure reports a configured download base that is plain http on a host other
// than loopback. It is a warning, not a refusal: a lab instance installed without TLS derives
// exactly this, and the device side decides whether to accept an http link.
func (u UpdatesConfiguration) DownloadBaseIsInsecure() bool {
	parsed, err := url.Parse(u.DownloadBaseURL)
	if err != nil || parsed.Scheme != "http" {
		return false
	}
	h := strings.ToLower(parsed.Hostname())
	if ip := net.ParseIP(h); (ip != nil && ip.IsLoopback()) || h == "localhost" || strings.HasSuffix(h, ".localhost") {
		return false
	}
	return true
}

func (u UpdatesConfiguration) validateBaseURL() error {
	if u.DownloadBaseURL == "" {
		return nil
	}
	parsed, err := url.Parse(u.DownloadBaseURL)
	if err != nil || !parsed.IsAbs() || parsed.Host == "" || (parsed.Scheme != "https" && parsed.Scheme != "http") {
		return fmt.Errorf("updates.downloadBaseUrl must be an absolute http or https URL (got %q)", u.DownloadBaseURL)
	}
	if parsed.RawQuery != "" || parsed.Fragment != "" || parsed.User != nil {
		return fmt.Errorf("updates.downloadBaseUrl must not carry a query, a fragment or credentials (got %q)", u.DownloadBaseURL)
	}
	return nil
}
