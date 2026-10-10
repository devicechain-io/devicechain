// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/rdb"
)

func TestCreateCommandRefusesOversizedInput(t *testing.T) {
	api := newTestApi(t)
	ctx := core.WithTenant(context.Background(), "A")
	big := `{"k":"` + strings.Repeat("x", rdb.MaxJSONInputBytes) + `"}`

	cases := []struct {
		name string
		req  CommandCreateRequest
		want RejectionCode
	}{
		{"payload", CommandCreateRequest{Token: "c1", DeviceToken: "d1", Name: "reboot", Payload: &big}, RejectPayloadTooLarge},
		{"metadata", CommandCreateRequest{Token: "c2", DeviceToken: "d1", Name: "reboot", Metadata: &big}, RejectMetadataTooLarge},
		{"name", CommandCreateRequest{Token: "c3", DeviceToken: "d1", Name: strings.Repeat("n", MaxCommandNameLength+1)}, RejectNameTooLong},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := api.CreateCommand(ctx, &tc.req)
			var rej *EnqueueRejected
			if !errors.As(err, &rej) || rej.Code != tc.want {
				t.Fatalf("want rejection %s, got %v", tc.want, err)
			}
		})
	}
	// Control: the same request within the bounds is accepted.
	small := `{"k":"v"}`
	if _, err := api.CreateCommand(ctx, &CommandCreateRequest{Token: "ok", DeviceToken: "d1",
		Name: strings.Repeat("n", MaxCommandNameLength), Payload: &small}); err != nil {
		t.Fatalf("a request within the bounds must be accepted: %v", err)
	}
}

func TestBatchRequestRefusesOversizedInput(t *testing.T) {
	big := `{"k":"` + strings.Repeat("x", rdb.MaxJSONInputBytes) + `"}`
	tokens := []string{"d1"}
	req := &CommandBatchCreateRequest{Token: "b1", Name: "reboot", DeviceTokens: &tokens, Payload: &big}
	var rej *EnqueueRejected
	if err := validateBatchRequest(req); !errors.As(err, &rej) || rej.Code != RejectPayloadTooLarge {
		t.Fatalf("want PAYLOAD_TOO_LARGE, got %v", err)
	}
}

func jsonOfExactSize(n int) string {
	const overhead = len(`{"k":""}`)
	return `{"k":"` + strings.Repeat("x", n-overhead) + `"}`
}

// The bound is inclusive: a payload or metadata of exactly rdb.MaxJSONInputBytes and a name
// of exactly MaxCommandNameLength are accepted, one more is refused.
func TestCommandInputBoundsAreInclusive(t *testing.T) {
	atBound := jsonOfExactSize(rdb.MaxJSONInputBytes)
	over := jsonOfExactSize(rdb.MaxJSONInputBytes + 1)
	name := strings.Repeat("n", MaxCommandNameLength)

	if err := validateCommandInputBounds(name, &atBound, &atBound); err != nil {
		t.Fatalf("values exactly at the bounds must be accepted: %v", err)
	}
	var rej *EnqueueRejected
	if err := validateCommandInputBounds(name, &over, nil); !errors.As(err, &rej) || rej.Code != RejectPayloadTooLarge {
		t.Fatalf("payload one byte over: %v", err)
	}
	if err := validateCommandInputBounds(name, nil, &over); !errors.As(err, &rej) || rej.Code != RejectMetadataTooLarge {
		t.Fatalf("metadata one byte over: %v", err)
	}
	if err := validateCommandInputBounds(name+"n", nil, nil); !errors.As(err, &rej) || rej.Code != RejectNameTooLong {
		t.Fatalf("name one byte over: %v", err)
	}

	// And end to end: the at-bound payload is stored by CreateCommand.
	api := newTestApi(t)
	ctx := core.WithTenant(context.Background(), "A")
	if _, err := api.CreateCommand(ctx, &CommandCreateRequest{Token: "edge", DeviceToken: "d1",
		Name: name, Payload: &atBound, Metadata: &atBound}); err != nil {
		t.Fatalf("an at-bound command must be created: %v", err)
	}
}

// Every RejectionCode constant this package declares is allowed as a metric label. The list
// is derived from the package source, so a code added later without an allowlist entry
// fails here instead of folding into "other".
func TestEveryDeclaredRejectionCodeIsMetricSafe(t *testing.T) {
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", func(fi os.FileInfo) bool {
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, 0)
	if err != nil {
		t.Fatal(err)
	}
	var declared []RejectionCode
	for _, pkg := range pkgs {
		for _, f := range pkg.Files {
			for _, d := range f.Decls {
				gd, ok := d.(*ast.GenDecl)
				if !ok || gd.Tok != token.CONST {
					continue
				}
				for _, spec := range gd.Specs {
					vs := spec.(*ast.ValueSpec)
					id, ok := vs.Type.(*ast.Ident)
					if !ok || id.Name != "RejectionCode" {
						continue
					}
					for _, v := range vs.Values {
						if lit, ok := v.(*ast.BasicLit); ok {
							s, _ := strconv.Unquote(lit.Value)
							declared = append(declared, RejectionCode(s))
						}
					}
				}
			}
		}
	}
	if len(declared) < 10 {
		t.Fatalf("found only %d declared codes; the source scan is not seeing them", len(declared))
	}
	for _, code := range declared {
		if _, ok := metricSafeCodes[code]; !ok {
			t.Errorf("rejection code %s is declared but not in metricSafeCodes", code)
		}
		if got := metricCodeLabel(code); got != string(code) {
			t.Errorf("metricCodeLabel(%s) = %q, want it unchanged", code, got)
		}
	}
}
