// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// planScan is one scan node of an EXPLAIN (FORMAT JSON) plan.
type planScan struct {
	node      string // "Index Scan", "Index Only Scan", "Seq Scan", ...
	relation  string // the relation scanned (a chunk, for a hypertable)
	index     string // the index used, as named on the chunk; "" for a non-index scan
	indexCond string
}

// planIndexScans walks EXPLAIN (FORMAT JSON) output through every "Plans" level and
// returns each node that scans a relation. Compare its index with chunkIndexIs.
func planIndexScans(explainJSON []byte) ([]planScan, error) {
	var top []struct {
		Plan map[string]any `json:"Plan"`
	}
	if err := json.Unmarshal(explainJSON, &top); err != nil {
		return nil, fmt.Errorf("parse EXPLAIN JSON: %w", err)
	}
	if len(top) == 0 || top[0].Plan == nil {
		return nil, fmt.Errorf("EXPLAIN JSON carries no plan")
	}
	var scans []planScan
	var walk func(n map[string]any)
	walk = func(n map[string]any) {
		str := func(k string) string { v, _ := n[k].(string); return v }
		// Only SCAN nodes. A ModifyTable names the relation it writes, and a Custom Scan
		// is Timescale's own node (ChunkAppend over the hypertable, DecompressChunk over
		// a compressed chunk): each names a relation but reads it through the scans
		// beneath it, which are what this reports.
		nodeType := str("Node Type")
		isScan := strings.HasSuffix(nodeType, "Scan") && nodeType != "Custom Scan"
		if rel := str("Relation Name"); rel != "" && isScan {
			scans = append(scans, planScan{
				node:      str("Node Type"),
				relation:  rel,
				index:     str("Index Name"),
				indexCond: str("Index Cond"),
			})
		}
		if kids, ok := n["Plans"].([]any); ok {
			for _, k := range kids {
				if m, ok := k.(map[string]any); ok {
					walk(m)
				}
			}
		}
	}
	walk(top[0].Plan)
	return scans, nil
}

// chunkPrefix matches the two prefixes Timescale gives a chunk's copy of a hypertable
// index on the pinned image: `_hyper_<ht>_<chunk>_chunk_<name>` for a plain index, and
// `<n>_<name>` for the index behind a primary-key or unique CONSTRAINT.
var chunkPrefix = regexp.MustCompile(`^(_hyper_\d+_\d+_chunk_|(\d+_)+)`)

// chunkIndexSuffix strips the chunk prefix from an index name.
func chunkIndexSuffix(name string) string {
	return chunkPrefix.ReplaceAllString(name, "")
}

// maxIdentifierLen is Postgres's NAMEDATALEN-1: a longer identifier is truncated.
const maxIdentifierLen = 63

// chunkIndexIs reports whether chunkIndex (a chunk's copy of an index, or the index
// itself) is the copy of the hypertable index named want. The chunk prefix makes many
// copies longer than an identifier may be, and Postgres truncates them to 63 bytes —
// `_hyper_3_11_chunk_measurement_events_tenant_id_occurred_time_idx` is stored as
// `..._occurred_time_id` — so a truncated name matches the full name it is a prefix of.
func chunkIndexIs(chunkIndex, want string) bool {
	got := chunkIndexSuffix(chunkIndex)
	if got == want {
		return true
	}
	return got != "" && len(chunkIndex) == maxIdentifierLen && strings.HasPrefix(want, got)
}

func TestPlanIndexScans(t *testing.T) {
	// A ChunkAppend over two index scans, a seq scan, and a nested subplan — the shapes a
	// hypertable read produces.
	fixture := []byte(`[{"Plan": {
		"Node Type": "ModifyTable", "Relation Name": "events",
		"Plans": [{
			"Node Type": "Custom Scan", "Custom Plan Provider": "ChunkAppend", "Relation Name": "events",
			"Plans": [
				{"Node Type": "Index Scan", "Relation Name": "_hyper_1_1_chunk",
				 "Index Name": "_hyper_1_1_chunk_events_device_token_occurred_time_idx",
				 "Index Cond": "((device_token)::text = 'd'::text)"},
				{"Node Type": "Index Only Scan", "Relation Name": "_hyper_1_2_chunk",
				 "Index Name": "_hyper_1_2_chunk_events_device_token_occurred_time_idx"},
				{"Node Type": "Seq Scan", "Relation Name": "_hyper_1_3_chunk"},
				{"Node Type": "Hash", "Plans": [
					{"Node Type": "Index Scan", "Relation Name": "_hyper_4_9_chunk",
					 "Index Name": "12_uq_event_anchors_idem"}
				]}
			]
		}]
	}}]`)
	scans, err := planIndexScans(fixture)
	require.NoError(t, err)
	require.Len(t, scans, 4)
	assert.Equal(t, planScan{"Index Scan", "_hyper_1_1_chunk", "_hyper_1_1_chunk_events_device_token_occurred_time_idx",
		"((device_token)::text = 'd'::text)"}, scans[0])
	assert.Equal(t, planScan{"Index Only Scan", "_hyper_1_2_chunk",
		"_hyper_1_2_chunk_events_device_token_occurred_time_idx", ""}, scans[1])
	assert.Equal(t, planScan{"Seq Scan", "_hyper_1_3_chunk", "", ""}, scans[2])
	assert.Equal(t, planScan{"Index Scan", "_hyper_4_9_chunk", "12_uq_event_anchors_idem", ""}, scans[3])
	assert.True(t, chunkIndexIs(scans[0].index, "events_device_token_occurred_time_idx"))
	assert.True(t, chunkIndexIs(scans[3].index, "uq_event_anchors_idem"))
	assert.False(t, chunkIndexIs(scans[2].index, "events_device_token_occurred_time_idx"), "a seq scan uses no index")

	_, err = planIndexScans([]byte(`[]`))
	assert.Error(t, err, "an empty EXPLAIN must not read as a plan with no scans")
}

func TestChunkIndexIs(t *testing.T) {
	// Truncated to 63 bytes on the pinned image (observed, not assumed).
	truncated := "_hyper_3_11_chunk_measurement_events_tenant_id_occurred_time_id"
	assert.Len(t, truncated, maxIdentifierLen)
	assert.True(t, chunkIndexIs(truncated, "measurement_events_tenant_id_occurred_time_idx"))
	// A truncated name matches only a name it is a prefix of.
	assert.False(t, chunkIndexIs(truncated, "measurement_events_occurred_time_idx"))
	// A SHORT name is never a truncation: a prefix match there is a different index.
	assert.False(t, chunkIndexIs("_hyper_1_1_chunk_events_occurred_time", "events_occurred_time_idx"))
	assert.True(t, chunkIndexIs("_hyper_1_1_chunk_events_occurred_time_idx", "events_occurred_time_idx"))
	assert.False(t, chunkIndexIs("", "events_occurred_time_idx"))
}

func TestChunkIndexSuffix(t *testing.T) {
	for in, want := range map[string]string{
		// The two shapes the pinned image names chunk indexes with.
		"_hyper_1_12_chunk_events_tenant_id_occurred_time_idx": "events_tenant_id_occurred_time_idx",
		"1_events_pkey":   "events_pkey",
		"1_2_events_pkey": "events_pkey",
		// A hypertable's own index is unchanged, hyphen and all.
		"idx_event-management_alert_events_tenant_id": "idx_event-management_alert_events_tenant_id",
		"uq_measurement_events_idem":                  "uq_measurement_events_idem",
	} {
		assert.Equal(t, want, chunkIndexSuffix(in), in)
	}
}
