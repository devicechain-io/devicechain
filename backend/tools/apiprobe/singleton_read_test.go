// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"encoding/json"
	"strings"
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

// semanticContext never answers null: a tenant with no stored context reads as an EMPTY one
// whose updatedAt is null. So for that row a null updatedAt is the row GONE, reported as
// missing rather than compared field by field as a changed row.
func TestASingletonWithANullPresenceFieldIsMissing(t *testing.T) {
	e := entity{Name: "semantic-context", Read: "semanticContext", Singleton: true,
		Fields: "enabledPacks updatedAt", AbsentIfNull: "updatedAt"}
	_, err := readObject(e, map[string]json.RawMessage{"semanticContext": json.RawMessage(`{"enabledPacks":[],"updatedAt":null}`)}, "k", nil)
	if codeOf(err) != exitMissing {
		t.Fatalf("an empty, never-set context is %v (code %d), want exitMissing", err, codeOf(err))
	}
	obj, err := readObject(e, map[string]json.RawMessage{"semanticContext": json.RawMessage(`{"enabledPacks":["BRICK"],"updatedAt":"2026-11-08T00:00:00Z"}`)}, "k", nil)
	if err != nil || obj == nil {
		t.Fatalf("a stored context: %s, %v", obj, err)
	}
}

// The table's semantic-context row selects the field its presence is judged by.
func TestTheSemanticContextRowJudgesPresenceByUpdatedAt(t *testing.T) {
	for _, e := range entities {
		if e.Name != "semantic-context" {
			continue
		}
		if e.AbsentIfNull != "updatedAt" || !strings.Contains(" "+e.Fields+" ", " updatedAt ") {
			t.Fatalf("semantic-context must select updatedAt and judge presence by it: %+v", e.Fields)
		}
		return
	}
	t.Fatal("no semantic-context row")
}

// updatedAt is allowed in a selection for one row only, for the reason
// timestampFieldsThatRoundTrip gives; any other row selecting it is comparing a timestamp
// nobody has shown round-trips.
func TestOnlyTheSemanticContextSelectsUpdatedAt(t *testing.T) {
	for _, e := range allEntities() {
		if e.Name != "semantic-context" && strings.Contains(" "+e.Fields+" ", " updatedAt ") {
			t.Errorf("%q selects updatedAt; only semantic-context may", e.Name)
		}
	}
}
