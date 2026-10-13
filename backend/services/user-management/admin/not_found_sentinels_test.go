// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package admin

import (
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"gorm.io/gorm"
)

// The not-found sentinels are misses (so the GraphQL boundary codes them NOT_FOUND) and
// nothing else about them moved: each keeps its wording, still matches itself wrapped, and is
// still told apart from its neighbours -- callers branch on exactly these.
func TestNotFoundSentinelsStayDistinctMisses(t *testing.T) {
	sentinels := map[string]error{
		"role not found":         ErrRoleNotFound,
		"tenant tier not found":  ErrTierNotFound,
		"identity not found":     ErrIdentityNotFound,
		"membership not found":   ErrMembershipNotFound,
		"tenant not found":       ErrTenantNotFound,
		"oauth client not found": ErrOAuthClientNotFound,
	}
	for msg, s := range sentinels {
		assert.Equal(t, msg, s.Error())
		assert.ErrorIs(t, s, gorm.ErrRecordNotFound, msg)
		assert.ErrorIs(t, fmt.Errorf("%w: %q", s, "x"), s, msg)
		for otherMsg, other := range sentinels {
			if otherMsg != msg {
				assert.False(t, errors.Is(s, other), "%s must not match %s", msg, otherMsg)
			}
		}
	}
}
