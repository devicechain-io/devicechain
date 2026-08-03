// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package rdb

import "testing"

// TestQuoteIdentifierSurvivesAHyphenatedSchema pins the case the platform actually has.
// Functional-area schema names carry hyphens, and an unquoted one is a subtraction
// rather than a name — the statement does not merely resolve wrongly, it fails to parse.
func TestQuoteIdentifierSurvivesAHyphenatedSchema(t *testing.T) {
	if got := QuoteIdentifier("device-management"); got != `"device-management"` {
		t.Errorf("QuoteIdentifier(device-management) = %s", got)
	}
	if got := QuoteIdentifier("notification-management"); got != `"notification-management"` {
		t.Errorf("QuoteIdentifier(notification-management) = %s", got)
	}
}

// TestQuoteIdentifierDoublesAnEmbeddedQuote pins the escaping rule. No schema in the
// platform contains a double quote, which is exactly why this is worth a test: the case
// that never occurs in practice is the one nobody notices is broken.
func TestQuoteIdentifierDoublesAnEmbeddedQuote(t *testing.T) {
	if got := QuoteIdentifier(`we"ird`); got != `"we""ird"` {
		t.Errorf(`QuoteIdentifier(we"ird) = %s`, got)
	}
}
