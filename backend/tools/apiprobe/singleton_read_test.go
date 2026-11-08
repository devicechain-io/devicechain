// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"encoding/json"
	"testing"
)

// A Singleton read is argument-less and answers one nullable object: the object is the row,
// and a null is the row GONE, never a shape change and never a pass.
func TestASingletonReadReturnsTheObjectAndReportsANullAsMissing(t *testing.T) {
	e := entity{Name: "semantic-context", Read: "semanticContext", Singleton: true, Fields: "enabledPacks"}
	if got := e.readDoc(); got != "query{semanticContext{enabledPacks}}" {
		t.Fatalf("readDoc %q", got)
	}
	obj, err := readObject(e, map[string]json.RawMessage{"semanticContext": json.RawMessage(`{"enabledPacks":["BRICK"]}`)}, "k", nil)
	if err != nil || string(obj) != `{"enabledPacks":["BRICK"]}` {
		t.Fatalf("object read: %s, %v", obj, err)
	}
	_, err = readObject(e, map[string]json.RawMessage{"semanticContext": json.RawMessage(`null`)}, "k", nil)
	if codeOf(err) != exitMissing {
		t.Fatalf("a null singleton is %v (code %d), want exitMissing", err, codeOf(err))
	}
}
