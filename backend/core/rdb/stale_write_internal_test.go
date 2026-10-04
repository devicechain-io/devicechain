// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package rdb

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"gorm.io/gorm"
)

func TestNextVersion(t *testing.T) {
	T := time.Date(2026, 10, 3, 10, 0, 0, 1000, time.UTC)
	cases := []struct {
		name          string
		readAt, clock time.Time
		want          time.Time
	}{
		{"clock has not moved", T, T, T.Add(time.Microsecond)},
		{"clock moved less than the precision the database keeps", T, T.Add(400 * time.Nanosecond), T.Add(time.Microsecond)},
		{"clock is behind", T, T.Add(-time.Hour), T.Add(time.Microsecond)},
		{"read version carries sub-microsecond digits", T.Add(789 * time.Nanosecond), T.Add(789 * time.Nanosecond), T.Add(time.Microsecond)},
		{"clock is past the floor", T, T.Add(time.Microsecond + time.Nanosecond), T.Add(time.Microsecond + time.Nanosecond)},
		{"clock is exactly the floor", T, T.Add(time.Microsecond), T.Add(time.Microsecond)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			db := &gorm.DB{Config: &gorm.Config{NowFunc: func() time.Time { return c.clock }}}
			got := nextVersion(db, c.readAt)
			assert.True(t, got.Equal(c.want), "got %v, want %v", got, c.want)
		})
	}
}
