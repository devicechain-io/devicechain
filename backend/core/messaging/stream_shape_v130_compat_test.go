// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging

import (
	"encoding/json"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/config"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/streams"
)

// The frozen snapshot records every stream declared by v1.3.0 for instance "inst".
// Pin the release an upgrade starts from, allowing new streams but preserving the
// shapes old pods verify when they restart during the roll.
func TestDefaultStreamShapesMatchV130(t *testing.T) {
	raw, err := os.ReadFile("testdata/streams_v1.3.0.json")
	if err != nil {
		t.Fatal(err)
	}
	var snapshot map[string]struct {
		Subjects               []string `json:"subjects"`
		Retention              string   `json:"retention"`
		DuplicateWindowSeconds int64    `json:"duplicateWindowSeconds"`
		AllowDirect            bool     `json:"allowDirect"`
	}
	if err := json.Unmarshal(raw, &snapshot); err != nil {
		t.Fatal(err)
	}
	if len(snapshot) == 0 {
		t.Fatal("v1.3.0 stream snapshot is empty")
	}
	cfg := config.NewDefaultInstanceConfiguration()
	cfg.ApplyDefaults()
	nmgr := &NatsManager{Microservice: &core.Microservice{InstanceId: "inst", InstanceConfiguration: *cfg}}
	const why = "v1.3.0 services verify these exactly at start (streamDrift) and refuse to start on a difference, so under default settings a declared stream must keep v1.3.0's shape"
	for suffix, want := range snapshot {
		t.Run(suffix, func(t *testing.T) {
			if !streams.IsDeclared(suffix) {
				t.Fatalf("%s is no longer declared: %s", suffix, why)
			}
			got := nmgr.expectedStream(suffix)
			if !slices.Equal(got.subjects, want.Subjects) {
				if suffix == streams.MaxDeliveries {
					t.Errorf("%s subjects = %v, want %v: v1.3.0 services require the exact advisory list; tolerance for extra advisory subjects only starts with this release", suffix, got.subjects, want.Subjects)
				} else {
					t.Errorf("%s subjects = %v, want %v: %s", suffix, got.subjects, want.Subjects, why)
				}
			}
			if got.retention.String() != want.Retention {
				t.Errorf("%s retention = %s, want %s: %s", suffix, got.retention.String(), want.Retention, why)
			}
			if got.duplicates != time.Duration(want.DuplicateWindowSeconds)*time.Second {
				t.Errorf("%s duplicate window seconds = %g, want %d: %s", suffix, got.duplicates.Seconds(), want.DuplicateWindowSeconds, why)
			}
			if got.allowDirect != want.AllowDirect {
				t.Errorf("%s allowDirect = %t, want %t: %s", suffix, got.allowDirect, want.AllowDirect, why)
			}
		})
	}
}
