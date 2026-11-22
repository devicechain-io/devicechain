// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package ledger

import (
	"sort"
	"strings"
)

// Overrides binds a reversal to its original operation, its ordered line
// selection and its override set. A nil selection means all original posted
// lines; an empty selection means none. OverrideOrdinals is an unordered set.
type Overrides struct {
	ReversalOf       *int64  `json:"reversalOf"`
	LineSelection    []int64 `json:"lineSelection"`
	OverrideOrdinals []int64 `json:"overrideOrdinals"`
}

// RequestDigestInput is the replay identity; runtime state and before-images
// cannot enter it. Intent must be a JSON-compatible value.
type RequestDigestInput struct {
	Kind         string    `json:"kind"`
	Intent       any       `json:"intent"`
	TargetTokens []string  `json:"targetTokens"`
	Safety       Safety    `json:"safety"`
	Overrides    Overrides `json:"overrides"`
}

// RequestDigest trims, deduplicates and sorts target tokens on a private copy.
// Token spelling after trimming is otherwise exact, including case and Unicode.
func RequestDigest(v RequestDigestInput) (string, error) {
	tokens := make([]string, 0, len(v.TargetTokens))
	seen := make(map[string]bool, len(v.TargetTokens))
	for _, token := range v.TargetTokens {
		token = strings.TrimSpace(token)
		if !seen[token] {
			seen[token] = true
			tokens = append(tokens, token)
		}
	}
	sort.Strings(tokens)
	v.TargetTokens = tokens
	v.Overrides = normalizeOverrides(v.Overrides)
	return Digest(v)
}

// PlanLine contains exactly the plan tuple. Admission is "ADMITTED" or a refusal
// code. TargetID is nil when unresolved. There is deliberately no before-image.
type PlanLine struct {
	Ordinal   int64
	TargetID  *int64
	Admission string
}

// PlanDigestInput preserves the planner's admitted ordinal order.
type PlanDigestInput struct {
	Lines []PlanLine
}

// PlanDigest hashes ordered (ordinal, targetID|null, admitted|refusal-code)
// tuples. It neither resolves targets nor reorders lines.
func PlanDigest(v PlanDigestInput) (string, error) {
	tuples := make([][3]any, 0, len(v.Lines))
	for _, line := range v.Lines {
		tuples = append(tuples, [3]any{line.Ordinal, line.TargetID, line.Admission})
	}
	return Digest(tuples)
}

// Approval identifies the approval policy bound by the execution contract.
type Approval struct {
	Kind          string `json:"kind"`
	PolicyVersion int64  `json:"policyVersion"`
}

// ContractDigestInput is the complete H05 execution-contract envelope. Every
// field, including explicit null ceilings and reversal selection, is covered.
type ContractDigestInput struct {
	V          int64     `json:"v"`
	Kind       string    `json:"kind"`
	Intent     any       `json:"intent"`
	PlanDigest string    `json:"planDigest"`
	Safety     Safety    `json:"safety"`
	Overrides  Overrides `json:"overrides"`
	Approval   Approval  `json:"approval"`
}

// ContractDigest binds approval to both the frozen plan and the safety policy.
func ContractDigest(v ContractDigestInput) (string, error) {
	v.Overrides = normalizeOverrides(v.Overrides)
	return Digest(v)
}

func normalizeOverrides(v Overrides) Overrides {
	ordinals := append([]int64{}, v.OverrideOrdinals...)
	sort.Slice(ordinals, func(i, j int) bool { return ordinals[i] < ordinals[j] })
	unique := ordinals[:0]
	for _, ordinal := range ordinals {
		if len(unique) == 0 || unique[len(unique)-1] != ordinal {
			unique = append(unique, ordinal)
		}
	}
	v.OverrideOrdinals = unique
	return v
}
