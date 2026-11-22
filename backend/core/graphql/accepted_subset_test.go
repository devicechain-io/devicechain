// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"fmt"
	"testing"

	graphql "github.com/graph-gophers/graphql-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Any prevents scalar validation from hiding a parser disagreement. These are the
// numeric refusals added by graphql-go v1.11.1, exercised through both guard callers.
func TestNumericLiteralRefusalsAgreeWithGraphQLGo(t *testing.T) {
	inner := lexInner()
	for _, literal := range []string{
		"-x", "-true", `-"text"`, "-}", "-]", "-{", "-[", "--1",
		"0x10", "0b11", "0o17", "1_0", "1.0_0", "0x1p2", "01", "1.",
		".5", "-.5", "-.5e2", "9223372036854775808", "-9223372036854775809",
		"1e999", "-1e999", "123abc", "1.0y", "12.3e5cfg", "1_name",
	} {
		t.Run(literal, func(t *testing.T) {
			doc := fmt.Sprintf(`mutation { a: bump(l: [%s]) }`, literal)
			kind, qerr := operationType(doc, "", DefaultGraphQLMaxQueryLength)
			require.NotNil(t, qerr, "the classifier accepted %s as %s", doc, kind)
			assert.Empty(t, kind)
			assert.Contains(t, qerr.Message, "syntax error")
			qerr = checkWork(doc, DefaultGraphQLMaxQueryLength, 5, 5)
			require.NotNil(t, qerr, "the work limit accepted %s", doc)
			assert.Nil(t, qerr.Extensions, "refuse syntax before counting fields")
			raw, calls := execCounting(func(ctx context.Context) *graphql.Response {
				return inner.Exec(ctx, doc, "", nil)
			})
			require.NotEmpty(t, raw.Errors)
			assert.Contains(t, raw.Errors[0].Message, "syntax error")
			assert.Nil(t, raw.Data)
			assert.Zero(t, calls)
		})
	}
}

// A minus still accepts whitespace, commas and # comments before a number. The
// int64 lower bound must be checked WITH its sign, as in upstream consumeNumber.
func TestValidNumericLiteralsStillRun(t *testing.T) {
	schema := lexSchema(1)
	for _, literal := range []string{
		"0", "-0", "0.123", "1e3", "-0.5", "-1", "-1e3", "1e+3",
		"9223372036854775807", "-9223372036854775808", "1.7976931348623157e308",
		"- 1", "-,1", "- # comment\n 1",
	} {
		t.Run(literal, func(t *testing.T) {
			doc := fmt.Sprintf(`mutation { a: bump(a: %s) }`, literal)
			kind, qerr := operationType(doc, "", DefaultGraphQLMaxQueryLength)
			require.Nil(t, qerr)
			assert.Equal(t, opMutation, kind)
			resp, calls := execCounting(func(ctx context.Context) *graphql.Response {
				return schema.Exec(ctx, doc, "", nil)
			})
			require.Empty(t, resp.Errors)
			assert.Equal(t, int32(1), calls)
		})
	}
}

// v1.11.1 accepts these escapes (including multiple backslashes and a quote run
// immediately before the real delimiter). Our established subset still refuses
// them, including in descriptions and unselected operations, before classification.
func TestEscapedBlockQuotesRemainRefused(t *testing.T) {
	inner := lexInner()
	for _, value := range []string{
		`"""x \""" y"""`,
		`"""x \\""" y"""`,
		`"""x \""""""`,
	} {
		for _, doc := range []string{
			`mutation { a: m(s: ` + value + `) }`,
			value + ` mutation { a: bump }`,
			`query Q { ping } mutation M { a: m(s: ` + value + `) }`,
		} {
			t.Run(doc, func(t *testing.T) {
				opName := ""
				if doc[0] == 'q' {
					opName = "Q"
				}
				kind, qerr := operationType(doc, opName, DefaultGraphQLMaxQueryLength)
				require.NotNil(t, qerr)
				assert.Empty(t, kind)
				assert.Contains(t, qerr.Message, `closing """ follows a backslash`)
				qerr = checkWork(doc, DefaultGraphQLMaxQueryLength, 5, 5)
				require.NotNil(t, qerr)
				assert.Contains(t, qerr.Message, `closing """ follows a backslash`)
				assert.Nil(t, qerr.Extensions)
				// Select Q when present, proving an escape in an unselected operation
				// still refuses the entire document in our guard.
				raw, calls := execCounting(func(ctx context.Context) *graphql.Response {
					return inner.Exec(ctx, doc, opName, nil)
				})
				require.Empty(t, raw.Errors)
				assert.Equal(t, int32(1), calls, "upstream accepts a document our subset refuses")
			})
		}
	}
}
