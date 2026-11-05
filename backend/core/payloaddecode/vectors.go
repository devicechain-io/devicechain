// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package payloaddecode

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// VectorResult is one saved vector's outcome.
type VectorResult struct {
	Name string
	OK   bool
	// Error says why it failed: the decode error, or the first mismatch.
	Error string
	// Got is what the decoder produced, key to value text.
	Got map[string]string
	// Cost is the actual cost of the run.
	Cost uint64
}

// RunVectors runs every saved vector through the plan and compares the readings with the
// expected ones. A vector passes when the decode succeeds and the readings are EXACTLY the
// expected keys with numerically equal values (an extra or missing key fails it), or when the
// decode is an ignore section and nothing was expected. Publish refuses unless all pass.
func (p *Plan) RunVectors() []VectorResult {
	out := make([]VectorResult, 0, len(p.vectors))
	for _, v := range p.vectors {
		r := VectorResult{Name: v.Name}
		in, err := v.Input.toInput()
		if err != nil {
			r.Error = err.Error()
			out = append(out, r)
			continue
		}
		res, derr := p.Decode(in)
		if derr != nil {
			r.Error = derr.Error()
			r.Cost = derr.Cost
			out = append(out, r)
			continue
		}
		r.Cost = res.Cost
		r.Got = make(map[string]string, len(res.Readings))
		for _, rd := range res.Readings {
			r.Got[rd.Key] = rd.Value
		}
		r.Error = compareExpect(v.Expect, r.Got, res.Ignored)
		r.OK = r.Error == ""
		out = append(out, r)
	}
	return out
}

func compareExpect(expect map[string]json.Number, got map[string]string, ignored bool) string {
	if ignored && len(expect) == 0 {
		return ""
	}
	keys := make([]string, 0, len(expect))
	for k := range expect {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		g, ok := got[k]
		if !ok {
			return fmt.Sprintf("expected %s, which was not produced", k)
		}
		want, err1 := strconv.ParseFloat(expect[k].String(), 64)
		have, err2 := strconv.ParseFloat(g, 64)
		if err1 != nil || err2 != nil {
			return fmt.Sprintf("%s: cannot compare %q with %q", k, expect[k].String(), g)
		}
		if want != have {
			return fmt.Sprintf("%s: expected %s, got %s", k, expect[k].String(), g)
		}
	}
	extra := []string{}
	for k := range got {
		if _, ok := expect[k]; !ok {
			extra = append(extra, k)
		}
	}
	sort.Strings(extra)
	if len(extra) > 0 {
		return fmt.Sprintf("produced %s, which the vector did not expect", strings.Join(extra, ", "))
	}
	return ""
}
