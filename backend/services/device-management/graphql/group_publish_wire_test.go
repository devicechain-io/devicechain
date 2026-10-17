// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"strings"
	"testing"
)

// publishEntityGroup's expectedActiveVersion reaches the publish as sent, through a
// variable the way every client sends it: a publish stating a version the group has moved
// past is refused as a stale write (no CONFLICT code, which would read as "already done"),
// and adds no version; the version the group does hold publishes. The fixture's group was
// published once, so it holds version 1.
func TestPublishEntityGroupStatesTheVersionItSaw(t *testing.T) {
	f := newGroupScopeFixture(t)
	const publish = `mutation($t: String!, $v: Int) { publishEntityGroup(token: $t, expectedActiveVersion: $v) { version } }`
	const versions = `{ entityGroupsByToken(tokens: ["north"]) { activeVersion versions { version } } }`

	stale := serve(t, f.operator(), publish, map[string]any{"t": "north", "v": 0})
	if len(stale.errs) == 0 {
		t.Fatalf("a publish stating version 0 of a group at version 1 was served: %v", stale.data)
	}
	if msg := stale.errs[0].Message; !strings.Contains(msg, "modified by another writer") {
		t.Fatalf("the stale publish was refused as %q, not as a stale write", msg)
	}
	if code := stale.errs[0].Extensions["code"]; code == "CONFLICT" {
		t.Fatalf("the stale publish carried CONFLICT, which a client reads as done")
	}
	after := serve(t, f.operator(), versions, nil)
	requireServed(t, after, "versions after the refused publish")
	g := after.data["entityGroupsByToken"].([]any)[0].(map[string]any)
	if n := len(g["versions"].([]any)); n != 1 || g["activeVersion"] != float64(1) {
		t.Fatalf("the refused publish moved the group: %v", g)
	}

	ok := serve(t, f.operator(), publish, map[string]any{"t": "north", "v": 1})
	requireServed(t, ok, "a publish stating the version the group holds")
	if v := ok.data["publishEntityGroup"].(map[string]any)["version"]; v != float64(2) {
		t.Fatalf("the publish stating version 1 made version %v", v)
	}
	// Omitted, the precondition is not asked: an unconditional publish still lands.
	free := serve(t, f.operator(), publish, map[string]any{"t": "north"})
	requireServed(t, free, "an unconditional publish")

	// A scoped member is refused the publish whatever version it states.
	scoped := serve(t, f.member("group:north"), publish, map[string]any{"t": "north", "v": 3})
	if len(scoped.errs) == 0 || scoped.errs[0].Extensions["code"] != "FORBIDDEN" {
		t.Fatalf("a scoped member published: %v %v", scoped.data, scoped.errs)
	}
}
