// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package updater

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
)

// The configuration half of the device contract: a simulated device that announces itself with a
// sync, receives the platform's desired configuration, checks it, applies it and reports back.
//
// Like the updater it speaks the wire from its OWN structs, never the platform's types, so the
// golden fixtures in the core contract package are the cross-language check on it
// (config_mirror_test.go). It carries no transport of its own: reports go out through the same
// Transport interface the updater uses, and desired messages are handed to HandleDesired by
// whatever delivers them.
const (
	kindSync           = "sync"
	kindConfigStatus   = "config.status"
	kindConfigRevision = "config.revision"

	configReceived = "RECEIVED"
	configApplied  = "APPLIED"
	configRejected = "REJECTED"

	// maxConfigErrors and maxConfigErrorBytes bound a REJECTED report's error list as the
	// contract does.
	maxConfigErrors     = 16
	maxConfigErrorBytes = 256

	configStateVersion = 1
)

type wireSync struct {
	V      int         `json:"v"`
	Kind   string      `json:"kind"`
	BootID string      `json:"bootId,omitempty"`
	Config *wireSyncOf `json:"config"`
}

type wireSyncOf struct {
	HaveRevision int64 `json:"haveRevision"`
}

type wireConfigStatus struct {
	V        int      `json:"v"`
	Kind     string   `json:"kind"`
	Revision int64    `json:"revision"`
	Digest   string   `json:"digest"`
	Status   string   `json:"status"`
	Errors   []string `json:"errors,omitempty"`
}

type wireDesired struct {
	V        int    `json:"v"`
	Kind     string `json:"kind"`
	Revision int64  `json:"revision"`
	Digest   string `json:"digest"`
	Document string `json:"document"`
}

// ErrCorruptConfigState is returned when the configuration state file exists but cannot be
// trusted. The agent refuses to start over it, for the reason ErrCorruptState gives.
var ErrCorruptConfigState = errors.New("updater: configuration state file is corrupt (delete it to reset the simulated device)")

// ConfigValidator is the device's own opinion of a document it was sent: nil means it can apply
// it, otherwise the reasons it cannot. It is how a test makes a device REJECT a revision.
type ConfigValidator func(document string) []string

// ConfigAgentConfig configures a ConfigAgent.
type ConfigAgentConfig struct {
	// StatePath is the device-local file holding the configuration the device applied. Empty
	// keeps it in memory only, a device that forgets everything when it restarts.
	StatePath string
	// Transport carries the device's messages toward the platform.
	Transport Transport
	// BootID is announced on a sync when set.
	BootID string
	// Validate, when set, may refuse a document.
	Validate ConfigValidator
}

type configState struct {
	V        int    `json:"v"`
	Revision int64  `json:"revision"`
	Digest   string `json:"digest"`
	Document string `json:"document"`
}

// ConfigAgent is the device-side configuration client.
type ConfigAgent struct {
	cfg ConfigAgentConfig

	mu   sync.Mutex
	held configState
}

// OpenConfigAgent opens the agent, loading what it had applied if its state file exists.
func OpenConfigAgent(cfg ConfigAgentConfig) (*ConfigAgent, error) {
	if cfg.Transport == nil {
		return nil, errors.New("updater: a configuration agent needs a Transport")
	}
	a := &ConfigAgent{cfg: cfg}
	if cfg.StatePath == "" {
		return a, nil
	}
	raw, err := os.ReadFile(cfg.StatePath)
	if errors.Is(err, os.ErrNotExist) {
		return a, nil
	}
	if err != nil {
		return nil, fmt.Errorf("updater: read configuration state %s: %w", cfg.StatePath, err)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var st configState
	if err := dec.Decode(&st); err != nil {
		return nil, fmt.Errorf("%w: %s: %v", ErrCorruptConfigState, cfg.StatePath, err)
	}
	var extra json.RawMessage
	if err := dec.Decode(&extra); err != io.EOF {
		return nil, fmt.Errorf("%w: %s: trailing data", ErrCorruptConfigState, cfg.StatePath)
	}
	if st.V != configStateVersion || st.Revision < 1 || st.Digest != digestOf(st.Document) {
		return nil, fmt.Errorf("%w: %s: inconsistent contents", ErrCorruptConfigState, cfg.StatePath)
	}
	a.held = st
	return a, nil
}

// Held returns the configuration the device has applied: revision 0 when it holds none.
func (a *ConfigAgent) Held() (revision int64, digest, document string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.held.Revision, a.held.Digest, a.held.Document
}

// Sync announces the device and what it holds. A device calls it every time it connects; the
// platform answers with the current revision, whether or not it is the one the device holds.
func (a *ConfigAgent) Sync(ctx context.Context) error {
	rev, _, _ := a.Held()
	b, err := json.Marshal(wireSync{V: wireVersion, Kind: kindSync, BootID: a.cfg.BootID,
		Config: &wireSyncOf{HaveRevision: rev}})
	if err != nil {
		return err
	}
	return a.cfg.Transport.Send(ctx, b)
}

// HandleDesired takes one desired-configuration message from the platform. It decodes strictly,
// hashes the document bytes it was sent and compares them with the digest before anything else,
// reports RECEIVED, and then either applies the document and reports APPLIED, or refuses it and
// reports REJECTED with the reasons.
//
// Re-delivery is harmless: a revision the device already holds is not applied again, only
// reported APPLIED again (the platform may have lost the first report), and a revision older
// than the one it holds is ignored. A send that fails returns the error with the state as it
// was left, so calling HandleDesired again with the same message continues from there.
func (a *ConfigAgent) HandleDesired(ctx context.Context, payload []byte) error {
	var d wireDesired
	dec := json.NewDecoder(bytes.NewReader(payload))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&d); err != nil {
		return fmt.Errorf("updater: desired configuration does not decode: %w", err)
	}
	var extra json.RawMessage
	if err := dec.Decode(&extra); err != io.EOF {
		return errors.New("updater: desired configuration has trailing data")
	}
	if d.V != wireVersion || d.Kind != kindConfigRevision || d.Revision < 1 || d.Digest == "" || d.Document == "" {
		return errors.New("updater: desired configuration is not a valid config.revision message")
	}

	a.mu.Lock()
	heldRev, heldDigest := a.held.Revision, a.held.Digest
	a.mu.Unlock()
	switch {
	case d.Revision < heldRev:
		return nil
	case d.Revision == heldRev && d.Digest == heldDigest:
		return a.report(ctx, d, configApplied, nil)
	}

	if err := a.report(ctx, d, configReceived, nil); err != nil {
		return err
	}
	if digestOf(d.Document) != d.Digest {
		return a.report(ctx, d, configRejected, []string{"digest does not match the document"})
	}
	if a.cfg.Validate != nil {
		if reasons := a.cfg.Validate(d.Document); len(reasons) > 0 {
			return a.report(ctx, d, configRejected, reasons)
		}
	}
	// Persist before reporting: a device that dies between the two re-reports APPLIED when it
	// is next sent the revision, and never reports one it did not keep.
	if err := a.apply(d); err != nil {
		return err
	}
	return a.report(ctx, d, configApplied, nil)
}

func (a *ConfigAgent) apply(d wireDesired) error {
	st := configState{V: configStateVersion, Revision: d.Revision, Digest: d.Digest, Document: d.Document}
	if a.cfg.StatePath != "" {
		raw, err := json.Marshal(st)
		if err != nil {
			return err
		}
		if err := writeFileAtomic(a.cfg.StatePath, raw); err != nil {
			return err
		}
	}
	a.mu.Lock()
	a.held = st
	a.mu.Unlock()
	return nil
}

func (a *ConfigAgent) report(ctx context.Context, d wireDesired, status string, reasons []string) error {
	b, err := json.Marshal(wireConfigStatus{V: wireVersion, Kind: kindConfigStatus, Revision: d.Revision,
		Digest: d.Digest, Status: status, Errors: boundConfigErrors(reasons)})
	if err != nil {
		return err
	}
	return a.cfg.Transport.Send(ctx, b)
}

func digestOf(document string) string {
	sum := sha256.Sum256([]byte(document))
	return "sha256:" + hex.EncodeToString(sum[:])
}

// boundConfigErrors fits a reason list inside the contract: at most 16 entries of 1-256 bytes of
// valid UTF-8 without control characters.
func boundConfigErrors(in []string) []string {
	var out []string
	for _, e := range in {
		if len(out) == maxConfigErrors {
			break
		}
		e = strings.Map(func(r rune) rune {
			if r < 0x20 || r == 0x7f {
				return ' '
			}
			return r
		}, strings.ToValidUTF8(e, "?"))
		if len(e) > maxConfigErrorBytes {
			e = e[:maxConfigErrorBytes]
			for !isValidUTF8(e) {
				e = e[:len(e)-1]
			}
		}
		if e != "" {
			out = append(out, e)
		}
	}
	if len(out) == 0 && len(in) > 0 {
		out = []string{"rejected"}
	}
	return out
}

func isValidUTF8(s string) bool { return strings.ToValidUTF8(s, "") == s }
