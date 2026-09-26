// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"testing"

	gql "github.com/graph-gophers/graphql-go"
)

// Alarm and AlarmEvent once served a `message` field that nothing ever wrote: every
// alarm read null through it. It is removed, and a client that still selects it must be
// REFUSED rather than handed a null that looks like a real, empty value. The counterweight
// documents select a field the types do serve, so a validator that rejected everything
// (or read a different schema) cannot pass this test.
func TestAlarmTypesServeNoMessageField(t *testing.T) {
	schema := gql.MustParseSchema(SchemaContent, &SchemaResolver{})

	for _, served := range []string{
		`query { alarmsByToken(tokens: ["a"]) { lastValue } }`,
		`subscription { alarmStream { lastValue } }`,
	} {
		if errs := schema.Validate(served); len(errs) != 0 {
			t.Errorf("counterweight %q: want 0 validation errors, got %v", served, errs)
		}
	}

	for _, c := range []struct {
		doc, want string
	}{
		{`query { alarmsByToken(tokens: ["a"]) { message } }`, `Cannot query field "message" on type "Alarm".`},
		{`subscription { alarmStream { message } }`, `Cannot query field "message" on type "AlarmEvent".`},
	} {
		errs := schema.Validate(c.doc)
		if len(errs) != 1 {
			t.Errorf("%q: want exactly 1 validation error, got %d: %v", c.doc, len(errs), errs)
			continue
		}
		if errs[0].Rule != "FieldsOnCorrectTypeRule" || errs[0].Message != c.want {
			t.Errorf("%q: want rule FieldsOnCorrectTypeRule / %q, got rule %q / %q",
				c.doc, c.want, errs[0].Rule, errs[0].Message)
		}
	}
}
