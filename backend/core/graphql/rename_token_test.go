// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"strings"
	"testing"
)

// The rename floor, at every boundary. It is small enough that this is exhaustive
// rather than representative, and it needs to be: it is the only thing standing
// between a rename mutation and a live record named by nothing.
func TestErrRenameTokenUnusable(t *testing.T) {
	for _, tc := range []struct {
		name     string
		token    string
		newToken string
		wantsErr bool
	}{
		// The whole point of a rename: a DIFFERENT token is the operation, not an error.
		// Without this row every case below would pass for a function that refused
		// everything.
		{"a differing token is a rename", "a", "b", false},
		{"the same token is an idempotent rename", "a", "a", false},
		// A rename onto a token that only DIFFERS BY CASE is a rename like any other.
		// It is here because the deleted reconcile rule treated case as its separating
		// case, and the two policies must not be confused as this one outlives the other.
		{"a case change is a rename", "a", "A", false},
		// The defect this closes. "" is a valid non-null String, so the schema lets it
		// through, and the old code wrote it straight onto the row.
		{"an empty token is refused", "a", "", true},
		// Trimmed, because this is a validity question about one string. The token
		// GRAMMAR does not catch a whitespace-only token, which is the hole the original
		// defect went through.
		{"a whitespace-only token is refused", "a", "   ", true},
		{"a tab-and-newline token is refused", "a", "\t\n ", true},
		// A padded token is NOT blank, so it is accepted here and refused by the grammar
		// instead — which says so plainly rather than silently storing "tok".
		{"a padded token is not blank", "a", " b ", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := ErrRenameTokenUnusable("thing", tc.token, tc.newToken)
			if tc.wantsErr && err == nil {
				t.Fatal("expected a refusal")
			}
			if !tc.wantsErr && err != nil {
				t.Fatalf("unexpected refusal: %v", err)
			}
		})
	}
}

// THE MESSAGE HAS TO NAME THE RECORD BEING RENAMED, because a caller renaming one of
// several channels in one script has nothing else to go on. It is asserted separately
// from the table above, which only ever reads err == nil and would pass just as
// happily for a refusal that said "invalid input".
//
// 🔴 IT ALSO HAS TO DESCRIBE THE RENAME ARGUMENT AND NOT A PAYLOAD FIELD. This
// function spent its first life guarding a token INSIDE a shared create input, and
// its message said so; every update input has since dropped its token, so a sentence
// about "the payload token" would send the one caller who reads it looking for a
// field their request does not have.
func TestTheRefusalNamesTheRecordAndTheRenameArgument(t *testing.T) {
	err := ErrRenameTokenUnusable("notification channel", "pager-duty", "")
	if err == nil {
		t.Fatal("a blank rename was accepted")
	}
	msg := err.Error()
	for _, want := range []string{"notification channel", `"pager-duty"`, "new token"} {
		if !strings.Contains(msg, want) {
			t.Errorf("the refusal does not mention %q: %s", want, msg)
		}
	}
	if strings.Contains(msg, "payload") {
		t.Errorf("the refusal still describes a payload token, which no update input "+
			"carries any more: %s", msg)
	}
}
