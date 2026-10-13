// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package rdb

import (
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"gorm.io/gorm"
)

// A sentinel made by NewNotFound is a miss in the store's sense, keeps its own wording, and
// is still told apart from every other sentinel (including another NewNotFound with the same
// text) by errors.Is, wrapped or bare.
func TestNewNotFoundIsAMissAndStaysDistinct(t *testing.T) {
	role := NewNotFound("role not found")
	tier := NewNotFound("role not found")

	assert.Equal(t, "role not found", role.Error())
	assert.ErrorIs(t, role, gorm.ErrRecordNotFound)
	assert.ErrorIs(t, fmt.Errorf("update: %w", role), gorm.ErrRecordNotFound)
	assert.ErrorIs(t, fmt.Errorf("update: %w", role), role)
	assert.False(t, errors.Is(tier, role), "two sentinels with one wording are still two sentinels")
	assert.False(t, errors.Is(role, errors.New("role not found")))
	assert.False(t, errors.Is(gorm.ErrRecordNotFound, role), "the store's miss is not this sentinel")
}
