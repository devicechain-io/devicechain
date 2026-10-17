// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package sharecheck

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

type fakeQuerier struct {
	answer string
	err    error
	tenant string
	query  string
	vars   map[string]any
}

func (f *fakeQuerier) Query(_ context.Context, _, tenant, query string, vars map[string]any, out any) error {
	f.tenant, f.query, f.vars = tenant, query, vars
	if f.err != nil {
		return f.err
	}
	return json.Unmarshal([]byte(f.answer), out)
}

// Only the customers device-management did not return are missing, asked in the caller's tenant.
func TestMissingReportsTheCustomersNotReturned(t *testing.T) {
	q := &fakeQuerier{answer: `{"customersByToken":[{"token":"acme"}]}`}
	missing, err := New(q, "u").Missing(context.Background(), "t1", "customer", []string{"globex", "acme", "initech"})
	require.NoError(t, err)
	require.Equal(t, []string{"globex", "initech"}, missing)
	require.Equal(t, "t1", q.tenant)
	require.Equal(t, customersByToken, q.query)
	require.Equal(t, []string{"globex", "acme", "initech"}, q.vars["tokens"])
}

// A group is shareable only while it is an access scope: one returned with the flag off is
// missing exactly as one not returned is, and the lookup is the group read, in the caller's tenant.
func TestMissingReportsGroupsThatAreNotAccessScopes(t *testing.T) {
	q := &fakeQuerier{answer: `{"entityGroupsByToken":[{"token":"north","accessScope":true},{"token":"south","accessScope":false}]}`}
	missing, err := New(q, "u").Missing(context.Background(), "t1", "group", []string{"south", "north", "ghost"})
	require.NoError(t, err)
	require.Equal(t, []string{"south", "ghost"}, missing)
	require.Equal(t, "t1", q.tenant)
	require.Equal(t, groupsByToken, q.query)
	require.Equal(t, []string{"south", "north", "ghost"}, q.vars["tokens"])
}

// A failed lookup, an answer without the list, a group without its flag and a kind nobody can
// look up are errors — never "none missing".
func TestAnUnansweredLookupIsAnError(t *testing.T) {
	for name, tc := range map[string]struct {
		q    *fakeQuerier
		kind string
	}{
		"customer refused":   {&fakeQuerier{err: errors.New("connection refused")}, "customer"},
		"customer no list":   {&fakeQuerier{answer: `{}`}, "customer"},
		"group refused":      {&fakeQuerier{err: errors.New("connection refused")}, "group"},
		"group no list":      {&fakeQuerier{answer: `{}`}, "group"},
		"group without flag": {&fakeQuerier{answer: `{"entityGroupsByToken":[{"token":"north"}]}`}, "group"},
		"unknown kind":       {&fakeQuerier{answer: `{}`}, "asset"},
	} {
		_, err := New(tc.q, "u").Missing(context.Background(), "t1", tc.kind, []string{"north"})
		require.Error(t, err, name)
	}
}
