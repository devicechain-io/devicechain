// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import "testing"

// The two policies, at every boundary. They are small enough that this is
// exhaustive rather than representative, and they need to be: between them they
// decide which row six services write to.

func TestErrPayloadTokenDisagrees(t *testing.T) {
	for _, tc := range []struct {
		name     string
		token    string
		payload  string
		wantsErr bool
	}{
		{"agreeing tokens pass", "a", "a", false},
		// Under a shared create input, a caller with nothing to say about identity has
		// no other way to say it. The caller must not WRITE this value — see the
		// callers, which dropped the assignment.
		{"an empty payload token is unspecified", "a", "", false},
		{"a disagreeing payload token is refused", "a", "b", true},
		// The case the old code got exactly backwards: it located and updated "b".
		{"case matters", "a", "A", true},
		// Not trimmed, deliberately: a whitespace token equals no stored token, so it
		// belongs in the refusal branch rather than being silently read as "a".
		{"whitespace is a disagreement, not an omission", "a", "   ", true},
		{"a padded match is still a disagreement", "a", " a ", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := ErrPayloadTokenDisagrees("thing", tc.token, tc.payload)
			if tc.wantsErr && err == nil {
				t.Fatal("expected a refusal")
			}
			if !tc.wantsErr && err != nil {
				t.Fatalf("unexpected refusal: %v", err)
			}
		})
	}
}

func TestErrRenameTokenUnusable(t *testing.T) {
	for _, tc := range []struct {
		name     string
		token    string
		payload  string
		wantsErr bool
	}{
		// The whole point of this class: a DIFFERENT token is a rename, not an error.
		{"a differing token is a rename", "a", "b", false},
		{"the same token is a no-op rename", "a", "a", false},
		// The defect this closes. "" is a valid non-null String, so the schema lets it
		// through, and the old code wrote it straight onto the row.
		{"an empty token is refused", "a", "", true},
		// Trimmed here, unlike the comparison above, because this is a validity
		// question about one string rather than a comparison between two.
		{"a whitespace-only token is refused", "a", "   ", true},
		{"a tab-and-newline token is refused", "a", "\t\n ", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := ErrRenameTokenUnusable("thing", tc.token, tc.payload)
			if tc.wantsErr && err == nil {
				t.Fatal("expected a refusal")
			}
			if !tc.wantsErr && err != nil {
				t.Fatalf("unexpected refusal: %v", err)
			}
		})
	}
}

// The counterweight to both tables: the two policies must actually DIFFER on the
// case that separates them. If a refactor collapsed them into one function, every
// row above could still pass while three mutations lost their rename or six gained
// one.
func TestTheTwoPoliciesDisagreeWhereTheyMust(t *testing.T) {
	if ErrPayloadTokenDisagrees("thing", "a", "b") == nil {
		t.Error("the non-rename policy accepted a differing token")
	}
	if ErrRenameTokenUnusable("thing", "a", "b") != nil {
		t.Error("the rename policy refused a differing token")
	}
	if ErrPayloadTokenDisagrees("thing", "a", "") != nil {
		t.Error("the non-rename policy refused an empty token")
	}
	if ErrRenameTokenUnusable("thing", "a", "") == nil {
		t.Error("the rename policy accepted an empty token")
	}
}
