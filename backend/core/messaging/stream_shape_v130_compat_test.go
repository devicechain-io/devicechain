// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging

import (
	"encoding/json"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/config"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/streams"
)

// The frozen snapshot records every stream declared by v1.5.0 for instance "inst".
// Pin the release an upgrade starts from, allowing new streams but preserving the
// shapes old pods verify when they restart during the roll.
func TestDefaultStreamShapesMatchV130(t *testing.T) {
	raw, err := os.ReadFile("testdata/streams_v1.5.0.json")
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
		t.Fatal("v1.5.0 stream snapshot is empty")
	}
	cfg := config.NewDefaultInstanceConfiguration()
	cfg.ApplyDefaults()
	nmgr := &NatsManager{Microservice: &core.Microservice{InstanceId: "inst", InstanceConfiguration: *cfg}}
	const why = "v1.5.0 services verify these exactly at start (streamDrift) and refuse to start on a difference, so under default settings a declared stream must keep v1.5.0's shape"
	for suffix, want := range snapshot {
		t.Run(suffix, func(t *testing.T) {
			if !streams.IsDeclared(suffix) {
				t.Fatalf("%s is no longer declared: %s", suffix, why)
			}
			got := nmgr.expectedStream(suffix)
			if suffix == streams.MaxDeliveries {
				// The shared advisory stream gains one subject per capturing stream a release adds. A v1.5.0
				// pod tolerates extra advisory subjects of exactly one stream's shape (streamDrift), so a later
				// release may declare new streams; what must never move is every subject v1.5.0 already listed,
				// every EXTRA subject must have that accepted shape, and it must belong to a stream this snapshot
				// does not know (a v1.5.0 stream's advisory cannot change).
				for _, w := range want.Subjects {
					if !slices.Contains(got.subjects, w) {
						t.Errorf("%s lost advisory subject %q that v1.5.0 declared", suffix, w)
					}
				}
				for _, g := range got.subjects {
					if slices.Contains(want.Subjects, g) {
						continue
					}
					known := false
					for other := range snapshot {
						if other != suffix && strings.Contains(g, StreamName("inst", other)) {
							known = true
						}
					}
					if known {
						t.Errorf("%s gained advisory subject %q for a stream v1.5.0 already declared", suffix, g)
					}
					if !isStreamAdvisorySubject(g, got.advisoryPrefix) {
						t.Errorf("%s gained advisory subject %q, which v1.5.0 pods refuse: not one stream's max-delivery advisory for this instance", suffix, g)
					}
				}
			} else if !slices.Equal(got.subjects, want.Subjects) {
				t.Errorf("%s subjects = %v, want %v: %s", suffix, got.subjects, want.Subjects, why)
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
