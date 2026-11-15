// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/devicechain-io/dc-microservice/messaging"
)

func TestDeliveryCapScanFixtures(t *testing.T) {
	for _, tc := range []struct {
		name, src, kind string
		want            int
	}{
		{"forward", "if msg.NumDelivered >= messaging.MaxDeliver {}", "raw", 1},
		{"reversed", "if messaging.MaxDeliver <= m.NumDelivered {}", "raw", 1},
		{"streams", "if msg.NumDelivered != streams.ConsumerMaxDeliver {}", "raw", 1},
		{"bare", "if MaxDeliver == msg.NumDelivered {}", "raw", 1},
		{"redelivery", "if msg.NumDelivered > 1 {}", "raw", 0},
		{"arithmetic", "_ = messaging.MaxDeliver*AckWait", "raw", 0},
		{"final caller", "messaging.FinalDelivery(msg)", "FinalDelivery", 1},
		{"dispose caller", "messaging.Dispose(msg, err)", "Dispose", 1},
		{"parenthesized caller", "(messaging.Dispose)(msg, err)", "Dispose", 1},
		{"alias", "js.Dispose(msg, err)", "Dispose", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			src := `package fixture
import "github.com/devicechain-io/dc-microservice/messaging"
import js "github.com/devicechain-io/dc-microservice/messaging"
func handle() { ` + tc.src + ` }
`
			if err := os.WriteFile(filepath.Join(dir, "fixture.go"), []byte(src), 0600); err != nil {
				t.Fatal(err)
			}
			found, _, err := deliveryCapChecksUnder(dir)
			if err != nil {
				t.Fatal(err)
			}
			if len(found) != tc.want {
				t.Fatalf("got %+v; want %d findings", found, tc.want)
			}
			if tc.want > 0 {
				if found[0].Kind != tc.kind {
					t.Fatalf("got %s; want %s", found[0].Kind, tc.kind)
				}
				// Planted offence must fail the empty ledger, then pass only when ledgered.
				if len(deliveryCapLedgerErrors(found, tc.kind, map[deliveryCapSite]string{})) != 1 {
					t.Fatal("missing entry accepted")
				}
				ledger := map[deliveryCapSite]string{found[0].Site: "fixture"}
				if errs := deliveryCapLedgerErrors(found, tc.kind, ledger); len(errs) != 0 {
					t.Fatal(errs)
				}
				ledger[found[0].Site] = ""
				if len(deliveryCapLedgerErrors(found, tc.kind, ledger)) != 1 {
					t.Fatal("entry without a reason accepted")
				}
				ledger[found[0].Site] = "fixture"
				if len(deliveryCapLedgerErrors(nil, tc.kind, ledger)) != 1 {
					t.Fatal("stale entry accepted")
				}
			}
		})
	}
}

func TestDeliveryCapScanExclusions(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"fixture_test.go", "testdata/fixture.go", "backend/core/messaging/fixture.go", "ordinary.go"} {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		src := "package fixture; func handle() { if msg.NumDelivered >= MaxDeliver {} }"
		if name == "ordinary.go" {
			src = "package fixture"
		}
		if err := os.WriteFile(path, []byte(src), 0600); err != nil {
			t.Fatal(err)
		}
	}
	found, _, err := deliveryCapChecksUnder(dir)
	if err != nil || len(found) != 0 {
		t.Fatalf("found %+v, err %v", found, err)
	}
}

func TestDeliveryCapScanRejectsEmptyTree(t *testing.T) {
	if _, _, err := deliveryCapChecksUnder(t.TempDir()); err == nil {
		t.Fatal("empty scan accepted")
	}
}

func TestFinalDelivery(t *testing.T) {
	cases := []struct {
		delivered int
		want      bool
	}{
		{messaging.MaxDeliver - 1, false}, {messaging.MaxDeliver, true}, {messaging.MaxDeliver + 1, true},
	}
	mismatches := func(final func(messaging.Message) bool) []int {
		var wrong []int
		for _, tc := range cases {
			if final(messaging.Message{NumDelivered: tc.delivered}) != tc.want {
				wrong = append(wrong, tc.delivered)
			}
		}
		return wrong
	}
	if wrong := mismatches(messaging.FinalDelivery); len(wrong) != 0 {
		t.Errorf("messaging.FinalDelivery disagrees with the delivery boundaries at %v", wrong)
	}
	// Negative controls: the SAME oracle must reject a planted broken predicate at
	// each boundary. A check that accepts one cannot protect that delivery's fate.
	for _, broken := range []struct {
		name  string
		final func(messaging.Message) bool
		wrong int
	}{
		{"premature", func(msg messaging.Message) bool { return msg.NumDelivered >= messaging.MaxDeliver-1 }, messaging.MaxDeliver - 1},
		{"strict", func(msg messaging.Message) bool { return msg.NumDelivered > messaging.MaxDeliver }, messaging.MaxDeliver},
		{"equality", func(msg messaging.Message) bool { return msg.NumDelivered == messaging.MaxDeliver }, messaging.MaxDeliver + 1},
	} {
		if wrong := mismatches(broken.final); len(wrong) != 1 || wrong[0] != broken.wrong {
			t.Errorf("%s negative control: mismatches %v; want [%d]", broken.name, wrong, broken.wrong)
		}
	}
}
