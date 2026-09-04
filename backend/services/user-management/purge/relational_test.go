// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package purge

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/devicechain-io/dc-microservice/tenantpurge"
)

// 🔴 A RETAINED ROW THAT STILL NAMES SOMEONE MUST STOP THE AREA ACKING. The redaction is
// the one step in a purge that would otherwise grade itself — its UPDATE reports a row
// count, and a row count is exactly what a WHERE clause selecting the wrong rows also
// produces — so the independent scan is only worth having if its answer is acted on.
func TestARetainedRowStillNamingSomeoneStopsTheAck(t *testing.T) {
	clean := tenantpurge.Result{Tenant: "acme"}
	require.NoError(t, retainedError("rdb", "acme", clean),
		"a clean scan was turned into a refusal, which would stall every purge")

	dirty := tenantpurge.Result{Tenant: "acme", Redacted: []tenantpurge.TableResult{
		{Table: tenantpurge.Table{Schema: "user-management", Name: "audit_events"},
			Class: tenantpurge.ClassRedacted, Rows: 3},
	}}
	err := retainedError("rdb", "acme", dirty)
	require.Error(t, err, "the store acked over a journal that still names the erased tenant's people")

	// 🔑 THE SENTENCE IS PART OF THE BEHAVIOUR. This failure and a residual-row failure
	// have different causes and different remedies; an operator told "something is still
	// writing" goes looking for a service to stop, which for this one there is not.
	assert.Contains(t, err.Error(), "still name someone")
	assert.Contains(t, err.Error(), "audit_events", "the refusal does not say where to look")
	assert.Contains(t, err.Error(), "3")
	assert.NotContains(t, err.Error(), "still writing")
}
