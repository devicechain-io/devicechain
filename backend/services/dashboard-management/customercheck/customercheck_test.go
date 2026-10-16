// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package customercheck

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
	vars   map[string]any
}

func (f *fakeQuerier) Query(_ context.Context, _, tenant, _ string, vars map[string]any, out any) error {
	f.tenant, f.vars = tenant, vars
	if f.err != nil {
		return f.err
	}
	return json.Unmarshal([]byte(f.answer), out)
}

// Only the tokens device-management did not return are missing, asked in the caller's tenant.
func TestMissingReportsTheTokensNotReturned(t *testing.T) {
	q := &fakeQuerier{answer: `{"customersByToken":[{"token":"acme"}]}`}
	missing, err := New(q, "u").Missing(context.Background(), "t1", []string{"globex", "acme", "initech"})
	require.NoError(t, err)
	require.Equal(t, []string{"globex", "initech"}, missing)
	require.Equal(t, "t1", q.tenant)
	require.Equal(t, []string{"globex", "acme", "initech"}, q.vars["tokens"])
}

// A failed lookup, and an answer without the list, are errors — never "none missing".
func TestAnUnansweredLookupIsAnError(t *testing.T) {
	_, err := New(&fakeQuerier{err: errors.New("connection refused")}, "u").Missing(context.Background(), "t1", []string{"acme"})
	require.Error(t, err)
	_, err = New(&fakeQuerier{answer: `{}`}, "u").Missing(context.Background(), "t1", []string{"acme"})
	require.Error(t, err)
}
