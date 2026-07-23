// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package decode

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestObservationsFiltersManagementObjects is the load-bearing selection guard: the OMA
// management objects (0 Security, 1 Server, 3 Device, 4 Connectivity) must NEVER be observed,
// only telemetry objects. Widening the allowlist to permit /0/0 reddens this.
func TestObservationsFiltersManagementObjects(t *testing.T) {
	body := []byte(`</>;rt="oma.lwm2m";ct=110,</0/0>,</1/0>,</3/0>,</4/0>,</3303/0>,</3300/0>`)
	paths, overflow := Observations(body, DefaultObjectAllowlist)
	assert.Equal(t, 0, overflow)
	assert.Equal(t, []string{"/3303/0", "/3300/0"}, paths) // registration order, management objects gone
	for _, p := range paths {
		assert.False(t, strings.HasPrefix(p, "/0/") || strings.HasPrefix(p, "/1/") || strings.HasPrefix(p, "/3/"))
	}
}

// TestObservationsDedupesAndReducesToInstance: a repeated instance is observed once, and a
// resource-level link is reduced to its object instance (we observe at instance level).
func TestObservationsDedupesAndReducesToInstance(t *testing.T) {
	body := []byte(`</3303/0>,</3303/0>,</3303/0/5700>,</3300/1/5500>`)
	paths, overflow := Observations(body, DefaultObjectAllowlist)
	assert.Equal(t, 0, overflow)
	assert.Equal(t, []string{"/3303/0", "/3300/1"}, paths)
}

// TestObservationsSkipsRootAndObjectOnly: the root link and an object-only link (no instance)
// are not observable instances.
func TestObservationsSkipsRootAndObjectOnly(t *testing.T) {
	body := []byte(`</>,</3303>,</3300/0>`)
	paths, _ := Observations(body, DefaultObjectAllowlist)
	assert.Equal(t, []string{"/3300/0"}, paths)
}

// TestObservationsCapAndOverflow: the device-controlled link list is bounded; instances beyond
// the cap are dropped and counted. Removing the cap reddens this.
func TestObservationsCapAndOverflow(t *testing.T) {
	var b strings.Builder
	total := MaxObservationsPerRegistration + 8
	for i := 0; i < total; i++ {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, "</3300/%d>", i)
	}
	paths, overflow := Observations([]byte(b.String()), DefaultObjectAllowlist)
	assert.Len(t, paths, MaxObservationsPerRegistration)
	assert.Equal(t, 8, overflow)
}

// TestObservationsQuotedCommaDoesNotSplitLinks pins the scan-the-brackets approach: a comma
// inside a quoted attribute value must not be mistaken for a link separator.
func TestObservationsQuotedCommaDoesNotSplitLinks(t *testing.T) {
	body := []byte(`</3303/0>;rt="urn:a,b";ct=110,</3300/0>`)
	paths, _ := Observations(body, DefaultObjectAllowlist)
	assert.Equal(t, []string{"/3303/0", "/3300/0"}, paths)
}

// TestPermitsBoundaries checks the inclusive-range allowlist edges.
func TestPermitsBoundaries(t *testing.T) {
	assert.True(t, DefaultObjectAllowlist.Permits(3200))
	assert.True(t, DefaultObjectAllowlist.Permits(3441))
	assert.False(t, DefaultObjectAllowlist.Permits(3199))
	assert.False(t, DefaultObjectAllowlist.Permits(3442))
	assert.False(t, DefaultObjectAllowlist.Permits(3)) // Device object
}

// TestObservationsEmptyBody is a no-op, not a crash.
func TestObservationsEmptyBody(t *testing.T) {
	paths, overflow := Observations(nil, DefaultObjectAllowlist)
	require.Empty(t, paths)
	assert.Equal(t, 0, overflow)
}

// TestObservationsRejectsOutOfRangeInstance: a negative or too-large instance id is a garbage
// link, not an observable instance — we must not issue a doomed Observe against it.
func TestObservationsRejectsOutOfRangeInstance(t *testing.T) {
	body := []byte(`</3303/-1>,</3303/70000>,</3303/0>`)
	paths, _ := Observations(body, DefaultObjectAllowlist)
	assert.Equal(t, []string{"/3303/0"}, paths)
}

// TestObservationsQuotedAngleBracketDoesNotSwallowNextLink: a `<` inside a quoted attribute
// value must not consume the following legitimate link (the mirror of the quoted-comma case).
func TestObservationsQuotedAngleBracketDoesNotSwallowNextLink(t *testing.T) {
	body := []byte(`</3303/0>;title="a<b",</3300/0>`)
	paths, _ := Observations(body, DefaultObjectAllowlist)
	assert.Equal(t, []string{"/3303/0", "/3300/0"}, paths)
}

// TestObservationsDuplicatePastCapDoesNotInflateOverflow: dedupe happens before the cap, so a
// repeated instance neither consumes cap budget nor counts as overflow.
func TestObservationsDuplicatePastCapDoesNotInflateOverflow(t *testing.T) {
	var b strings.Builder
	for i := 0; i < MaxObservationsPerRegistration; i++ {
		fmt.Fprintf(&b, "</3300/%d>,", i)
	}
	// Then re-list the first instance many times — all duplicates, none should overflow.
	for j := 0; j < 5; j++ {
		b.WriteString("</3300/0>,")
	}
	paths, overflow := Observations([]byte(strings.TrimSuffix(b.String(), ",")), DefaultObjectAllowlist)
	assert.Len(t, paths, MaxObservationsPerRegistration)
	assert.Equal(t, 0, overflow) // duplicates are not overflow
}
