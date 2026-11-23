// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/devicechain-io/dc-microservice/actiongate"
	"github.com/devicechain-io/dc-microservice/core"
)

type commandIntent struct {
	DeviceToken    string          `json:"deviceToken"`
	Command        string          `json:"command"`
	Payload        json.RawMessage `json:"payload"`
	PayloadPresent bool            `json:"payloadPresent"`
	ExpiresAt      *string         `json:"expiresAt"`
}
type commandDependencies struct {
	Incarnation string `json:"incarnation"`
	Definition  string `json:"definition"`
	Impact      string `json:"impact"`
}
type commandEffective struct {
	Cutoff string `json:"cutoff"`
	commandDependencies
}
type commandForm struct {
	V         int              `json:"v"`
	Intent    commandIntent    `json:"intent"`
	Effective commandEffective `json:"effective"`
}

// walkJSON builds values from Tokens, rejecting duplicate keys at EVERY depth.
// UseNumber preserves the original decimal representation, including 1.10.
func walkJSON(d *json.Decoder) (any, error) {
	t, err := d.Token()
	if err != nil {
		return nil, err
	}
	if delim, ok := t.(json.Delim); ok {
		switch delim {
		case '{':
			m := map[string]any{}
			for d.More() {
				key, err := d.Token()
				if err != nil {
					return nil, err
				}
				k, ok := key.(string)
				if !ok {
					return nil, errors.New("invalid JSON key")
				}
				if _, exists := m[k]; exists {
					return nil, errors.New("duplicate JSON object key")
				}
				v, err := walkJSON(d)
				if err != nil {
					return nil, err
				}
				m[k] = v
			}
			end, err := d.Token()
			if err != nil || end != json.Delim('}') {
				return nil, errors.New("invalid JSON object")
			}
			return m, nil
		case '[':
			a := []any{}
			for d.More() {
				v, err := walkJSON(d)
				if err != nil {
					return nil, err
				}
				a = append(a, v)
			}
			end, err := d.Token()
			if err != nil || end != json.Delim(']') {
				return nil, errors.New("invalid JSON array")
			}
			return a, nil
		default:
			return nil, errors.New("invalid JSON delimiter")
		}
	}
	return t, nil
}
func canonicalJSON(raw []byte, objectOrNull bool) ([]byte, error) {
	if !utf8.Valid(raw) {
		return nil, errors.New("JSON must be valid UTF-8")
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	v, err := walkJSON(d)
	if err != nil {
		return nil, err
	}
	if _, err = d.Token(); err != io.EOF {
		return nil, errors.New("JSON must contain exactly one value")
	}
	if objectOrNull && v != nil {
		if _, ok := v.(map[string]any); !ok {
			return nil, errors.New("payload must be an object or null")
		}
		if err := storableValue(v); err != nil {
			return nil, err
		}
	}
	return actiongate.Canonical(v)
}

// storableValue refuses what the ledger's jsonb column would not store BYTE-FOR-BYTE. The
// approved effect is the stored canonical payload, and its digest is recomputed from what
// Postgres hands back: jsonb rewrites an exponent (1e2 -> 100, 0.10e1 -> 1.0) and a negative
// zero (-0 -> 0), and refuses a NUL in a string. Accepting either would store a proposal no
// one could ever approve (its digest would no longer verify), so it is refused here, where
// the agent can still write the value another way. Plain decimals, 1.10 included, survive.
func storableValue(v any) error {
	switch x := v.(type) {
	case json.Number:
		n := string(x)
		if strings.ContainsAny(n, "eE") {
			return errors.New("payload numbers must be plain decimals, without an exponent")
		}
		if strings.HasPrefix(n, "-") && strings.Trim(n, "-0.") == "" {
			return errors.New("payload numbers must not be a negative zero")
		}
	case string:
		if strings.ContainsRune(x, 0) {
			return errors.New("payload strings must not contain a NUL character")
		}
	case map[string]any:
		for k, e := range x {
			if strings.ContainsRune(k, 0) {
				return errors.New("payload keys must not contain a NUL character")
			}
			if err := storableValue(e); err != nil {
				return err
			}
		}
	case []any:
		for _, e := range x {
			if err := storableValue(e); err != nil {
				return err
			}
		}
	}
	return nil
}

// printableName is stricter than printable: every rune must be a printable Unicode character
// (unicode.IsPrint), so C1 controls (U+0085), bidirectional overrides (U+202E) and other
// format characters cannot hide in a command name a person reviews.
func printableName(s string) bool {
	if !utf8.ValidString(s) {
		return false
	}
	for _, r := range s {
		if !unicode.IsPrint(r) {
			return false
		}
	}
	return true
}
func printable(s string) bool {
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	return utf8.ValidString(s)
}
func normalizeCommandIntent(req CommandProposalRequest) (commandIntent, error) {
	i := commandIntent{DeviceToken: req.DeviceToken, Command: req.Name, Payload: json.RawMessage("null"), PayloadPresent: req.Payload != nil}
	if err := core.ValidateToken(req.DeviceToken); err != nil {
		return i, err
	}
	if req.Name == "" || !printableName(req.Name) {
		return i, errors.New("command name must be 1 to 128 bytes of printable text")
	}
	if err := validateCommandInputBounds(req.Name, req.Payload, nil); err != nil {
		return i, err
	}
	if len(req.RequestId) < 1 || len(req.RequestId) > MaxProposalRequestId || !printable(req.RequestId) {
		return i, errors.New("requestId must be 1 to 128 printable characters")
	}
	if req.Rationale != nil && (len(*req.Rationale) > MaxProposalRationale || !utf8.ValidString(*req.Rationale) || strings.ContainsRune(*req.Rationale, 0)) {
		return i, errors.New("rationale must be valid text of at most 2048 bytes")
	}
	if req.DeliveryMode != nil || req.MaxAttempts != nil {
		return i, actiongate.Refusal("DELIVERY_OPTIONS_NOT_SUPPORTED", "not available for agent proposals in this release", false)
	}
	if req.Payload != nil {
		p, err := canonicalJSON([]byte(*req.Payload), true)
		if err != nil {
			return i, err
		}
		i.Payload = p
	}
	if req.ExpiresAt != nil {
		exp, err := time.Parse(time.RFC3339, *req.ExpiresAt)
		if err != nil {
			return i, rejected(RejectExpiresAtInvalid, "expiresAt must be RFC3339")
		}
		s := exp.UTC().Truncate(time.Second).Format(time.RFC3339)
		i.ExpiresAt = &s
	}
	return i, nil
}
func commandCutoff(i commandIntent, now time.Time) (time.Time, error) {
	cutoff := now.Add(time.Hour).Truncate(time.Second)
	if i.ExpiresAt != nil {
		cutoff, _ = time.Parse(time.RFC3339, *i.ExpiresAt)
		if !cutoff.After(now.Add(time.Minute)) {
			return time.Time{}, actiongate.Refusal("EXPIRES_AT_TOO_SOON", "expiresAt must be more than one minute in the future", false)
		}
		if cutoff.After(now.Add(24 * time.Hour)) {
			return time.Time{}, actiongate.Refusal("EXPIRES_AT_TOO_FAR", "expiresAt must be within 24 hours", false)
		}
	}
	return cutoff, nil
}
func (f commandForm) canonical() ([]byte, []byte, []byte, string, error) {
	p, err := actiongate.Canonical(f)
	if err != nil {
		return nil, nil, nil, "", err
	}
	t, err := actiongate.Canonical([]string{f.Intent.DeviceToken})
	if err != nil {
		return nil, nil, nil, "", err
	}
	d, err := actiongate.Canonical(f.Effective.commandDependencies)
	if err != nil {
		return nil, nil, nil, "", err
	}
	return p, t, d, actiongate.Digest(actiongate.KindCommandSend, p, t), nil
}

// verifyCommandProposal is required before BOTH intermediate and final approval writes.
// The digest binds the effective dependency snapshot as well as submitted intent.
func verifyCommandProposal(p *AgentProposal) (*commandForm, error) {
	if p.Kind != string(actiongate.KindCommandSend) || p.TargetCount != 1 {
		return nil, errors.New("invalid stored command proposal kind or target count")
	}
	raw, err := canonicalJSON(p.Payload, false)
	if err != nil {
		return nil, err
	}
	targets, err := canonicalJSON(p.TargetSet, false)
	if err != nil {
		return nil, err
	}
	// PostgreSQL JSONB may add whitespace/reorder keys. Judge the recomputed
	// canonical form, never its database presentation. Numeric changes still
	// fail the digest check rather than silently changing approved effect bytes.
	if !actiongate.DigestEqual(actiongate.Digest(actiongate.KindCommandSend, raw, targets), p.Digest) {
		return nil, errors.New("corrupt stored command proposal digest")
	}
	var f commandForm
	if err := json.Unmarshal(raw, &f); err != nil {
		return nil, err
	}
	if f.V != 1 {
		return nil, fmt.Errorf("unsupported command proposal version %d", f.V)
	}
	if err := core.ValidateToken(f.Intent.DeviceToken); err != nil {
		return nil, err
	}
	if f.Intent.Command == "" || len(f.Intent.Command) > MaxCommandNameLength {
		return nil, errors.New("invalid stored command")
	}
	if len(f.Intent.Payload) == 0 {
		return nil, errors.New("missing stored payload")
	}
	canonicalPayload, err := canonicalJSON(f.Intent.Payload, true)
	if err != nil {
		return nil, err
	}
	f.Intent.Payload = canonicalPayload
	if !f.Intent.PayloadPresent && string(f.Intent.Payload) != "null" {
		return nil, errors.New("invalid absent payload")
	}
	if _, err := time.Parse(time.RFC3339, f.Effective.Cutoff); err != nil {
		return nil, err
	}
	if _, err := strconv.ParseUint(f.Effective.Incarnation, 10, 63); err != nil {
		return nil, err
	}
	if f.Effective.Definition == "" || f.Effective.Impact != p.Impact {
		return nil, errors.New("invalid stored dependencies")
	}
	_, expectedTargets, deps, _, err := f.canonical()
	if err != nil {
		return nil, err
	}
	storedDeps, err := canonicalJSON(p.Dependencies, false)
	if err != nil {
		return nil, err
	}
	if !bytes.Equal(expectedTargets, targets) || !bytes.Equal(deps, storedDeps) {
		return nil, errors.New("inconsistent stored command targets or dependencies")
	}
	return &f, nil
}
func (f *commandForm) preview(hold *DispatchHold) *CommandProposalPreview {
	inc, _ := strconv.ParseUint(f.Effective.Incarnation, 10, 63)
	cutoff, _ := time.Parse(time.RFC3339, f.Effective.Cutoff)
	return &CommandProposalPreview{DeviceToken: f.Intent.DeviceToken, Incarnation: inc, Command: f.Intent.Command, Payload: append([]byte(nil), f.Intent.Payload...), PayloadPresent: f.Intent.PayloadPresent, Impact: f.Effective.Impact, Definition: f.Effective.Definition, Cutoff: cutoff, Hold: hold}
}
