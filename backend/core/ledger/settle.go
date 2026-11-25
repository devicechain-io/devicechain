// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package ledger

import (
	"bytes"
	"encoding/json"
)

// Raw is a configuration value rendered by the configuration document builder.
// A nil *Raw means the key is absent, distinct from JSON null.
type Raw = json.RawMessage

type PostedImage struct {
	// Nil means no configuration revision existed at posting.
	Revision *int64
	Value    *Raw
}

// Report is the first terminal report at or above the posted revision, selected
// by (revision ASC, id ASC) in the store's single-snapshot evidence query.
type Report struct {
	Revision   int64
	Status     string
	Detail     string
	FromLog    bool
	Source     string
	GapThrough *int64
}

type Verdict struct {
	Decided     bool
	Settlement  Settlement
	Code        string
	Detail      string
	Basis       string
	Revision    int64
	Contradicts bool
}

// Decide never invents evidence or a deadline verdict. A resolved line keeps its
// operator verdict, but may acquire evidence and a contradiction flag.
func Decide(posted PostedImage, deciding *Report, docValue *Raw, current *Settlement) Verdict {
	if posted.Revision == nil {
		return Verdict{}
	}
	if current != nil {
		switch *current {
		case SettlementAmbiguous, SettlementResolvedConfirmed, SettlementResolvedFailed:
		default:
			return Verdict{}
		}
	}
	if deciding == nil || deciding.Revision < *posted.Revision ||
		(deciding.Status != "APPLIED" && deciding.Status != "REJECTED") {
		return Verdict{}
	}
	equal := posted.Value == nil && docValue == nil
	if posted.Value != nil && docValue != nil {
		a, errA := Canonical(*posted.Value)
		b, errB := Canonical(*docValue)
		if errA != nil || errB != nil {
			return Verdict{}
		}
		equal = bytes.Equal(a, b)
	}
	v := Verdict{Decided: true, Settlement: SettlementSuperseded, Basis: "LATEST_STATE", Revision: deciding.Revision}
	gapThrough := int64(-1)
	if deciding.GapThrough != nil {
		gapThrough = *deciding.GapThrough
	}
	if deciding.FromLog && deciding.Source == "REPORT" && *posted.Revision > gapThrough {
		v.Basis = "REPORT_LOG"
	}
	if equal {
		v.Settlement = SettlementConfirmed
		if deciding.Status == "REJECTED" {
			v.Settlement, v.Code = SettlementFailed, "DEVICE_REJECTED"
			detail := []rune(deciding.Detail)
			if len(detail) > 512 {
				detail = detail[:512]
			}
			v.Detail = string(detail)
		}
	}
	if current != nil {
		v.Contradicts = (*current == SettlementResolvedConfirmed && v.Settlement != SettlementConfirmed) ||
			(*current == SettlementResolvedFailed && v.Settlement != SettlementFailed)
	}
	return v
}
