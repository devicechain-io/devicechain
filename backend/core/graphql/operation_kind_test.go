// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	graphql "github.com/graph-gophers/graphql-go"
)

// operationTypeCases are documents whose classification is known. want "" means the
// document must be refused (an error). They double as the fuzz seed corpus, so every
// one of them is also checked against graphql-go itself.
var operationTypeCases = []struct {
	name, doc, opName, want string
}{
	{"subscription", `subscription { ticks }`, "", opSubscription},
	{"named subscription", `subscription S { ticks }`, "", opSubscription},
	{"query", `query { hello }`, "", opQuery},
	{"shorthand query", `{ hello }`, "", opQuery},
	{"mutation", `mutation { doIt }`, "", opMutation},
	{"variables with an object default", `subscription S($o: In = {a: {b: "}"}}, $l: [Int] = [1]) { ticks(o: $o, l: $l) }`, "", opSubscription},
	{"directive arguments", `subscription S @d(x: "{") { ticks }`, "", opSubscription},
	{"keyword inside a string", `subscription { ticks(x: "mutation { doIt }") }`, "", opSubscription},
	{"keyword inside a # comment", "# mutation { doIt }\nsubscription { ticks }", "", opSubscription},
	{"a # comment hides the subscription", "# subscription { ticks }\nmutation { doIt }", "", opMutation},
	{"comment ending in CR", "# x\rsubscription { ticks }", "", opSubscription},
	{"commas are insignificant", `,,subscription,S,{,ticks,},`, "", opSubscription},
	{"description", `"desc" subscription { ticks }`, "", opSubscription},
	{"block string description", `"""a } { mutation""" subscription { ticks }`, "", opSubscription},
	{"block string argument", `subscription { ticks(x: """ } """) }`, "", opSubscription},
	{"a backslash before a shorter quote run in a block string", `subscription { ticks(x: """a\"b""") }`, "", opSubscription},
	{"a block string hides a brace", `subscription { ticks(x: """ { """) } mutation { doIt }`, "", ""},
	// Refused lexemes. graphql-go's scanner runs in Go-token mode, so `//` and `/* */`
	// are comments to it and backquoted raw strings and `'c'` are single tokens; and it
	// closes a block string at the first three quotes, backslash or not. Its
	// normalising pass reads none of these the same way, so the reader refuses them all.
	{"backslash before three quotes closes a block string", `subscription { ticks(x: """a\""" ) }`, "", ""},
	{"Go line comment", "// mutation { doIt }\nsubscription { ticks }", "", ""},
	{"Go block comment", "/* mutation { doIt } */ subscription { ticks }", "", ""},
	{"a Go block comment hides a brace", "subscription { ticks /* } mutation { doIt } */ }", "", ""},
	{"raw string", "subscription { ticks(x: `}`) }", "", ""},
	{"character literal", "subscription { ticks(x: 'x') }", "", ""},
	// Documents the old top-level walk accepted and the full reader refuses, as
	// graphql-go does.
	{"empty selection set", `subscription { }`, "", ""},
	{"junk between the name and the selection set", `subscription S ??? { ticks }`, "", ""},
	{"an argument with no value", `subscription { ticks(x: ) }`, "", ""},
	// Strings graphql-go's normalising pass rewrites, and a minus before a name: both
	// classified, and both run by graphql-go (see TestOperationTypeAgreesWithGraphQLGo).
	{"surrogate pair escape", `subscription { ticks(x: "\uD83D\uDE00") }`, "", opSubscription},
	{"minus before a name, for an Any argument", `subscription { ticks(a: -x) }`, "", opSubscription},
	{"fragment before the operation", `fragment F on Subscription { ticks } subscription { ...F }`, "", opSubscription},
	{"fragment only", `fragment F on Query { hello }`, "", ""},
	{"select the subscription by name", `mutation M { doIt } subscription S { ticks }`, "S", opSubscription},
	{"select the mutation by name", `subscription S { ticks } mutation M { doIt }`, "M", opMutation},
	{"a keyword can be a name", `query subscription { hello }`, "subscription", opQuery},
	{"two operations and no name", `subscription S { ticks } mutation M { doIt }`, "", ""},
	{"no operation by that name", `subscription S { ticks }`, "T", ""},
	{"duplicate name", `subscription S { ticks } subscription S { ticks }`, "S", ""},
	{"an anonymous operation has no name", `subscription { ticks }`, "S", ""},
	{"empty", ``, "", ""},
	{"only a comment", "# nothing", "", ""},
	{"unknown keyword", `subscribe { ticks }`, "", ""},
	{"description on the shorthand", `"d" { hello }`, "", ""},
	{"unterminated selection", `subscription { ticks`, "", ""},
	{"unbalanced", `subscription { ticks) }`, "", ""},
	{"unterminated string", `subscription { ticks(x: "abc) }`, "", ""},
	{"unterminated block string", `subscription { ticks(x: """abc) }`, "", ""},
	{"unterminated Go comment", `subscription { ticks } /* mutation { doIt }`, "", ""},
	{"stray token at the top level", `subscription { ticks } }`, "", ""},
	// The GraphQL-only escape graphql-go normalises before scanning: refused here.
	{"braced unicode escape", `subscription { ticks(x: "\u{1F600}") }`, "", ""},
	{"invalid UTF-8", "subscription { ticks(x: \"\xff\") }", "", ""},
}

func TestOperationType(t *testing.T) {
	for _, tc := range operationTypeCases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := operationType(tc.doc, tc.opName, DefaultGraphQLMaxQueryLength)
			if tc.want == "" {
				if err == nil {
					t.Fatalf("operationType(%q, %q) = %q, want an error", tc.doc, tc.opName, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("operationType(%q, %q) failed: %v", tc.doc, tc.opName, err)
			}
			if got != tc.want {
				t.Fatalf("operationType(%q, %q) = %q, want %q", tc.doc, tc.opName, got, tc.want)
			}
		})
	}
}

// arbiterSchema is what FuzzOperationType runs documents against. Its fields take
// the arguments and directives the seed documents use, so graphql-go gets past
// validation and actually reaches the operation it selected.
const arbiterSchema = `
	schema { query: Query mutation: Mutation subscription: Subscription }
	directive @d(x: String) on SUBSCRIPTION | QUERY | MUTATION | FIELD
	scalar Any
	input In { a: InA }
	input InA { b: String }
	type Query { hello(x: String, a: Any): String! }
	type Mutation { doIt(x: String, a: Any): String! }
	type Subscription { ticks(x: String, o: In, l: [Int], a: Any): Int! }
`

// newArbiter is graphql-go over arbiterSchema, without the work limit. The Any scalar
// (see lexSDL) lets it execute a minus in front of a name.
func newArbiter(res *arbiterResolver) *graphql.Schema {
	return graphql.MustParseSchema(arbiterSchema, res, graphql.Logger(silentLogger{}))
}

// arbiterResolver records whether graphql-go ran a query or mutation resolver.
type arbiterResolver struct{ ran atomic.Int64 }

func (r *arbiterResolver) Hello(struct {
	X *string
	A *lexAny
}) string {
	r.ran.Add(1)
	return "hi"
}

func (r *arbiterResolver) DoIt(struct {
	X *string
	A *lexAny
}) string {
	r.ran.Add(1)
	return "done"
}

func (r *arbiterResolver) Ticks(ctx context.Context, _ struct {
	X *string
	O *struct{ A *struct{ B *string } }
	L *[]*int32
	A *lexAny
}) <-chan int32 {
	ch := make(chan int32, 1)
	ch <- 1
	close(ch)
	return ch
}

// FuzzOperationType holds the classifier to graphql-go itself: whenever it says a
// document's selected operation is a subscription, graphql-go's Subscribe must not
// run a query or a mutation resolver for it. That is the one direction a
// disagreement would be a hole rather than a false refusal. Under plain `go test` the
// seed corpus — every TestOperationType case — runs as the check; `go test -fuzz`
// explores from there.
func FuzzOperationType(f *testing.F) {
	for _, tc := range operationTypeCases {
		f.Add(tc.doc, tc.opName)
	}
	for _, doc := range lexemeFuzzSeeds {
		f.Add(strings.Replace(doc, "mutation", "subscription", 1), "")
	}
	f.Fuzz(func(t *testing.T, doc, opName string) {
		kind, qerr := operationType(doc, opName, DefaultGraphQLMaxQueryLength)
		if qerr != nil || kind != opSubscription {
			return
		}
		res := &arbiterResolver{}
		schema := newArbiter(res)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		responses, err := schema.Subscribe(ctx, doc, opName, nil)
		if err != nil {
			t.Fatalf("Subscribe: %v", err)
		}
		for range responses {
		}
		if n := res.ran.Load(); n != 0 {
			t.Fatalf("operationType(%q, %q) said subscription, but graphql-go ran %d query/mutation resolver(s)",
				doc, opName, n)
		}
	})
}

// Every classification the table asserts is also graphql-go's own: for a document
// classified as a query or mutation, graphql-go runs exactly one such resolver; for
// one classified as a subscription it runs none and streams without errors. This is
// what makes the table's claims about graphql-go facts rather than beliefs, and it
// proves the fuzz arbiter above can see a query or mutation run at all.
func TestOperationTypeAgreesWithGraphQLGo(t *testing.T) {
	for _, tc := range operationTypeCases {
		if tc.want == "" {
			continue
		}
		t.Run(tc.name, func(t *testing.T) {
			res := &arbiterResolver{}
			schema := newArbiter(res)
			responses, err := schema.Subscribe(context.Background(), tc.doc, tc.opName, nil)
			if err != nil {
				t.Fatalf("Subscribe: %v", err)
			}
			var errs []string
			for r := range responses {
				for _, e := range r.(*graphql.Response).Errors {
					errs = append(errs, e.Message)
				}
			}
			if len(errs) != 0 {
				t.Fatalf("graphql-go refused %q: %v", tc.doc, errs)
			}
			wantRan := int64(1)
			if tc.want == opSubscription {
				wantRan = 0
			}
			if got := res.ran.Load(); got != wantRan {
				t.Fatalf("graphql-go ran %d query/mutation resolvers for %q, want %d (classified %s)",
					got, tc.doc, wantRan, tc.want)
			}
		})
	}
}

// The gate refuses every refused lexeme too: it reads with the same reader, so the
// subscription form of each document the work limit refuses is refused here, with
// the same reason.
func TestOperationTypeRefusesTheLexemes(t *testing.T) {
	for _, tc := range refusedLexemes {
		doc := strings.Replace(tc.doc, "mutation", "subscription", 1)
		kind, qerr := operationType(doc, "", DefaultGraphQLMaxQueryLength)
		if qerr == nil {
			t.Errorf("%s: operationType(%q) = %q, want a refusal", tc.name, doc, kind)
			continue
		}
		if !strings.Contains(qerr.Message, tc.reason) {
			t.Errorf("%s: refused with %q, want it to contain %q", tc.name, qerr.Message, tc.reason)
		}
	}
}

// 🔴 THE GATE CHECKS THE LENGTH BEFORE IT READS. A valid subscription one byte over the
// ceiling is refused with the length message: had it been read first, it would have
// classified as a subscription.
func TestOperationTypeChecksLengthFirst(t *testing.T) {
	const maxLen = 64
	doc := "subscription { ticks } #"
	doc += strings.Repeat("x", maxLen+1-len(doc))

	kind, qerr := operationType(doc[:maxLen], "", maxLen)
	if qerr != nil || kind != opSubscription {
		t.Fatalf("the control: %q at the ceiling = (%q, %v), want a subscription", doc[:maxLen], kind, qerr)
	}
	kind, qerr = operationType(doc, "", maxLen)
	if qerr == nil {
		t.Fatalf("operationType(%d bytes, ceiling %d) = %q, want the length refusal", len(doc), maxLen, kind)
	}
	if want := "query length 65 exceeds the maximum allowed query length of 64 bytes"; qerr.Message != want {
		t.Fatalf("refused with %q, want %q", qerr.Message, want)
	}
}

// The reader recurses into brackets and selection sets, so the worst document the
// ceiling lets through is one nested all the way down. At the REAL default ceiling that
// document is read — and refused, since it never closes — in time linear in its
// length; the bound here is loose enough for a race-instrumented run on a busy runner,
// and far below what a quadratic reader would need.
func TestOperationTypeReadsTheDeepestDocumentQuickly(t *testing.T) {
	for _, open := range []string{"[", "{a:", "{ ticks "} {
		prefix := "subscription { ticks(x: "
		if open == "{ ticks " {
			prefix = "subscription { ticks "
		}
		doc := prefix + strings.Repeat(open, (DefaultGraphQLMaxQueryLength-len(prefix))/len(open))
		start := time.Now()
		kind, qerr := operationType(doc, "", DefaultGraphQLMaxQueryLength)
		elapsed := time.Since(start)
		if qerr == nil {
			t.Fatalf("an unterminated %d-byte document of %q classified as %q", len(doc), open, kind)
		}
		if elapsed > 2*time.Second {
			t.Fatalf("reading %d bytes of %q took %s", len(doc), open, elapsed)
		}
		t.Logf("%d bytes of %q: refused in %s", len(doc), open, elapsed)
	}
}
