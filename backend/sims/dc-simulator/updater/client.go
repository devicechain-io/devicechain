// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package updater

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"
)

// The update half of the desired-state channel, as the device sees it: the platform assigns an
// update, answers a download request with a capability URL, and cancels. Like the rest of this
// package the device speaks the wire from its OWN structs, so the core contract golden fixtures
// are the cross-language check on it (client_mirror_test.go).
const (
	kindUpdateAssignment = "update.assignment"
	kindDownloadGrant    = "download.grant"
	kindUpdateCancel     = "update.cancel"
	kindDownloadRequest  = "download.request"
)

type wireAssignmentArtifact struct {
	Version        string `json:"version"`
	Digest         string `json:"digest"`
	Size           int64  `json:"size"`
	RequiresReboot bool   `json:"requiresReboot"`
}

type wireAssignment struct {
	V            int                    `json:"v"`
	Kind         string                 `json:"kind"`
	AttemptID    string                 `json:"attemptId"`
	AssignmentID string                 `json:"assignmentId"`
	Component    string                 `json:"component"`
	Trust        string                 `json:"trust"`
	Artifact     wireAssignmentArtifact `json:"artifact"`
	Signature    string                 `json:"signature,omitempty"`
	KeyID        string                 `json:"keyId,omitempty"`
	Algorithm    string                 `json:"algorithm,omitempty"`
}

type wireGrant struct {
	V            int    `json:"v"`
	Kind         string `json:"kind"`
	AssignmentID string `json:"assignmentId"`
	URL          string `json:"url"`
	ExpiresAt    string `json:"expiresAt"`
}

type wireCancel struct {
	V            int    `json:"v"`
	Kind         string `json:"kind"`
	AttemptID    string `json:"attemptId,omitempty"`
	AssignmentID string `json:"assignmentId"`
}

type wireDownloadRequest struct {
	V            int    `json:"v"`
	Kind         string `json:"kind"`
	AssignmentID string `json:"assignmentId"`
}

// ErrAwaitingGrant is returned by Drive after the device asked the platform for a download grant:
// nothing more can be done until HandleDesired delivers the answer.
var ErrAwaitingGrant = errors.New("updater: waiting for a download grant")

// ClientConfig configures a Client.
type ClientConfig struct {
	// StatePath, Component, InitialVersion and ChunkSize configure the Updater the client drives.
	StatePath      string
	Component      string
	InitialVersion string
	ChunkSize      int64
	// TrustedKeys and RequireSignature configure the Updater's authenticity check; see Config.
	TrustedKeys      []string
	RequireSignature *bool
	// Transport carries every message the device sends: update reports and download requests.
	Transport Transport
	// HTTP fetches the artifact. Nil uses a client with a 30 second timeout.
	HTTP *http.Client
	// Now is the device clock, used to stop trusting an expired grant. Nil is time.Now.
	Now func() time.Time
}

// Client is a simulated device end to end for the update flow: it receives the platform desired
// messages, asks for a download grant, fetches the artifact with ranged requests from the offset
// the Updater persisted, and drives the Updater through install, reboot and confirmation. It adds no
// update logic of its own; the stages, the persistence and the faults are the Updater.
type Client struct {
	cfg    ClientConfig
	up     *Updater
	grants *grantBox

	mu        sync.Mutex
	requested bool // a download.request is outstanding
}

// OpenClient opens the client and the Updater it drives over the same state file.
func OpenClient(cfg ClientConfig) (*Client, error) {
	if cfg.Transport == nil {
		return nil, errors.New("updater: a client needs a Transport")
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	httpc := cfg.HTTP
	if httpc == nil {
		httpc = &http.Client{Timeout: 30 * time.Second}
	}
	boxed := &grantBox{now: cfg.Now}
	up, err := Open(Config{
		StatePath: cfg.StatePath, Component: cfg.Component, Transport: cfg.Transport,
		Source: &httpSource{client: httpc, grants: boxed}, ChunkSize: cfg.ChunkSize,
		InitialVersion: cfg.InitialVersion, TrustedKeys: cfg.TrustedKeys, RequireSignature: cfg.RequireSignature,
	})
	if err != nil {
		return nil, err
	}
	return &Client{cfg: cfg, up: up, grants: boxed}, nil
}

// Updater is the underlying device updater, for fault injection and inspection.
func (c *Client) Updater() *Updater { return c.up }

// HandleDesired takes one message from the device-desired channel. A kind that is not in the update
// family is not this client business and returns ErrNotUpdateKind, so a caller that shares the
// channel with a ConfigAgent can route on it.
func (c *Client) HandleDesired(ctx context.Context, payload []byte) error {
	var head struct {
		Kind string `json:"kind"`
	}
	if err := json.Unmarshal(payload, &head); err != nil {
		return fmt.Errorf("updater: undecodable desired message: %w", err)
	}
	switch head.Kind {
	case kindUpdateAssignment:
		var m wireAssignment
		if err := strictJSON(payload, &m); err != nil {
			return err
		}
		if m.V != wireVersion || m.Trust == "" {
			return fmt.Errorf("updater: unsupported update.assignment (v=%d trust=%q)", m.V, m.Trust)
		}
		if err := checkWireTrust(m); err != nil {
			return err
		}
		return c.up.Assign(Assignment{
			AttemptID: m.AttemptID, AssignmentID: m.AssignmentID, Component: m.Component,
			ArtifactDigest: m.Artifact.Digest, Version: m.Artifact.Version, Size: m.Artifact.Size,
			RequiresReboot: m.Artifact.RequiresReboot,
			Trust:          m.Trust, Signature: m.Signature, KeyID: m.KeyID, Algorithm: m.Algorithm,
		})
	case kindDownloadGrant:
		var m wireGrant
		if err := strictJSON(payload, &m); err != nil {
			return err
		}
		exp, err := time.Parse(time.RFC3339, m.ExpiresAt)
		if err != nil {
			return fmt.Errorf("updater: download.grant expiresAt: %w", err)
		}
		if cur := c.up.Snapshot(); cur.AttemptID == "" || !c.holds(m.AssignmentID) {
			return nil // a grant for an assignment this device is not working on
		}
		c.grants.set(grant{url: m.URL, expires: exp})
		c.mu.Lock()
		c.requested = false
		c.mu.Unlock()
		return nil
	case kindUpdateCancel:
		var m wireCancel
		if err := strictJSON(payload, &m); err != nil {
			return err
		}
		if !c.holds(m.AssignmentID) {
			return nil
		}
		err := c.up.Abandon(ctx)
		if errors.Is(err, ErrPastRecall) || errors.Is(err, ErrFinished) || errors.Is(err, ErrNoAttempt) {
			return nil
		}
		return err
	default:
		return ErrNotUpdateKind
	}
}

// checkWireTrust refuses an assignment whose trust and signature fields disagree: SIGNED carries
// all three of signature, keyId and algorithm, UNVERIFIED carries none.
func checkWireTrust(m wireAssignment) error {
	has := m.Signature != "" || m.KeyID != "" || m.Algorithm != ""
	switch {
	case m.Trust == trustUnverified && !has:
		return nil
	case m.Trust == trustSigned && m.Signature != "" && m.KeyID != "" && m.Algorithm != "":
		return nil
	}
	return fmt.Errorf("updater: update.assignment trust %q does not match its signature fields", m.Trust)
}

// ErrNotUpdateKind is returned by HandleDesired for a message outside the update family.
var ErrNotUpdateKind = errors.New("updater: not an update message")

// holds reports whether the device is currently working on the assignment.
func (c *Client) holds(assignmentID string) bool {
	c.up.lock()
	defer c.up.unlock()
	a := c.up.st.Attempt
	return a != nil && a.AssignmentID == assignmentID
}

// currentAssignment is the assignment id of the attempt in hand, or "".
func (c *Client) currentAssignment() string {
	c.up.lock()
	defer c.up.unlock()
	if a := c.up.st.Attempt; a != nil {
		return a.AssignmentID
	}
	return ""
}

// RequestGrant asks the platform for a download grant for the current assignment. The request is
// sent at most once until an answer arrives, so a device that polls does not flood the channel.
func (c *Client) RequestGrant(ctx context.Context) error {
	c.mu.Lock()
	if c.requested {
		c.mu.Unlock()
		return nil
	}
	c.requested = true
	c.mu.Unlock()
	id := c.currentAssignment()
	if id == "" {
		return ErrNoAttempt
	}
	raw, err := json.Marshal(wireDownloadRequest{V: wireVersion, Kind: kindDownloadRequest, AssignmentID: id})
	if err != nil {
		return err
	}
	if err := c.cfg.Transport.Send(ctx, raw); err != nil {
		c.mu.Lock()
		c.requested = false
		c.mu.Unlock()
		return err
	}
	return nil
}

// ForgetRequest allows the next Drive to ask for a grant again. It models a device whose request
// or answer was lost, which is the only reason to ask twice.
func (c *Client) ForgetRequest() {
	c.mu.Lock()
	c.requested = false
	c.mu.Unlock()
}

// Drive steps the Updater until it has nothing more to do, an error occurs, or maxSteps is reached.
// When the download needs a grant the device does not have (none yet, expired by its own clock, or
// refused by the platform) Drive sends one download.request and returns ErrAwaitingGrant; call it
// again after HandleDesired has delivered the answer. Any other error is returned as it is, with
// the Updater state intact, so a retry resumes at the persisted offset.
func (c *Client) Drive(ctx context.Context, maxSteps int) (done bool, err error) {
	for range maxSteps {
		done, err = c.up.Step(ctx)
		switch {
		case err == nil && done:
			return true, nil
		case err == nil:
			continue
		case errors.Is(err, ErrNoGrant) || errors.Is(err, ErrGrantRefused):
			if rerr := c.RequestGrant(ctx); rerr != nil {
				return false, rerr
			}
			return false, ErrAwaitingGrant
		default:
			return false, err
		}
	}
	return false, nil
}

// strictJSON decodes b into v, refusing unknown fields and anything after the value, the way a
// device that mirrors the contract must.
func strictJSON(b []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("updater: decode desired message: %w", err)
	}
	var extra json.RawMessage
	if err := dec.Decode(&extra); err != io.EOF {
		return errors.New("updater: decode desired message: trailing data")
	}
	return nil
}
