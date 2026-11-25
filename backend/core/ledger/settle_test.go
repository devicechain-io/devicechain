// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package ledger

import (
	"strings"
	"testing"
)

func settlementRevision(v int64) *int64 { return &v }

func TestDecideVerdictTable(t *testing.T) {
	value := Raw(`22.0`)
	equal, different := Raw(`22`), Raw(`23`)
	for _, tc := range []struct {
		status string
		doc    *Raw
		unset  bool
		want   Settlement
		code   string
	}{
		{"APPLIED", &equal, false, SettlementConfirmed, ""},
		{"REJECTED", &equal, false, SettlementFailed, "DEVICE_REJECTED"},
		{"APPLIED", &different, false, SettlementSuperseded, ""},
		{"REJECTED", &different, false, SettlementSuperseded, ""},
		{"APPLIED", nil, true, SettlementConfirmed, ""},
		{"REJECTED", nil, true, SettlementFailed, "DEVICE_REJECTED"},
		{"APPLIED", &equal, true, SettlementSuperseded, ""},
		{"APPLIED", nil, false, SettlementSuperseded, ""},
	} {
		p := PostedImage{Revision: settlementRevision(5), Value: &value}
		if tc.unset {
			p.Value = nil
		}
		got := Decide(p, &Report{Revision: 5, Status: tc.status, FromLog: true, Source: "REPORT", Detail: "first error"}, tc.doc, nil)
		if got.Settlement != tc.want || got.Code != tc.code || !got.Decided {
			t.Fatalf("%+v: %+v", tc, got)
		}
		if got.Basis != "REPORT_LOG" {
			t.Fatalf("basis: %+v", got)
		}
	}
}

func TestDecidePrecedenceAndBasis(t *testing.T) {
	v := Raw(`true`)
	p := PostedImage{Revision: settlementRevision(5), Value: &v}
	r := &Report{Revision: 6, Status: "APPLIED", FromLog: true, Source: "REPORT"}
	if Decide(PostedImage{Value: &v}, r, &v, nil).Decided {
		t.Fatal("NULL posted revision acquired a decider")
	}
	for _, current := range []Settlement{SettlementConfirmed, SettlementFailed, SettlementSuperseded, "FUTURE"} {
		if Decide(p, r, &v, &current).Decided {
			t.Fatalf("reconsidered %s", current)
		}
	}
	for _, current := range []Settlement{SettlementAmbiguous, SettlementResolvedConfirmed, SettlementResolvedFailed} {
		if !Decide(p, r, &v, &current).Decided {
			t.Fatalf("excluded %s", current)
		}
	}
	for _, source := range []string{"REPORT", "STATE_BACKFILL"} {
		for _, log := range []bool{true, false} {
			for _, gap := range []int64{4, 5, 6} {
				r.Source, r.FromLog, r.GapThrough = source, log, &gap
				want := "LATEST_STATE"
				if source == "REPORT" && log && gap < 5 {
					want = "REPORT_LOG"
				}
				if got := Decide(p, r, &v, nil); got.Basis != want {
					t.Fatalf("%+v: %+v", r, got)
				}
			}
		}
	}
	for _, report := range []*Report{nil, {Revision: 4, Status: "APPLIED"}, {Revision: 5, Status: "RECEIVED"}} {
		if Decide(p, report, &v, nil).Decided {
			t.Fatalf("invalid decider %+v", report)
		}
	}
}

func TestDecideResolutionAndBoundedDetail(t *testing.T) {
	v, different := Raw(`true`), Raw(`false`)
	p := PostedImage{Revision: settlementRevision(5), Value: &v}
	for _, current := range []Settlement{SettlementResolvedConfirmed, SettlementResolvedFailed} {
		for _, status := range []string{"APPLIED", "REJECTED"} {
			for _, doc := range []*Raw{&v, &different} {
				r := &Report{Revision: 5, Status: status, Detail: strings.Repeat("界", 600)}
				got := Decide(p, r, doc, &current)
				want := SettlementSuperseded
				if doc == &v {
					want = SettlementConfirmed
					if status == "REJECTED" {
						want = SettlementFailed
					}
				}
				agrees := (current == SettlementResolvedConfirmed && want == SettlementConfirmed) || (current == SettlementResolvedFailed && want == SettlementFailed)
				if !got.Decided || got.Settlement != want || got.Contradicts == agrees {
					t.Fatalf("%s %s %s: %+v", current, status, *doc, got)
				}
				if want == SettlementFailed && got.Detail != strings.Repeat("界", 512) {
					t.Fatalf("unbounded/incorrect rejection detail: %d runes", len([]rune(got.Detail)))
				}
				if want != SettlementFailed && got.Detail != "" {
					t.Fatalf("unexpected detail on %s", want)
				}
			}
		}
	}
}
