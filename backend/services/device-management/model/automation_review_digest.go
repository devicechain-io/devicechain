// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"sort"

	"github.com/devicechain-io/dc-microservice/actiongate"
)

// SnapshotDigest excludes row identity, save timestamps and database row order,
// but keeps all semantic fields. Numeric normalization also survives PostgreSQL jsonb.
func SnapshotDigest(raw []byte) (string, error) {
	x, err := reviewJSON(raw)
	if err != nil {
		return "", err
	}
	m, ok := x.(map[string]any)
	if !ok {
		return "", fmt.Errorf("profile snapshot must be an object")
	}
	for _, key := range []string{"metrics", "commands", "rules"} {
		if m[key] == nil {
			m[key] = []any{}
		}
		list, ok := m[key].([]any)
		if !ok {
			return "", fmt.Errorf("snapshot %s must be a list", key)
		}
		identity := map[string]string{"rules": "Token", "metrics": "MetricKey", "commands": "CommandKey"}[key]
		type element struct {
			value any
			key   string
			bytes []byte
		}
		elements := make([]element, len(list))
		for i, item := range list {
			stripReviewBookkeeping(item)
			canonical, err := reviewCanonical(item)
			if err != nil {
				return "", err
			}
			m, _ := item.(map[string]any)
			k, _ := m[identity].(string)
			elements[i] = element{value: item, key: k, bytes: canonical}
		}
		// Loader order is incidental; canonical bytes also stabilize duplicate keys.
		sort.Slice(elements, func(i, j int) bool {
			if elements[i].key != elements[j].key {
				return elements[i].key < elements[j].key
			}
			return bytes.Compare(elements[i].bytes, elements[j].bytes) < 0
		})
		for i, element := range elements {
			list[i] = element.value
		}
	}
	b, err := actiongate.Canonical(m)
	if err != nil {
		return "", err
	}
	return reviewHash(b), nil
}

func stripReviewBookkeeping(item any) {
	if m, ok := item.(map[string]any); ok {
		for _, k := range []string{"ID", "CreatedAt", "UpdatedAt", "DeletedAt", "TenantId", "DeviceProfileId", "DeviceProfile"} {
			delete(m, k)
		}
	}
}

func reviewHash(raw []byte) string {
	sum := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// reviewJSON uses exact decimal arithmetic: converting through float64 would hide
// edits to large integers. jsonb preserves values but may expand exponent syntax.
func reviewJSON(raw []byte) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		return nil, fmt.Errorf("expected one JSON value")
	}
	return normalizeReviewNumbers(v), nil
}

func normalizeReviewNumbers(v any) any {
	switch x := v.(type) {
	case json.Number:
		// All JSON numbers have finite decimal expansions. A rational avoids rounding.
		r, ok := new(big.Rat).SetString(string(x))
		if !ok {
			return x
		}
		d := new(big.Int).Set(r.Denom())
		two, five := big.NewInt(2), big.NewInt(5)
		n2, n5 := 0, 0
		for new(big.Int).Mod(d, two).Sign() == 0 {
			d.Div(d, two)
			n2++
		}
		for new(big.Int).Mod(d, five).Sign() == 0 {
			d.Div(d, five)
			n5++
		}
		if n5 > n2 {
			n2 = n5
		}
		return json.Number(r.FloatString(n2))
	case map[string]any:
		for k, value := range x {
			x[k] = normalizeReviewNumbers(value)
		}
	case []any:
		for i, value := range x {
			x[i] = normalizeReviewNumbers(value)
		}
	}
	return v
}

func reviewCanonical(v any) ([]byte, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	x, err := reviewJSON(b)
	if err != nil {
		return nil, err
	}
	return actiongate.Canonical(x)
}

func reviewElement(v any) ([]byte, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	x, err := reviewJSON(b)
	if err != nil {
		return nil, err
	}
	stripReviewBookkeeping(x)
	return actiongate.Canonical(x)
}

// reviewDigestOf binds the evidence to the tenant, profile and exact draft.
func reviewDigestOf(r *AutomationReview, profileToken string) (string, error) {
	b, err := actiongate.Canonical(map[string]any{
		"tenant": r.TenantId, "profileToken": profileToken, "kind": r.Kind,
		"ruleToken": r.RuleToken, "draftDigest": r.DraftDigest,
		"baselineActivationId": r.BaselineActivationId, "baselineDigest": r.BaselineDigest,
		"scopeDigest": r.ScopeDigest, "streamBounds": r.StreamBounds,
		"expectationsDigest": r.ExpectationsDigest, "resultDigest": r.ResultDigest,
		"inputDigest": r.InputDigest, "outcome": r.Outcome, "profileChanges": r.ProfileChanges,
		"draftAuthors": r.DraftAuthors,
	})
	if err != nil {
		return "", err
	}
	return reviewHash(b), nil
}

// ReviewDigestOf exposes the same binding to callers outside the model package.
func ReviewDigestOf(r *AutomationReview, profileToken string) (string, error) {
	return reviewDigestOf(r, profileToken)
}

// The binding covers digest fields, so approval must also verify the evidence
// those fields describe rather than trusting a digest beside altered text.
func reviewEvidenceVerifies(r *AutomationReview) bool {
	if !reviewResultDigestEqual(reviewHash([]byte(r.Result)), r.ResultDigest) {
		return false
	}
	for _, evidence := range []struct {
		raw    []byte
		digest string
	}{{r.Scope, r.ScopeDigest}, {r.Expectations, r.ExpectationsDigest}} {
		canonical, err := actiongate.Canonical(json.RawMessage(evidence.raw))
		if err != nil || !actiongate.DigestEqual(reviewHash(canonical), evidence.digest) {
			return false
		}
	}
	return true
}
