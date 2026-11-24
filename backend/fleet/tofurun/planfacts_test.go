// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package tofurun

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func rc(address, typ string, actions []string, before, after any) map[string]any {
	return map[string]any{
		"address": address, "mode": "managed", "type": typ, "provider_name": "registry.opentofu.org/hashicorp/google",
		"change": map[string]any{"actions": actions, "before": before, "after": after},
	}
}

func planDoc(t *testing.T, version string, changes ...map[string]any) []byte {
	t.Helper()
	b, err := json.Marshal(map[string]any{"format_version": version, "resource_changes": changes})
	require.NoError(t, err)
	return b
}

func TestExtractIsValueFree(t *testing.T) {
	doc := planDoc(t, "1.2", rc("google_x.a", "google_x", []string{"update"},
		map[string]any{"name": "n", "password": "hunter2-before", "nested": map[string]any{"k": "v1"}},
		map[string]any{"name": "n", "password": "hunter2-after", "nested": map[string]any{"k": "v2"}}))
	f := Extract(doc)
	require.Equal(t, 1, f.ChangeCount)
	require.Equal(t, []string{"nested.k", "password"}, f.Facts[0].ChangedPaths)
	require.NotContains(t, string(f.Canonical()), "hunter2")
}

func TestExtractIsTotal(t *testing.T) {
	for name, doc := range map[string][]byte{
		"unparsable":     []byte("not json"),
		"empty":          nil,
		"unknown major":  planDoc(t, "2.0"),
		"no version":     planDoc(t, ""),
		"unknown action": planDoc(t, "1.2", rc("a.b", "t", []string{"teleport"}, nil, nil)),
		"no actions":     planDoc(t, "1.2", rc("a.b", "t", nil, nil, nil)),
		"no address":     planDoc(t, "1.2", map[string]any{"change": map[string]any{"actions": []string{"create"}}}),
		"no change":      planDoc(t, "1.2", map[string]any{"address": "a.b"}),
	} {
		f := Extract(doc)
		require.Equal(t, len(f.Facts), f.ChangeCount, name)
		require.NotEmpty(t, f.Facts, name)
		var refused bool
		for _, x := range f.Facts {
			refused = refused || x.Refusal != ""
		}
		require.True(t, refused, name+": an unreadable plan yields a refusal fact, never nothing")
		require.False(t, Gate{AllowedTypes: map[string]bool{"t": true}}.Judge(KindCreate, f).Allowed, name)
	}
}

func TestExtractIsCanonical(t *testing.T) {
	a := planDoc(t, "1.2", rc("b.y", "t", []string{"create"}, nil, map[string]any{"k": 1}), rc("a.x", "t", []string{"create"}, nil, map[string]any{"k": 1}))
	b := planDoc(t, "1.2", rc("a.x", "t", []string{"create"}, nil, map[string]any{"k": 1}), rc("b.y", "t", []string{"create"}, nil, map[string]any{"k": 1}))
	require.Equal(t, Extract(a).Digest(), Extract(b).Digest(), "fact order does not move the digest")
	c := planDoc(t, "1.2", rc("a.x", "t", []string{"create"}, nil, map[string]any{"k": 1}), rc("b.y", "t", []string{"update"}, nil, map[string]any{"k": 1}))
	require.NotEqual(t, Extract(a).Digest(), Extract(c).Digest(), "an action change moves it")
}

func TestExtractCountsDriftAndReplacePaths(t *testing.T) {
	doc, err := json.Marshal(map[string]any{
		"format_version": "1.2",
		"resource_drift": []any{map[string]any{"address": "a.x"}},
		"resource_changes": []any{map[string]any{
			"address": "a.x", "type": "t", "change": map[string]any{
				"actions": []string{"delete", "create"}, "replace_paths": [][]any{{"spec", 0, "name"}},
			}}},
	})
	require.NoError(t, err)
	f := Extract(doc)
	require.Equal(t, 1, f.DriftCount)
	require.Equal(t, []string{"spec.0.name"}, f.Facts[0].ReplacePaths)
}

var allowT = Gate{AllowedTypes: map[string]bool{"t": true}}

func facts(t *testing.T, changes ...map[string]any) PlanFacts {
	return Extract(planDoc(t, "1.2", changes...))
}

func TestGateMatrix(t *testing.T) {
	create := rc("t.a", "t", []string{"create"}, nil, map[string]any{"k": 1})
	update := rc("t.a", "t", []string{"update"}, map[string]any{"k": 1}, map[string]any{"k": 2})
	del := rc("t.a", "t", []string{"delete"}, map[string]any{"k": 1}, nil)
	replace := rc("t.a", "t", []string{"delete", "create"}, map[string]any{"k": 1}, map[string]any{"k": 2})
	replaceRev := rc("t.a", "t", []string{"create", "delete"}, map[string]any{"k": 1}, map[string]any{"k": 2})
	noop := rc("t.a", "t", []string{"no-op"}, map[string]any{"k": 1}, map[string]any{"k": 1})
	read := rc("data.t.a", "t", []string{"read"}, nil, nil)
	foreign := rc("other.a", "other", []string{"update"}, nil, nil)
	foreignNoop := rc("other.a", "other", []string{"no-op"}, nil, nil)

	cases := []struct {
		name  string
		kind  Kind
		plan  PlanFacts
		allow bool
	}{
		{"create: create", KindCreate, facts(t, create), true},
		{"create: no changes", KindCreate, facts(t, noop, read), true},
		{"create: update", KindCreate, facts(t, update), true},
		{"create: delete", KindCreate, facts(t, del), false},
		{"create: replace", KindCreate, facts(t, replace), false},
		{"create: replace reversed", KindCreate, facts(t, replaceRev), false},
		{"change: update", KindChange, facts(t, update), true},
		{"change: delete", KindChange, facts(t, del), false},
		{"upgrade: replace", KindUpgrade, facts(t, replace), false},
		{"upgrade: update", KindUpgrade, facts(t, update), true},
		{"drift check: nothing", KindDriftCheck, facts(t, noop), true},
		{"drift check: update", KindDriftCheck, facts(t, update), false},
		{"recover: nothing", KindRecover, facts(t, noop), true},
		{"recover: update", KindRecover, facts(t, update), false},
		{"recover: delete", KindRecover, facts(t, del), false},
		{"destroy: delete", KindDestroy, facts(t, del), true},
		{"destroy: update", KindDestroy, facts(t, update), false},
		{"destroy: create", KindDestroy, facts(t, create), false},
		{"destroy: replace", KindDestroy, facts(t, replace), false},
		{"destroy: nothing but reads", KindDestroy, facts(t, read, noop), true},
		{"unlisted type changing is forbidden in create", KindCreate, facts(t, foreign), false},
		{"unlisted type is forbidden in destroy too", KindDestroy, facts(t, rc("other.a", "other", []string{"delete"}, nil, nil)), false},
		{"unlisted type that does nothing is not a change", KindCreate, facts(t, foreignNoop), true},
		{"unknown kind", Kind("DETACH"), facts(t, noop), false},
	}
	for _, c := range cases {
		v := allowT.Judge(c.kind, c.plan)
		require.Equal(t, c.allow, v.Allowed, c.name+": "+strings.Join(v.Reasons, "; "))
		if !c.allow {
			require.NotEmpty(t, v.Reasons, c.name)
			require.NotEqual(t, ClassSafe, v.Worst, c.name)
		}
	}
}

func TestGateFailsClosed(t *testing.T) {
	create := facts(t, rc("t.a", "t", []string{"create"}, nil, nil))
	require.False(t, Gate{}.Judge(KindCreate, create).Allowed, "the zero gate allows no change")
	require.False(t, Gate{AllowedTypes: map[string]bool{}}.Judge(KindCreate, create).Allowed)

	skewed := create
	skewed.ChangeCount = 0
	require.False(t, allowT.Judge(KindCreate, skewed).Allowed, "a count that is not the number of facts is refused")

	require.False(t, allowT.Judge(KindRecover, PlanFacts{FormatVer: "1.2", DriftCount: 2}).Allowed, "drift on a recovery plan is refused")
	require.True(t, allowT.Judge(KindDriftCheck, PlanFacts{FormatVer: "1.2", DriftCount: 2}).Allowed, "a drift check exists to look at drift")
}
