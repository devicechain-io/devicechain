// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package wire is this device's own reading of the published update protocol (the "Over-the-air
// updates" section of the device guide). It is written from that page alone and imports nothing
// from the platform: the device must stand on the public contract, and the conformance tests
// check this file against the platform's golden fixtures.
//
// It is strict in both directions. Messages it reads are refused when they carry an unknown or
// repeated key, a key that differs from the contract's only by case, trailing data, or a value
// outside the grammar. Messages it writes go through the same checks before they are sent, so a
// bug in this device can never put an invalid frame on the wire.
package wire

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"regexp"
	"time"
	"unicode/utf8"
)

// Message kinds.
const (
	KindAssignment      = "update.assignment"
	KindGrant           = "download.grant"
	KindCancel          = "update.cancel"
	KindProgress        = "update.progress"
	KindInventory       = "update.inventory"
	KindDownloadRequest = "download.request"
	KindSync            = "sync"
	KindConfigRevision  = "config.revision"
)

// Version is the protocol version every message carries.
const Version = 1

// MaxBytes caps any message in either direction.
const MaxBytes = 4096

// Stages of an update.progress report, in the order they may be reported.
const (
	StageReceived    = "RECEIVED"
	StageDownloading = "DOWNLOADING"
	StageDownloaded  = "DOWNLOADED"
	StageVerified    = "VERIFIED"
	StageInstalling  = "INSTALLING"
	StageRebooting   = "REBOOTING"
	StageRunning     = "RUNNING"
	StageFailed      = "FAILED"
	StageAbandoned   = "ABANDONED"
)

var stageRank = map[string]int{
	StageReceived: 1, StageDownloading: 2, StageDownloaded: 3, StageVerified: 4,
	StageInstalling: 5, StageRebooting: 6, StageRunning: 7, StageFailed: 7, StageAbandoned: 7,
}

// StageRank orders the stages; the terminal ones share the highest rank. It is 0 for an unknown stage.
func StageRank(stage string) int { return stageRank[stage] }

// Terminal reports whether no report may follow the stage.
func Terminal(stage string) bool {
	return stage == StageRunning || stage == StageFailed || stage == StageAbandoned
}

// Trust values of an assignment.
const (
	TrustUnverified = "UNVERIFIED"
	TrustSigned     = "SIGNED"
)

// Signature algorithms.
const (
	AlgECDSA   = "ECDSA_P256_SHA256"
	AlgEd25519 = "ED25519"
)

// Failure codes the contract names. Others are this device's own and are listed where they arise.
const (
	CodeSignatureInvalid = "SIGNATURE_INVALID"
	CodeUnknownSigner    = "UNKNOWN_SIGNER"
)

const (
	maxComponent = 64
	maxID        = 128
	maxVersion   = 64
	maxBootID    = 128
	maxCode      = 64
	maxDetail    = 512
	maxURL       = 2048
	maxSigB64    = 172
)

var (
	componentRE = regexp.MustCompile(`^[a-z0-9._/-]+$`)
	digestRE    = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	codeRE      = regexp.MustCompile(`^[A-Z0-9_]+$`)
	idRE        = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]*$`)
	keyIDRE     = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

// Artifact is the image an assignment describes.
type Artifact struct {
	Version        string `json:"version"`
	Digest         string `json:"digest"`
	Size           int64  `json:"size"`
	RequiresReboot bool   `json:"requiresReboot"`
}

// Assignment is the platform's update.assignment.
type Assignment struct {
	V            int      `json:"v"`
	Kind         string   `json:"kind"`
	AttemptID    string   `json:"attemptId"`
	AssignmentID string   `json:"assignmentId"`
	Component    string   `json:"component"`
	Trust        string   `json:"trust"`
	Artifact     Artifact `json:"artifact"`
	Signature    string   `json:"signature,omitempty"`
	KeyID        string   `json:"keyId,omitempty"`
	Algorithm    string   `json:"algorithm,omitempty"`
}

// Grant is the platform's download.grant.
type Grant struct {
	V            int    `json:"v"`
	Kind         string `json:"kind"`
	AssignmentID string `json:"assignmentId"`
	URL          string `json:"url"`
	ExpiresAt    string `json:"expiresAt"`
}

// Cancel is the platform's update.cancel.
type Cancel struct {
	V            int    `json:"v"`
	Kind         string `json:"kind"`
	AttemptID    string `json:"attemptId,omitempty"`
	AssignmentID string `json:"assignmentId"`
}

// DownloadRequest is the device's ask for a grant.
type DownloadRequest struct {
	V            int    `json:"v"`
	Kind         string `json:"kind"`
	AssignmentID string `json:"assignmentId"`
}

// SyncConfig is the configuration half of a sync.
type SyncConfig struct {
	HaveRevision int64 `json:"haveRevision"`
}

// Sync is the message a device sends on every connect.
type Sync struct {
	V      int        `json:"v"`
	Kind   string     `json:"kind"`
	BootID string     `json:"bootId,omitempty"`
	Config SyncConfig `json:"config"`
}

// Progress is bytes held and the total, when known.
type Progress struct {
	Bytes int64 `json:"bytes"`
	Total int64 `json:"total"`
}

// Result is a failure code and detail.
type Result struct {
	Code   string `json:"code"`
	Detail string `json:"detail"`
}

// Running is what a component runs now.
type Running struct {
	Version string `json:"version"`
	Digest  string `json:"digest,omitempty"`
}

// Report is an update.progress or update.inventory.
type Report struct {
	V              int       `json:"v"`
	Kind           string    `json:"kind"`
	AttemptID      string    `json:"attemptId,omitempty"`
	AssignmentID   string    `json:"assignmentId,omitempty"`
	Component      string    `json:"component"`
	ArtifactDigest string    `json:"artifactDigest,omitempty"`
	Seq            uint64    `json:"seq,omitempty"`
	Stage          string    `json:"stage,omitempty"`
	Progress       *Progress `json:"progress,omitempty"`
	Result         *Result   `json:"result,omitempty"`
	Running        *Running  `json:"running,omitempty"`
	BootID         string    `json:"bootId,omitempty"`
}

// Encode marshals a message and checks it as a receiver would. A frame that fails is never returned.
func Encode(v any) ([]byte, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	if _, err := CheckOutbound(b); err != nil {
		return nil, err
	}
	return b, nil
}

// shape lists the keys an object may carry; a nil child is a leaf.
type shape map[string]shape

// anyShape is the union of every key of every kind, so one scan serves all of them. A key that
// belongs to another kind passes the scan and is then refused by the kind's own struct decode.
var anyShape = shape{
	"v": nil, "kind": nil, "attemptId": nil, "assignmentId": nil, "component": nil,
	"artifactDigest": nil, "seq": nil, "stage": nil, "bootId": nil, "url": nil, "expiresAt": nil,
	"trust": nil, "signature": nil, "keyId": nil, "algorithm": nil,
	"revision": nil, "digest": nil, "document": nil,
	"progress": {"bytes": nil, "total": nil},
	"result":   {"code": nil, "detail": nil},
	"running":  {"version": nil, "digest": nil},
	"artifact": {"version": nil, "digest": nil, "size": nil, "requiresReboot": nil},
	"config":   {"haveRevision": nil},
}

// inventoryForbidden may not appear at all on an update.inventory, not even as null.
var inventoryForbidden = []string{"attemptId", "assignmentId", "seq", "stage", "artifactDigest", "progress", "result"}

// scan walks the raw JSON and enforces what encoding/json does not: every key must match a
// contract key EXACTLY (the standard decoder folds case) and none may repeat within an object.
func scan(b []byte) (top map[string]bool, kind string, err error) {
	dec := json.NewDecoder(bytes.NewReader(b))
	t, err := dec.Token()
	if err != nil {
		return nil, "", err
	}
	if d, ok := t.(json.Delim); !ok || d != '{' {
		return nil, "", fmt.Errorf("message must be a JSON object")
	}
	top = map[string]bool{}
	if err := scanObject(dec, anyShape, top, &kind); err != nil {
		return nil, "", err
	}
	return top, kind, nil
}

func scanObject(dec *json.Decoder, sh shape, seen map[string]bool, kind *string) error {
	for dec.More() {
		kt, err := dec.Token()
		if err != nil {
			return err
		}
		key, _ := kt.(string)
		child, ok := sh[key]
		if !ok {
			return fmt.Errorf("unknown field %q", key)
		}
		if seen[key] {
			return fmt.Errorf("duplicate field %q", key)
		}
		seen[key] = true
		vt, err := dec.Token()
		if err != nil {
			return err
		}
		switch v := vt.(type) {
		case json.Delim:
			if v == '{' && child != nil {
				if err := scanObject(dec, child, map[string]bool{}, nil); err != nil {
					return err
				}
				continue
			}
			if err := skip(dec); err != nil {
				return err
			}
		case string:
			if key == "kind" && kind != nil {
				*kind = v
			}
		}
	}
	_, err := dec.Token()
	return err
}

func skip(dec *json.Decoder) error {
	for depth := 1; depth > 0; {
		t, err := dec.Token()
		if err != nil {
			return err
		}
		if d, ok := t.(json.Delim); ok {
			if d == '{' || d == '[' {
				depth++
			} else {
				depth--
			}
		}
	}
	return nil
}

func strict(b []byte, into any) error {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(into); err != nil {
		return err
	}
	var extra json.RawMessage
	if err := dec.Decode(&extra); err != io.EOF {
		return fmt.Errorf("trailing data after the message")
	}
	return nil
}

// Desired is one decoded platform-to-device message; exactly one pointer is set, except that a
// message of a kind this device does not act on (a config.revision) sets only Kind.
type Desired struct {
	Kind       string
	Assignment *Assignment
	Grant      *Grant
	Cancel     *Cancel
}

// DecodeDesired parses and validates a message read from device-desired.
func DecodeDesired(b []byte) (Desired, error) {
	if len(b) > MaxBytes {
		return Desired{}, fmt.Errorf("message is %d bytes, over the %d byte limit", len(b), MaxBytes)
	}
	_, kind, err := scan(b)
	if err != nil {
		return Desired{}, fmt.Errorf("decode: %w", err)
	}
	switch kind {
	case KindAssignment:
		var m Assignment
		if err := strict(b, &m); err != nil {
			return Desired{}, err
		}
		if err := m.Validate(); err != nil {
			return Desired{}, err
		}
		return Desired{Kind: kind, Assignment: &m}, nil
	case KindGrant:
		var m Grant
		if err := strict(b, &m); err != nil {
			return Desired{}, err
		}
		if err := m.Validate(); err != nil {
			return Desired{}, err
		}
		return Desired{Kind: kind, Grant: &m}, nil
	case KindCancel:
		var m Cancel
		if err := strict(b, &m); err != nil {
			return Desired{}, err
		}
		if err := m.Validate(); err != nil {
			return Desired{}, err
		}
		return Desired{Kind: kind, Cancel: &m}, nil
	case KindConfigRevision:
		return Desired{Kind: kind}, nil
	default:
		return Desired{}, fmt.Errorf("unknown message kind %q", kind)
	}
}

// CheckOutbound validates a frame this device is about to publish on device-reports and returns
// its kind. It is the same grammar the platform applies on receipt.
func CheckOutbound(b []byte) (string, error) {
	if len(b) > MaxBytes {
		return "", fmt.Errorf("message is %d bytes, over the %d byte limit", len(b), MaxBytes)
	}
	top, kind, err := scan(b)
	if err != nil {
		return "", fmt.Errorf("decode: %w", err)
	}
	switch kind {
	case KindProgress, KindInventory:
		if kind == KindInventory {
			for _, k := range inventoryForbidden {
				if top[k] {
					return "", fmt.Errorf("%s is not allowed on an update.inventory report", k)
				}
			}
		}
		var r Report
		if err := strict(b, &r); err != nil {
			return "", err
		}
		return kind, r.Validate()
	case KindDownloadRequest:
		var r DownloadRequest
		if err := strict(b, &r); err != nil {
			return "", err
		}
		return kind, r.Validate()
	case KindSync:
		var s Sync
		if err := strict(b, &s); err != nil {
			return "", err
		}
		return kind, s.Validate()
	default:
		return "", fmt.Errorf("unknown report kind %q", kind)
	}
}

// Validate checks a sync.
func (s Sync) Validate() error {
	if s.V != Version {
		return fmt.Errorf("unsupported version %d", s.V)
	}
	if s.BootID != "" {
		if err := checkBootID(s.BootID); err != nil {
			return err
		}
	}
	if s.Config.HaveRevision < 0 {
		return fmt.Errorf("haveRevision must not be negative")
	}
	return nil
}

// Validate checks a download.request.
func (d DownloadRequest) Validate() error {
	if d.V != Version {
		return fmt.Errorf("unsupported version %d", d.V)
	}
	if d.Kind != KindDownloadRequest {
		return fmt.Errorf("unknown kind %q", d.Kind)
	}
	return checkID("assignmentId", d.AssignmentID)
}

// Validate checks an update.assignment against the wire grammar.
func (a Assignment) Validate() error {
	if a.V != Version {
		return fmt.Errorf("unsupported version %d", a.V)
	}
	if a.Kind != KindAssignment {
		return fmt.Errorf("unknown kind %q", a.Kind)
	}
	for _, f := range []struct{ n, v string }{{"attemptId", a.AttemptID}, {"assignmentId", a.AssignmentID}} {
		if err := checkID(f.n, f.v); err != nil {
			return err
		}
	}
	if err := checkComponent(a.Component); err != nil {
		return err
	}
	switch a.Trust {
	case TrustUnverified:
		if a.Signature != "" || a.KeyID != "" || a.Algorithm != "" {
			return fmt.Errorf("an %s assignment must not carry signature fields", TrustUnverified)
		}
	case TrustSigned:
		if a.Signature == "" || a.KeyID == "" || a.Algorithm == "" {
			return fmt.Errorf("a %s assignment requires signature, keyId and algorithm", TrustSigned)
		}
		if a.Algorithm != AlgECDSA && a.Algorithm != AlgEd25519 {
			return fmt.Errorf("unknown algorithm %q", a.Algorithm)
		}
		if !keyIDRE.MatchString(a.KeyID) {
			return fmt.Errorf("keyId must be 64 lowercase hex characters")
		}
		if len(a.Signature) > maxSigB64 {
			return fmt.Errorf("signature is over the size limit")
		}
		if raw, err := base64.StdEncoding.Strict().DecodeString(a.Signature); err != nil || len(raw) == 0 {
			return fmt.Errorf("signature must be standard base64")
		}
	default:
		return fmt.Errorf("unknown trust %q", a.Trust)
	}
	if err := checkLen("artifact.version", a.Artifact.Version, maxVersion); err != nil {
		return err
	}
	if err := checkPrintable("artifact.version", a.Artifact.Version); err != nil {
		return err
	}
	if err := checkDigest("artifact.digest", a.Artifact.Digest, true); err != nil {
		return err
	}
	if a.Artifact.Size < 1 {
		return fmt.Errorf("artifact.size must be at least 1")
	}
	return nil
}

// Validate checks a download.grant. The origin pin is the device's policy and lives with the
// agent; this is only the grammar.
func (g Grant) Validate() error {
	if g.V != Version {
		return fmt.Errorf("unsupported version %d", g.V)
	}
	if g.Kind != KindGrant {
		return fmt.Errorf("unknown kind %q", g.Kind)
	}
	if err := checkID("assignmentId", g.AssignmentID); err != nil {
		return err
	}
	if len(g.URL) < 1 || len(g.URL) > maxURL || !utf8.ValidString(g.URL) {
		return fmt.Errorf("url must be 1-%d bytes of UTF-8", maxURL)
	}
	u, err := url.Parse(g.URL)
	if err != nil || !u.IsAbs() || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return fmt.Errorf("url must be an absolute http or https URL")
	}
	if _, err := g.Expiry(); err != nil {
		return err
	}
	return nil
}

// Expiry parses expiresAt.
func (g Grant) Expiry() (time.Time, error) {
	t, err := time.Parse(time.RFC3339, g.ExpiresAt)
	if err != nil {
		return time.Time{}, fmt.Errorf("expiresAt must be an RFC 3339 timestamp: %w", err)
	}
	return t.UTC(), nil
}

// Validate checks an update.cancel.
func (c Cancel) Validate() error {
	if c.V != Version {
		return fmt.Errorf("unsupported version %d", c.V)
	}
	if c.Kind != KindCancel {
		return fmt.Errorf("unknown kind %q", c.Kind)
	}
	if c.AttemptID != "" {
		if err := checkID("attemptId", c.AttemptID); err != nil {
			return err
		}
	}
	return checkID("assignmentId", c.AssignmentID)
}

// Validate checks a report against the wire grammar, in both directions: a field that must be
// present is required, and a field that does not belong to the kind or stage is refused.
func (r Report) Validate() error {
	if r.V != Version {
		return fmt.Errorf("unsupported version %d", r.V)
	}
	if r.Kind != KindProgress && r.Kind != KindInventory {
		return fmt.Errorf("unknown report kind %q", r.Kind)
	}
	if err := checkComponent(r.Component); err != nil {
		return err
	}
	if err := checkBootID(r.BootID); err != nil {
		return err
	}
	if r.Kind == KindInventory {
		if r.AttemptID != "" || r.AssignmentID != "" || r.Seq != 0 || r.Stage != "" ||
			r.ArtifactDigest != "" || r.Progress != nil || r.Result != nil {
			return fmt.Errorf("an update.inventory carries only component, running and bootId")
		}
		return checkRunning(r.Running)
	}
	for _, f := range []struct{ n, v string }{{"attemptId", r.AttemptID}, {"assignmentId", r.AssignmentID}} {
		if err := checkID(f.n, f.v); err != nil {
			return err
		}
	}
	if err := checkDigest("artifactDigest", r.ArtifactDigest, true); err != nil {
		return err
	}
	if r.Seq < 1 {
		return fmt.Errorf("seq must be at least 1 on an update.progress")
	}
	if StageRank(r.Stage) == 0 {
		return fmt.Errorf("unknown stage %q", r.Stage)
	}
	if r.Progress != nil {
		if r.Stage != StageDownloading {
			return fmt.Errorf("progress is only allowed when stage is DOWNLOADING")
		}
		p := r.Progress
		if p.Bytes < 0 || p.Total < 0 || (p.Total > 0 && p.Bytes > p.Total) {
			return fmt.Errorf("progress bytes %d and total %d are inconsistent", p.Bytes, p.Total)
		}
	}
	if r.Stage == StageFailed {
		if r.Result == nil {
			return fmt.Errorf("result is required when stage is FAILED")
		}
		if err := checkLen("result.code", r.Result.Code, maxCode); err != nil {
			return err
		}
		if !codeRE.MatchString(r.Result.Code) {
			return fmt.Errorf("result.code %q must match %s", r.Result.Code, codeRE)
		}
		if len(r.Result.Detail) > maxDetail || !utf8.ValidString(r.Result.Detail) {
			return fmt.Errorf("result.detail must be at most %d bytes of UTF-8", maxDetail)
		}
	} else if r.Result != nil {
		return fmt.Errorf("result is only allowed when stage is FAILED")
	}
	if r.Stage == StageRunning {
		return checkRunning(r.Running)
	}
	if r.Running != nil {
		return fmt.Errorf("running is only allowed when stage is RUNNING")
	}
	return nil
}

func checkRunning(run *Running) error {
	if run == nil {
		return fmt.Errorf("running is required for this report")
	}
	if err := checkLen("running.version", run.Version, maxVersion); err != nil {
		return err
	}
	if err := checkPrintable("running.version", run.Version); err != nil {
		return err
	}
	return checkDigest("running.digest", run.Digest, false)
}

func checkComponent(c string) error {
	if err := checkLen("component", c, maxComponent); err != nil {
		return err
	}
	if !componentRE.MatchString(c) {
		return fmt.Errorf("component %q must match %s", c, componentRE)
	}
	return nil
}

func checkBootID(id string) error {
	if err := checkLen("bootId", id, maxBootID); err != nil {
		return err
	}
	return checkPrintable("bootId", id)
}

func checkID(name, v string) error {
	if err := checkLen(name, v, maxID); err != nil {
		return err
	}
	if !idRE.MatchString(v) {
		return fmt.Errorf("%s %q is not a valid token", name, v)
	}
	return nil
}

func checkLen(name, v string, max int) error {
	if len(v) < 1 || len(v) > max {
		return fmt.Errorf("%s must be 1-%d bytes, got %d", name, max, len(v))
	}
	return nil
}

func checkPrintable(name, v string) error {
	if !utf8.ValidString(v) {
		return fmt.Errorf("%s is not valid UTF-8", name)
	}
	for _, c := range v {
		if c < 0x20 || c == 0x7f {
			return fmt.Errorf("%s contains a control character", name)
		}
	}
	return nil
}

func checkDigest(name, v string, required bool) error {
	if v == "" {
		if required {
			return fmt.Errorf("%s is required", name)
		}
		return nil
	}
	if !digestRE.MatchString(v) {
		return fmt.Errorf("%s must be sha256: followed by 64 lowercase hex characters", name)
	}
	return nil
}
