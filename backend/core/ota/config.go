// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package ota

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"unicode/utf8"
)

// The configuration kinds share the device-plane envelope with the update kinds (report.go):
// the same version, the same strict key scan, the same rule that the device's identity is the
// subject it published on and never a field.
//
//	up    {"v":1,"kind":"sync","bootId":"…","config":{"haveRevision":3}}
//	up    {"v":1,"kind":"config.status","revision":3,"digest":"sha256:…","status":"APPLIED"}
//	up    {"v":1,"kind":"config.status","revision":3,"digest":"sha256:…","status":"REJECTED",
//	       "errors":["sampleIntervalSeconds: out of range"]}
//	down  {"v":1,"kind":"config.revision","revision":3,"digest":"sha256:…","document":"{…}"}
//
// 🔴 THE DOCUMENT TRAVELS AS A JSON STRING, NOT AS AN EMBEDDED OBJECT. The digest covers the
// exact bytes of the canonical document, and a device that parsed an embedded object and
// re-serialized it would hash different bytes whenever its serializer orders keys or writes
// numbers differently. A string is opaque to every parser: the device hashes what it decodes
// from the string and compares it with the digest before it applies anything.
const (
	KindSync           = "sync"
	KindConfigStatus   = "config.status"
	KindConfigRevision = "config.revision"

	// ConfigReceived, ConfigApplied and ConfigRejected are the statuses a device reports for a
	// revision. Received: it holds the revision and has not applied it. Applied: it did.
	// Rejected: it refused the revision, and Errors says why.
	ConfigReceived = "RECEIVED"
	ConfigApplied  = "APPLIED"
	ConfigRejected = "REJECTED"

	// MaxDesiredBytes bounds an encoded config.revision message: a document at its 64 KiB cap
	// escaped inside a JSON string (at worst every byte a quote, so twice the size) plus the
	// envelope around it.
	MaxDesiredBytes = 2*64*1024 + 1024

	maxConfigErrors   = 16
	maxConfigErrorLen = 256
	maxHaveRevision   = int64(1) << 53
)

// SyncReport is the device's announcement that it is connected and what it holds. Config is
// required in this version: a sync that says nothing about configuration has nothing to answer.
type SyncReport struct {
	V      int         `json:"v"`
	Kind   string      `json:"kind"`
	BootID string      `json:"bootId,omitempty"`
	Config *SyncConfig `json:"config"`
}

// SyncConfig is what the device holds. HaveRevision 0 means it holds none.
type SyncConfig struct {
	HaveRevision int64 `json:"haveRevision"`
}

// ConfigStatusReport is the device's report on one revision it was sent.
type ConfigStatusReport struct {
	V        int      `json:"v"`
	Kind     string   `json:"kind"`
	Revision int64    `json:"revision"`
	Digest   string   `json:"digest"`
	Status   string   `json:"status"`
	Errors   []string `json:"errors,omitempty"`
}

// ConfigRevision is the platform's desired-configuration message to one device.
type ConfigRevision struct {
	V        int    `json:"v"`
	Kind     string `json:"kind"`
	Revision int64  `json:"revision"`
	Digest   string `json:"digest"`
	Document string `json:"document"`
}

// DeviceReport is one decoded device-to-platform message of any kind. Exactly one of the
// pointers is set, the one the Kind names.
type DeviceReport struct {
	Kind         string
	Update       *Report
	Sync         *SyncReport
	ConfigStatus *ConfigStatusReport
	// DownloadRequest is set for a download.request (assignment.go).
	DownloadRequest *DownloadRequest
}

// DecodeDeviceReport parses and validates one device-to-platform message of any kind. It is the
// single decoder for the up channel: the size cap, the exact-key scan, strict decoding and
// trailing-data refusal are the update decoder's own, and a kind it does not know is an error.
func DecodeDeviceReport(b []byte) (DeviceReport, error) {
	if len(b) > MaxReportBytes {
		return DeviceReport{}, fmt.Errorf("ota: report is %d bytes, exceeds the %d byte limit", len(b), MaxReportBytes)
	}
	_, kind, err := scanKeys(b)
	if err != nil {
		return DeviceReport{}, fmt.Errorf("ota: decode report: %w", err)
	}
	switch kind {
	case KindProgress, KindInventory:
		r, err := DecodeReport(b)
		if err != nil {
			return DeviceReport{}, err
		}
		return DeviceReport{Kind: kind, Update: &r}, nil
	case KindSync:
		var s SyncReport
		if err := strictDecode(b, &s); err != nil {
			return DeviceReport{}, err
		}
		if err := s.Validate(); err != nil {
			return DeviceReport{}, err
		}
		return DeviceReport{Kind: kind, Sync: &s}, nil
	case KindConfigStatus:
		var c ConfigStatusReport
		if err := strictDecode(b, &c); err != nil {
			return DeviceReport{}, err
		}
		if err := c.Validate(); err != nil {
			return DeviceReport{}, err
		}
		return DeviceReport{Kind: kind, ConfigStatus: &c}, nil
	case KindDownloadRequest:
		var d DownloadRequest
		if err := strictDecode(b, &d); err != nil {
			return DeviceReport{}, err
		}
		if err := d.Validate(); err != nil {
			return DeviceReport{}, err
		}
		return DeviceReport{Kind: kind, DownloadRequest: &d}, nil
	default:
		return DeviceReport{}, fmt.Errorf("ota: unknown report kind %q", kind)
	}
}

// strictDecode decodes b into v, refusing unknown fields and anything after the value.
func strictDecode(b []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("ota: decode report: %w", err)
	}
	var extra json.RawMessage
	if err := dec.Decode(&extra); err != io.EOF {
		return fmt.Errorf("ota: decode report: trailing data after the report")
	}
	return nil
}

// Validate checks a sync against the wire grammar.
func (s SyncReport) Validate() error {
	if s.V != ReportVersion {
		return fmt.Errorf("ota: unsupported report version %d (want %d)", s.V, ReportVersion)
	}
	if s.Kind != KindSync {
		return fmt.Errorf("ota: unknown report kind %q", s.Kind)
	}
	if s.BootID != "" {
		if err := checkBootID(s.BootID); err != nil {
			return err
		}
	}
	if s.Config == nil {
		return fmt.Errorf("ota: config is required on a sync report")
	}
	if s.Config.HaveRevision < 0 || s.Config.HaveRevision > maxHaveRevision {
		return fmt.Errorf("ota: config.haveRevision %d is out of range", s.Config.HaveRevision)
	}
	return nil
}

// Validate checks a config.status against the wire grammar.
func (c ConfigStatusReport) Validate() error {
	if c.V != ReportVersion {
		return fmt.Errorf("ota: unsupported report version %d (want %d)", c.V, ReportVersion)
	}
	if c.Kind != KindConfigStatus {
		return fmt.Errorf("ota: unknown report kind %q", c.Kind)
	}
	if c.Revision < 1 || c.Revision > maxHaveRevision {
		return fmt.Errorf("ota: revision %d is out of range", c.Revision)
	}
	if err := checkDigest("digest", c.Digest, true); err != nil {
		return err
	}
	switch c.Status {
	case ConfigReceived, ConfigApplied:
		if len(c.Errors) != 0 {
			return fmt.Errorf("ota: errors are only allowed when status is %s", ConfigRejected)
		}
	case ConfigRejected:
		if len(c.Errors) < 1 {
			return fmt.Errorf("ota: errors are required when status is %s", ConfigRejected)
		}
	default:
		return fmt.Errorf("ota: unknown config status %q", c.Status)
	}
	if len(c.Errors) > maxConfigErrors {
		return fmt.Errorf("ota: %d errors exceed the %d entry limit", len(c.Errors), maxConfigErrors)
	}
	for i, e := range c.Errors {
		if len(e) < 1 || len(e) > maxConfigErrorLen {
			return fmt.Errorf("ota: errors[%d] must be 1-%d bytes, got %d", i, maxConfigErrorLen, len(e))
		}
		if err := checkPrintable(fmt.Sprintf("errors[%d]", i), e); err != nil {
			return err
		}
	}
	return nil
}

// Validate checks a config.revision against the wire grammar. It does not check that the
// digest matches the document; DigestMatches does, and a device must.
func (c ConfigRevision) Validate() error {
	if c.V != ReportVersion {
		return fmt.Errorf("ota: unsupported message version %d (want %d)", c.V, ReportVersion)
	}
	if c.Kind != KindConfigRevision {
		return fmt.Errorf("ota: unknown message kind %q", c.Kind)
	}
	if c.Revision < 1 || c.Revision > maxHaveRevision {
		return fmt.Errorf("ota: revision %d is out of range", c.Revision)
	}
	if err := checkDigest("digest", c.Digest, true); err != nil {
		return err
	}
	if c.Document == "" {
		return fmt.Errorf("ota: document is required")
	}
	if !utf8.ValidString(c.Document) {
		return fmt.Errorf("ota: document is not valid UTF-8")
	}
	return nil
}

// DigestMatches reports whether the document hashes to the digest the message names. A device
// applies nothing until this is true.
func (c ConfigRevision) DigestMatches() bool {
	sum := sha256.Sum256([]byte(c.Document))
	return c.Digest == "sha256:"+hex.EncodeToString(sum[:])
}

// EncodeConfigRevision builds the down message for one revision. It refuses a digest that does
// not match the document and a message over MaxDesiredBytes: a revision that cannot be delivered
// intact is not sent, rather than sent in a form the device would reject or mis-hash.
func EncodeConfigRevision(revision int64, digest, document string) ([]byte, error) {
	c := ConfigRevision{V: ReportVersion, Kind: KindConfigRevision, Revision: revision, Digest: digest, Document: document}
	if err := c.Validate(); err != nil {
		return nil, err
	}
	if !c.DigestMatches() {
		return nil, fmt.Errorf("ota: revision %d: the stored document does not hash to its digest", revision)
	}
	b, err := json.Marshal(c)
	if err != nil {
		return nil, err
	}
	if len(b) > MaxDesiredBytes {
		return nil, fmt.Errorf("ota: revision %d encodes to %d bytes, over the %d byte limit", revision, len(b), MaxDesiredBytes)
	}
	return b, nil
}

// DecodeConfigRevision parses and validates a down message. The platform never reads its own
// output; this is the device side's decoder (the simulator mirrors it from its own structs) and
// the contract test's.
func DecodeConfigRevision(b []byte) (ConfigRevision, error) {
	if len(b) > MaxDesiredBytes {
		return ConfigRevision{}, fmt.Errorf("ota: message is %d bytes, exceeds the %d byte limit", len(b), MaxDesiredBytes)
	}
	if _, _, err := scanKeys(b); err != nil {
		return ConfigRevision{}, fmt.Errorf("ota: decode message: %w", err)
	}
	var c ConfigRevision
	if err := strictDecode(b, &c); err != nil {
		return ConfigRevision{}, err
	}
	if err := c.Validate(); err != nil {
		return ConfigRevision{}, err
	}
	return c, nil
}
