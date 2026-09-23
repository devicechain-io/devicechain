// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
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
	// graphql-go closes a block string at the first three quotes, backslash or not.
	{"backslash before three quotes closes a block string", `subscription { ticks(x: """a\""" ) }`, "", opSubscription},
	{"a block string hides a brace", `subscription { ticks(x: """ { """) } mutation { doIt }`, "", ""},
	// graphql-go's scanner runs in Go-token mode: `//` and `/* */` are comments to it.
	{"Go line comment", "// mutation { doIt }\nsubscription { ticks }", "", opSubscription},
	{"Go block comment", "/* mutation { doIt } */ subscription { ticks }", "", opSubscription},
	{"a Go block comment hides a brace", "subscription { ticks /* } mutation { doIt } */ }", "", opSubscription},
	// graphql-go scans a backquoted raw string as one token (so the brace inside is
	// not a brace to it) and then refuses it as a value: nothing executes.
	{"raw string", "subscription { ticks(x: `}`) }", "", opSubscription},
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
			got, err := operationType(tc.doc, tc.opName)
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
	input In { a: InA }
	input InA { b: String }
	type Query { hello(x: String): String! }
	type Mutation { doIt(x: String): String! }
	type Subscription { ticks(x: String, o: In, l: [Int]): Int! }
`

// arbiterResolver records whether graphql-go ran a query or mutation resolver.
type arbiterResolver struct{ ran atomic.Int64 }

func (r *arbiterResolver) Hello(struct{ X *string }) string {
	r.ran.Add(1)
	return "hi"
}

func (r *arbiterResolver) DoIt(struct{ X *string }) string {
	r.ran.Add(1)
	return "done"
}

func (r *arbiterResolver) Ticks(ctx context.Context, _ struct {
	X *string
	O *struct{ A *struct{ B *string } }
	L *[]*int32
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
	f.Fuzz(func(t *testing.T, doc, opName string) {
		kind, err := operationType(doc, opName)
		if err != nil || kind != opSubscription {
			return
		}
		res := &arbiterResolver{}
		schema := graphql.MustParseSchema(arbiterSchema, res)
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
// what makes the table's claims about graphql-go (Go-style comments, block-string
// termination) facts rather than beliefs, and it proves the fuzz arbiter above can
// see a query or mutation run at all.
func TestOperationTypeAgreesWithGraphQLGo(t *testing.T) {
	// Cases the classifier accepts that graphql-go refuses before executing anything.
	refusedByGraphQLGo := map[string]bool{"raw string": true}

	for _, tc := range operationTypeCases {
		if tc.want == "" {
			continue
		}
		t.Run(tc.name, func(t *testing.T) {
			res := &arbiterResolver{}
			schema := graphql.MustParseSchema(arbiterSchema, res)
			if refusedByGraphQLGo[tc.name] {
				responses, err := schema.Subscribe(context.Background(), tc.doc, tc.opName, nil)
				if err != nil {
					t.Fatalf("Subscribe: %v", err)
				}
				refused := false
				for r := range responses {
					refused = refused || len(r.(*graphql.Response).Errors) > 0
				}
				if !refused || res.ran.Load() != 0 {
					t.Fatalf("graphql-go was expected to refuse %q outright (refused=%v, resolvers run=%d)",
						tc.doc, refused, res.ran.Load())
				}
				return
			}
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
