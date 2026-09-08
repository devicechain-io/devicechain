// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func ptrStr(s string) *string { return &s }

// 🔴 AN UNPARSEABLE TIME BOUND IS AN ERROR, NOT AN IGNORED FILTER. A caller narrowing to an
// incident window and getting the whole table back because their timestamp was malformed
// would read the extra rows as findings. A mutation that ignored the parse error survived
// every test until this one existed.
func TestAMalformedTimeBoundIsRefusedRatherThanDropped(t *testing.T) {
	for name, in := range map[string]deadLetterCriteriaInput{
		"since": {PageNumber: 1, PageSize: 10, Since: ptrStr("yesterday")},
		"until": {PageNumber: 1, PageSize: 10, Until: ptrStr("2026-13-45")},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := toDeadLetterCriteria(in)
			require.Error(t, err, "a malformed %s was silently dropped, widening the query", name)
			assert.Contains(t, err.Error(), name, "the error does not say which bound was wrong")
		})
	}
}

// The counterweight: a well-formed bound is carried through, in UTC.
func TestAWellFormedTimeBoundReachesTheCriteria(t *testing.T) {
	criteria, err := toDeadLetterCriteria(deadLetterCriteriaInput{
		PageNumber: 2, PageSize: 25,
		Since:  ptrStr("2026-09-04T06:00:00-04:00"),
		Until:  ptrStr("2026-09-04T18:00:00Z"),
		Tenant: ptrStr("acme"), Kind: ptrStr("notification"), Source: ptrStr("x"),
	})
	require.NoError(t, err)
	require.NotNil(t, criteria.Since)
	assert.Equal(t, time.Date(2026, 9, 4, 10, 0, 0, 0, time.UTC), *criteria.Since,
		"the bound was not normalised to UTC, so a client's offset would shift the window")
	require.NotNil(t, criteria.Until)
	assert.Equal(t, "acme", criteria.Tenant)
	assert.Equal(t, "notification", criteria.Kind)
	assert.Equal(t, "x", criteria.Source)
	assert.Equal(t, int32(2), criteria.PageNumber)
	assert.Equal(t, int32(25), criteria.PageSize)
}

// An absent bound stays absent. An empty string parsed as a time would be the zero value,
// which as a lower bound matches everything and as an upper bound matches nothing.
func TestAnAbsentTimeBoundStaysAbsent(t *testing.T) {
	criteria, err := toDeadLetterCriteria(deadLetterCriteriaInput{
		PageNumber: 1, PageSize: 10, Since: ptrStr(""), Until: nil})
	require.NoError(t, err)
	assert.Nil(t, criteria.Since)
	assert.Nil(t, criteria.Until)
}
