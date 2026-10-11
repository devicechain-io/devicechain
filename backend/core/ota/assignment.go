// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package ota

import (
	"encoding/json"
	"fmt"
	"net/url"
	"time"
	"unicode/utf8"

	"github.com/devicechain-io/dc-microservice/core"
)

// The assignment and download kinds share the device-plane envelope with the update and
// configuration kinds: the same version, the same strict key scan, and the same rule that the
// device's identity is the subject it published on and never a field.
//
//	down  {"v":1,"kind":"update.assignment","attemptId":"…","assignmentId":"…","component":"firmware",
//	       "trust":"UNVERIFIED","artifact":{"version":"1.4.2","digest":"sha256:…","size":8388608,
//	       "requiresReboot":true}}
//	up    {"v":1,"kind":"download.request","assignmentId":"…"}
//	down  {"v":1,"kind":"download.grant","assignmentId":"…","url":"https://…?cap=…","expiresAt":"…"}
//	down  {"v":1,"kind":"update.cancel","attemptId":"…","assignmentId":"…"}
//
// 🔴 A DOWNLOAD GRANT IS A CAPABILITY, NOT A CREDENTIAL. The url names one artifact for one
// device under one assignment and stops working at expiresAt; a device that wants more time asks
// again with the same download.request, and what it gets back names the same artifact.
//
// update.cancel is sent for an assignment the platform no longer wants the device to work on: the
// operator cancelled it, or a device that asked for a download grant named an assignment that is
// no longer current. A device that has not begun to install answers with ABANDONED.
const (
	KindDownloadRequest  = "download.request"
	KindUpdateAssignment = "update.assignment"
	KindDownloadGrant    = "download.grant"
	KindUpdateCancel     = "update.cancel"

	// TrustUnverified is the only trust a down assignment carries: the platform checked that the
	// bytes are the bytes that were declared, and did not authenticate who produced them.
	TrustUnverified = "UNVERIFIED"

	// MaxAssignmentBytes bounds an encoded update.assignment, download.grant or update.cancel.
	MaxAssignmentBytes = 4096

	// MaxGrantURLBytes bounds a download.grant url.
	MaxGrantURLBytes = 2048
)

// DownloadRequest is the device's ask for a download grant for its current assignment.
type DownloadRequest struct {
	V            int    `json:"v"`
	Kind         string `json:"kind"`
	AssignmentID string `json:"assignmentId"`
}

// Validate checks a download.request against the wire grammar.
func (d DownloadRequest) Validate() error {
	if d.V != ReportVersion {
		return fmt.Errorf("ota: unsupported report version %d (want %d)", d.V, ReportVersion)
	}
	if d.Kind != KindDownloadRequest {
		return fmt.Errorf("ota: unknown report kind %q", d.Kind)
	}
	return checkID("assignmentId", d.AssignmentID)
}

// AssignmentArtifact is what the assignment tells the device about the image it should install.
type AssignmentArtifact struct {
	Version        string `json:"version"`
	Digest         string `json:"digest"`
	Size           int64  `json:"size"`
	RequiresReboot bool   `json:"requiresReboot"`
}

// UpdateAssignment is the platform's desired-update message to one device.
type UpdateAssignment struct {
	V            int                `json:"v"`
	Kind         string             `json:"kind"`
	AttemptID    string             `json:"attemptId"`
	AssignmentID string             `json:"assignmentId"`
	Component    string             `json:"component"`
	Trust        string             `json:"trust"`
	Artifact     AssignmentArtifact `json:"artifact"`
}

// Validate checks an update.assignment against the wire grammar.
func (u UpdateAssignment) Validate() error {
	if u.V != ReportVersion {
		return fmt.Errorf("ota: unsupported message version %d (want %d)", u.V, ReportVersion)
	}
	if u.Kind != KindUpdateAssignment {
		return fmt.Errorf("ota: unknown message kind %q", u.Kind)
	}
	if err := checkID("attemptId", u.AttemptID); err != nil {
		return err
	}
	if err := checkID("assignmentId", u.AssignmentID); err != nil {
		return err
	}
	if err := checkLen("component", u.Component, maxComponentLen); err != nil {
		return err
	}
	if !componentGrammar.MatchString(u.Component) {
		return fmt.Errorf("ota: component %q must match %s", u.Component, componentGrammar)
	}
	if u.Trust != TrustUnverified {
		return fmt.Errorf("ota: unknown trust %q", u.Trust)
	}
	a := u.Artifact
	if err := checkLen("artifact.version", a.Version, maxVersionLen); err != nil {
		return err
	}
	if err := checkPrintable("artifact.version", a.Version); err != nil {
		return err
	}
	if err := checkDigest("artifact.digest", a.Digest, true); err != nil {
		return err
	}
	if a.Size < 1 {
		return fmt.Errorf("ota: artifact.size must be at least 1")
	}
	return nil
}

// DownloadGrant is the platform's answer to a download.request.
type DownloadGrant struct {
	V            int    `json:"v"`
	Kind         string `json:"kind"`
	AssignmentID string `json:"assignmentId"`
	URL          string `json:"url"`
	ExpiresAt    string `json:"expiresAt"`
}

// Validate checks a download.grant against the wire grammar.
func (g DownloadGrant) Validate() error {
	if g.V != ReportVersion {
		return fmt.Errorf("ota: unsupported message version %d (want %d)", g.V, ReportVersion)
	}
	if g.Kind != KindDownloadGrant {
		return fmt.Errorf("ota: unknown message kind %q", g.Kind)
	}
	if err := checkID("assignmentId", g.AssignmentID); err != nil {
		return err
	}
	if len(g.URL) < 1 || len(g.URL) > MaxGrantURLBytes {
		return fmt.Errorf("ota: url must be 1-%d bytes, got %d", MaxGrantURLBytes, len(g.URL))
	}
	if !utf8.ValidString(g.URL) {
		return fmt.Errorf("ota: url is not valid UTF-8")
	}
	u, err := url.Parse(g.URL)
	if err != nil || !u.IsAbs() || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return fmt.Errorf("ota: url must be an absolute http or https URL")
	}
	if _, err := g.Expiry(); err != nil {
		return err
	}
	return nil
}

// Expiry parses expiresAt, which is an RFC 3339 UTC timestamp.
func (g DownloadGrant) Expiry() (time.Time, error) {
	t, err := time.Parse(time.RFC3339, g.ExpiresAt)
	if err != nil {
		return time.Time{}, fmt.Errorf("ota: expiresAt must be an RFC 3339 timestamp: %w", err)
	}
	return t.UTC(), nil
}

// UpdateCancel tells the device to stop working on an assignment.
type UpdateCancel struct {
	V            int    `json:"v"`
	Kind         string `json:"kind"`
	AttemptID    string `json:"attemptId,omitempty"`
	AssignmentID string `json:"assignmentId"`
}

// Validate checks an update.cancel against the wire grammar.
func (c UpdateCancel) Validate() error {
	if c.V != ReportVersion {
		return fmt.Errorf("ota: unsupported message version %d (want %d)", c.V, ReportVersion)
	}
	if c.Kind != KindUpdateCancel {
		return fmt.Errorf("ota: unknown message kind %q", c.Kind)
	}
	if c.AttemptID != "" {
		if err := checkID("attemptId", c.AttemptID); err != nil {
			return err
		}
	}
	return checkID("assignmentId", c.AssignmentID)
}

func checkID(name, v string) error {
	if err := checkLen(name, v, maxIDLen); err != nil {
		return err
	}
	if err := core.ValidateToken(v); err != nil {
		return fmt.Errorf("ota: %s: %w", name, err)
	}
	return nil
}

// EncodeUpdateAssignment builds the down message that hands a device an update.
func EncodeUpdateAssignment(attemptID, assignmentID, component string, a AssignmentArtifact) ([]byte, error) {
	m := UpdateAssignment{V: ReportVersion, Kind: KindUpdateAssignment, AttemptID: attemptID,
		AssignmentID: assignmentID, Component: component, Trust: TrustUnverified, Artifact: a}
	if err := m.Validate(); err != nil {
		return nil, err
	}
	return encodeBounded(m)
}

// EncodeDownloadGrant builds the down message that carries a capability URL to a device.
func EncodeDownloadGrant(assignmentID, downloadURL string, expiresAt time.Time) ([]byte, error) {
	m := DownloadGrant{V: ReportVersion, Kind: KindDownloadGrant, AssignmentID: assignmentID, URL: downloadURL,
		ExpiresAt: expiresAt.UTC().Format(time.RFC3339)}
	if err := m.Validate(); err != nil {
		return nil, err
	}
	return encodeBounded(m)
}

// EncodeUpdateCancel builds the down message that tells a device to stop. attemptID may be empty
// when the platform has no attempt to name (a grant request for an assignment it does not hold).
func EncodeUpdateCancel(attemptID, assignmentID string) ([]byte, error) {
	m := UpdateCancel{V: ReportVersion, Kind: KindUpdateCancel, AttemptID: attemptID, AssignmentID: assignmentID}
	if err := m.Validate(); err != nil {
		return nil, err
	}
	return encodeBounded(m)
}

func encodeBounded(v any) ([]byte, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	if len(b) > MaxAssignmentBytes {
		return nil, fmt.Errorf("ota: message encodes to %d bytes, over the %d byte limit", len(b), MaxAssignmentBytes)
	}
	return b, nil
}

// DesiredMessage is one decoded platform-to-device message of the update family. Exactly one of
// the pointers is set, the one the Kind names.
type DesiredMessage struct {
	Kind       string
	Assignment *UpdateAssignment
	Grant      *DownloadGrant
	Cancel     *UpdateCancel
}

// DecodeDesiredUpdate parses and validates a down message of the update family. A kind outside
// the family (a config.revision, say) is an error here: the device-side dispatcher routes by kind
// before calling the decoder it means.
func DecodeDesiredUpdate(b []byte) (DesiredMessage, error) {
	if len(b) > MaxAssignmentBytes {
		return DesiredMessage{}, fmt.Errorf("ota: message is %d bytes, exceeds the %d byte limit", len(b), MaxAssignmentBytes)
	}
	_, kind, err := scanKeys(b)
	if err != nil {
		return DesiredMessage{}, fmt.Errorf("ota: decode message: %w", err)
	}
	switch kind {
	case KindUpdateAssignment:
		var m UpdateAssignment
		if err := strictDecode(b, &m); err != nil {
			return DesiredMessage{}, err
		}
		if err := m.Validate(); err != nil {
			return DesiredMessage{}, err
		}
		return DesiredMessage{Kind: kind, Assignment: &m}, nil
	case KindDownloadGrant:
		var m DownloadGrant
		if err := strictDecode(b, &m); err != nil {
			return DesiredMessage{}, err
		}
		if err := m.Validate(); err != nil {
			return DesiredMessage{}, err
		}
		return DesiredMessage{Kind: kind, Grant: &m}, nil
	case KindUpdateCancel:
		var m UpdateCancel
		if err := strictDecode(b, &m); err != nil {
			return DesiredMessage{}, err
		}
		if err := m.Validate(); err != nil {
			return DesiredMessage{}, err
		}
		return DesiredMessage{Kind: kind, Cancel: &m}, nil
	default:
		return DesiredMessage{}, fmt.Errorf("ota: unknown message kind %q", kind)
	}
}
