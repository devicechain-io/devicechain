// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"testing"

	gql "github.com/graph-gophers/graphql-go"

	"github.com/devicechain-io/dc-microservice/cmddownlink"
)

// TestEveryDownlinkDocumentValidatesAgainstThisSchema. The transport adapters build their
// claim, confirm, park and drain documents from core/cmddownlink, and core cannot import this
// service, so nothing but this test connects the two. A renamed field or argument here, or a
// typo there, would fail at the first command of every adapter rather than at build time.
//
// Validation, not execution: it needs no database, and it is exactly the check the server applies
// before running anything. The literal-string tests in core pin the document TEXT; this pins
// that the text is a valid operation on THIS schema.
func TestEveryDownlinkDocumentValidatesAgainstThisSchema(t *testing.T) {
	schema := gql.MustParseSchema(SchemaContent, &SchemaResolver{})
	for name, c := range map[string]struct {
		doc  string
		vars map[string]any
	}{
		"drain":   {cmddownlink.DrainQuery, map[string]any{"deviceToken": "d", "limit": 10}},
		"claim":   {cmddownlink.ClaimMutation, map[string]any{"token": "c"}},
		"confirm": {cmddownlink.ConfirmDispatchMutation, map[string]any{"token": "c", "dispatchNonce": "n"}},
		"park":    {cmddownlink.ParkMutation, map[string]any{"token": "c", "dispatchNonce": "n"}},
	} {
		if errs := schema.ValidateWithVariables(c.doc, c.vars); len(errs) != 0 {
			t.Errorf("%s document is not valid against command-delivery's schema: %v\n%s", name, errs, c.doc)
		}
	}

	// The control: validation must be able to fail, or the loop above proves nothing.
	if errs := schema.ValidateWithVariables(`mutation($token: String!) { markCommandSentX(token: $token) }`, map[string]any{"token": "c"}); len(errs) == 0 {
		t.Fatal("a document naming a field the schema does not have validated; the check above cannot fail")
	}
}
