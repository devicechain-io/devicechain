// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package admission

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"
	"unicode/utf8"
)

const (
	// MaxTargets bounds one proposal's target set, whatever a policy allows.
	MaxTargets = 10000
	// MaxNameBytes bounds the submitter, tenant, action and each target identifier.
	MaxNameBytes = 256

	proposalDomain = "devicechain.admission.proposal.v1\n"
	digestPrefix   = "sha256:"
)

// ErrMalformedProposal is wrapped by every error that says a proposal cannot be digested at
// all. A malformed proposal gets no receipt, not even a refusal: there is nothing to bind one
// to.
var ErrMalformedProposal = errors.New("malformed proposal")

// Proposal is what a head-end intends to do: one action kind, applied to a set of targets,
// with arguments, inside a time window [NotBefore, NotAfter).
type Proposal struct {
	// Submitter names the system asking, for example "cms:city-a".
	Submitter string `json:"submitter"`
	// Tenant is the DeviceChain tenant whose assets the action reaches.
	Tenant string `json:"tenant"`
	// Action is the action kind, for example "lighting.dim". A policy rule is keyed by it.
	Action string `json:"action"`
	// Targets names every asset the action reaches. Order does not matter; duplicates are
	// refused, because a duplicated target is either a mistake or an attempt to make two
	// spellings of one proposal.
	Targets []string `json:"targets"`
	// Parameters are the action's arguments as a JSON document; absent means null.
	Parameters json.RawMessage `json:"parameters,omitempty"`
	// NotBefore and NotAfter bound when the action may run: from NotBefore, up to but not
	// including NotAfter.
	NotBefore time.Time `json:"notBefore"`
	NotAfter  time.Time `json:"notAfter"`
}

// canonicalProposal is the digested form. Its field order is fixed by the struct, times are
// UTC text, targets are sorted and parameters are canonical, so equal intent gives equal bytes.
type canonicalProposal struct {
	V          int             `json:"v"`
	Submitter  string          `json:"submitter"`
	Tenant     string          `json:"tenant"`
	Action     string          `json:"action"`
	Targets    []string        `json:"targets"`
	Parameters json.RawMessage `json:"parameters"`
	NotBefore  string          `json:"notBefore"`
	NotAfter   string          `json:"notAfter"`
}

func malformed(field, format string, args ...any) error {
	return fmt.Errorf("%w: %s %s", ErrMalformedProposal, field, fmt.Sprintf(format, args...))
}

func checkName(field, v string) error {
	switch {
	case v == "":
		return malformed(field, "is empty")
	case len(v) > MaxNameBytes:
		return malformed(field, "is longer than %d bytes", MaxNameBytes)
	case !utf8.ValidString(v):
		return malformed(field, "is not valid UTF-8")
	}
	return nil
}

func utcText(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }

func (p Proposal) canonical() (canonicalProposal, error) {
	for _, f := range []struct{ name, v string }{
		{"submitter", p.Submitter}, {"tenant", p.Tenant}, {"action", p.Action},
	} {
		if err := checkName(f.name, f.v); err != nil {
			return canonicalProposal{}, err
		}
	}
	if len(p.Targets) == 0 {
		return canonicalProposal{}, malformed("targets", "is empty")
	}
	if len(p.Targets) > MaxTargets {
		return canonicalProposal{}, malformed("targets", "holds more than %d entries", MaxTargets)
	}
	targets := append([]string(nil), p.Targets...)
	sort.Strings(targets)
	for i, t := range targets {
		if err := checkName("target", t); err != nil {
			return canonicalProposal{}, err
		}
		if i > 0 && targets[i-1] == t {
			return canonicalProposal{}, malformed("targets", "name %q more than once", t)
		}
	}
	params, err := canonicalJSON(p.Parameters)
	if err != nil {
		return canonicalProposal{}, malformed("parameters", "%v", err)
	}
	if p.NotBefore.IsZero() || p.NotAfter.IsZero() {
		return canonicalProposal{}, malformed("window", "is missing a bound")
	}
	if !p.NotAfter.After(p.NotBefore) {
		return canonicalProposal{}, malformed("window", "ends at or before it starts")
	}
	return canonicalProposal{
		V: 1, Submitter: p.Submitter, Tenant: p.Tenant, Action: p.Action,
		Targets: targets, Parameters: params,
		NotBefore: utcText(p.NotBefore), NotAfter: utcText(p.NotAfter),
	}, nil
}

// Digest is the proposal's identity: "sha256:" and the hex SHA-256 of its canonical form under
// a domain prefix. Two proposals with the same intent (targets in another order, parameters
// spelled with other whitespace or key order, times in another zone) have the same digest.
func (p Proposal) Digest() (string, error) {
	c, err := p.canonical()
	if err != nil {
		return "", err
	}
	return digestOf(proposalDomain, c)
}

func digestOf(domain string, v any) (string, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(append([]byte(domain), b...))
	return digestPrefix + hex.EncodeToString(sum[:]), nil
}
