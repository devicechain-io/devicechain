// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package sim

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"
)

// dcctlRecordSource is dcctl's mirror of the handshake wire contract. dcctl never
// imports this module (it is a separate, deliberately untrusting client), so the two
// declarations are kept in step by hand — which is exactly the shape that drifts.
const dcctlRecordSource = "../../../cli/sim/record.go"

// 🔑 THE HANDSHAKE MIRROR GATE. `dcctl sim create` WRITES the file dc-simulator
// READS, and the two ends declare that file's shape in two different Go modules. A
// tag that agrees only in a comment is a wire contract held together by someone
// remembering.
//
// The failure is silent in the worst direction: encoding/json drops a field it does
// not recognise, so a tag typo on either side unmarshals to the zero value. A
// misspelled `mqttTlsInsecure` yields insecure=false, and on a local bring-up that
// surfaces as an x509 error nobody attributes to a typo; on a deployment with a
// valid certificate it surfaces as nothing at all.
//
// The same file pair already carries TestDcctlKnowsEveryRegisteredScenario, which
// exists because the comment-only manifest-id mirror "did not survive the third
// scenario". This is that lesson applied to the tags, before the same thing happens.
//
// It reads dcctl's source through go/parser rather than a regex. That is not
// incidental: a regex over source cannot tell code from prose, so a gate scanning
// for a declaration is satisfied by the same text in a comment — a trap this
// codebase has already been bitten by. The parser knows the difference by
// construction, so this gate cannot be revived by documentation.
func TestHandshakeAndDcctlRecordDeclareTheSameWire(t *testing.T) {
	// Endpoints is ONE object on the wire, so the two declarations must agree
	// exactly — a field on either side alone is a value one end writes and the
	// other silently discards.
	mine := jsonTagsOf(t, "handshake.go", "Endpoints")
	theirs := jsonTagsOf(t, dcctlRecordSource, "Endpoints")
	if len(mine) == 0 || len(theirs) == 0 {
		t.Fatalf("parsed %d and %d Endpoints tags; with either empty this gate asserts nothing",
			len(mine), len(theirs))
	}
	if !slices.Equal(mine, theirs) {
		t.Errorf("Endpoints json tags disagree across the handshake mirror.\n"+
			"  dc-simulator reads: %v\n  dcctl writes:       %v\n"+
			"A field on one side only is written and never read, or read and never written.",
			mine, theirs)
	}

	// Handshake vs Record is a SUBSET relation, not equality: dcctl's Record carries
	// fields dc-simulator has no business reading (the sim's local name, the control
	// address, the admin URL destroy tears down against). Every field this side
	// reads must exist over there, or it is permanently zero.
	handshake := jsonTagsOf(t, "handshake.go", "Handshake")
	record := jsonTagsOf(t, dcctlRecordSource, "Record")
	if len(handshake) == 0 || len(record) == 0 {
		t.Fatalf("parsed %d and %d tags for Handshake/Record; with either empty this gate "+
			"asserts nothing", len(handshake), len(record))
	}
	for _, tag := range handshake {
		if !slices.Contains(record, tag) {
			t.Errorf("Handshake reads %q, which dcctl's Record never writes — it unmarshals "+
				"to the zero value on every sim record dcctl produces", tag)
		}
	}
}

// jsonTagsOf returns the sorted json tag names declared on a struct type in a Go
// source file. Fields tagged `json:"-"` and untagged fields are skipped: neither
// crosses the wire, so neither is part of the contract under test.
func jsonTagsOf(t *testing.T, path, typeName string) []string {
	t.Helper()
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("stat %s: %v (if the file moved, re-point this check rather than deleting "+
			"it — it is the only thing holding the handshake's two declarations together)", path, err)
	}
	file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}

	var tags []string
	found := false
	ast.Inspect(file, func(n ast.Node) bool {
		ts, ok := n.(*ast.TypeSpec)
		if !ok || ts.Name.Name != typeName {
			return true
		}
		st, ok := ts.Type.(*ast.StructType)
		if !ok {
			return true
		}
		found = true
		for _, f := range st.Fields.List {
			if f.Tag == nil {
				continue
			}
			// Tag.Value keeps its backquotes; trim them before reflect reads it.
			name, _, _ := strings.Cut(reflect.StructTag(strings.Trim(f.Tag.Value, "`")).Get("json"), ",")
			if name == "" || name == "-" {
				continue
			}
			tags = append(tags, name)
		}
		return false
	})
	if !found {
		t.Fatalf("no struct type %q in %s; this gate cannot see what it is comparing and "+
			"would pass without asserting anything", typeName, path)
	}
	slices.Sort(tags)
	return tags
}
