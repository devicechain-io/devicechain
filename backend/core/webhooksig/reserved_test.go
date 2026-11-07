// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package webhooksig

import (
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestReservedNamespace(t *testing.T) {
	cur, prev := KeyNames("hooks/prod")
	assert.False(t, InReservedNamespace(cur), "KeyNames gives the base's slots; the prefix is added by the caller that owns the base")
	assert.False(t, InReservedNamespace(prev))
	assert.True(t, InReservedNamespace(ReservedPrefix+"hooks/prod/current"))
	assert.True(t, InReservedNamespace(ReservedPrefix))
	assert.False(t, InReservedNamespace("hooks/prod"))
	assert.False(t, InReservedNamespace(""))
	assert.False(t, InReservedNamespace("webhook-signing"), "the bare word is not the namespace")
}

// Concurrent rotations in one process serialize: every call returns a key, the final current key is
// the last one installed, and the key it replaced is the previous slot, so the overlap holds.
func TestConcurrentRotationsInOneProcessKeepTheOverlap(t *testing.T) {
	ctx := context.Background()
	st := newMemStore()
	kr := Keyring{Store: st}
	_, err := kr.Create(ctx, "t1", "h")
	require.NoError(t, err)

	const n = 16
	issued := make([]string, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			s, err := kr.Rotate(ctx, "t1", "h")
			assert.NoError(t, err)
			issued[i] = s
		}(i)
	}
	wg.Wait()

	keys, err := ResolveKeys(ctx, "h", st.resolver("t1"))
	require.NoError(t, err)
	require.Len(t, keys, 2)
	assert.Contains(t, issued, keys[0], "the current key is one that was issued")
	assert.Contains(t, issued, keys[1], "so is the previous: with serialized rotations it is the one just replaced")
	assert.NotEqual(t, keys[0], keys[1])
}
