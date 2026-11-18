// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// invalidationBookkeepingSize counts retained invalidation slots independently of
// whether they are per-key map entries or a single scalar epoch.
func (c *disabledCache) invalidationBookkeepingSize() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	v := reflect.ValueOf(c).Elem()
	size := 0
	for i := 0; i < v.NumField(); i++ {
		switch v.Type().Field(i).Name {
		case "mu", "m":
			continue
		}
		if v.Field(i).Kind() == reflect.Map {
			size += v.Field(i).Len()
		} else {
			size++
		}
	}
	return size
}

func TestDisabledCacheInvalidationBookkeepingIsBounded(t *testing.T) {
	var c disabledCache
	for i := range 2 * maxDisabledCache {
		c.forget(fmt.Sprintf("tenant-%d/device-%d", i, i))
	}
	require.LessOrEqual(t, c.invalidationBookkeepingSize(), 1,
		"distinct invalidations must retain at most one bookkeeping slot")
	require.Empty(t, c.m, "forget must not retain facts for unseen devices")
}

func TestDisabledCacheForgetFencesOtherInFlightKeys(t *testing.T) {
	var c disabledCache
	now := time.Now()
	_, _, _, before := c.get("acme/dev-1", now)
	c.forget("other/dev-2")
	c.put("acme/dev-1", DeviceFacts{Incarnation: 17}, true, now, before)
	_, _, hit, after := c.get("acme/dev-1", now)
	require.False(t, hit, "any forget must fence an earlier lookup")
	c.put("acme/dev-1", DeviceFacts{Incarnation: 23}, true, now, after)
	facts, found, hit, _ := c.get("acme/dev-1", now)
	require.True(t, hit, "a lookup begun after forget must be cached")
	require.True(t, found)
	require.Equal(t, uint64(23), facts.Incarnation)
}
