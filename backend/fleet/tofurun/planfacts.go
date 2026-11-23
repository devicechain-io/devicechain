// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package tofurun

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// FactsVersion is the version of the canonical facts form.
const FactsVersion = 1

// Fact is one resource change of a plan, reduced to what a gate needs and a person may read.
// It carries NO attribute values: only the paths that change, so a sensitive value cannot reach
// the manager through it. An unreadable change is a refusal fact, never a skip.
type Fact struct {
	Address      string   `json:"address"`
	Mode         string   `json:"mode,omitempty"`
	Type         string   `json:"type,omitempty"`
	Provider     string   `json:"provider,omitempty"`
	Actions      []string `json:"actions,omitempty"`
	ReplacePaths []string `json:"replace_paths,omitempty"`
	ChangedPaths []string `json:"changed_paths,omitempty"`
	// Refusal is non-empty when this change could not be read; the gate forbids any such fact.
	Refusal string `json:"refusal,omitempty"`
}

// PlanFacts is the canonical, value-free reduction of `tofu show -json <plan>`.
type PlanFacts struct {
	Version     int    `json:"facts_version"`
	FormatVer   string `json:"format_version"`
	ChangeCount int    `json:"resource_change_count"`
	// DriftCount is the number of resources the refresh found changed outside tofu.
	DriftCount int    `json:"resource_drift_count"`
	Facts      []Fact `json:"facts"`
}

// Extract reduces plan JSON to facts. It is total: it never returns an error and never skips, so an
// unknown format version, an unparsable document, an unreadable change or an unknown action each
// yield a refusal fact the gate refuses. The count equals the number of facts.
func Extract(doc []byte) PlanFacts {
	pf := PlanFacts{Version: FactsVersion}
	refuse := func(why string) PlanFacts {
		pf.Facts = append(pf.Facts, Fact{Address: "(plan)", Refusal: why})
		pf.ChangeCount = len(pf.Facts)
		return pf
	}
	var p struct {
		FormatVersion   string            `json:"format_version"`
		ResourceChanges []json.RawMessage `json:"resource_changes"`
		ResourceDrift   []json.RawMessage `json:"resource_drift"`
	}
	if err := json.NewDecoder(bytes.NewReader(doc)).Decode(&p); err != nil {
		return refuse("the plan document does not parse")
	}
	pf.FormatVer = p.FormatVersion
	if major, _, _ := strings.Cut(p.FormatVersion, "."); major != "1" {
		return refuse(fmt.Sprintf("unknown plan format_version %q", truncate(p.FormatVersion, 20)))
	}
	pf.DriftCount = len(p.ResourceDrift)
	for _, raw := range p.ResourceChanges {
		pf.Facts = append(pf.Facts, extractOne(raw))
	}
	sort.SliceStable(pf.Facts, func(i, j int) bool {
		if pf.Facts[i].Address != pf.Facts[j].Address {
			return pf.Facts[i].Address < pf.Facts[j].Address
		}
		return strings.Join(pf.Facts[i].Actions, ",") < strings.Join(pf.Facts[j].Actions, ",")
	})
	pf.ChangeCount = len(pf.Facts)
	return pf
}

var knownActions = map[string]bool{"no-op": true, "read": true, "create": true, "update": true, "delete": true, "forget": true}

func extractOne(raw json.RawMessage) Fact {
	var rc struct {
		Address      string `json:"address"`
		Mode         string `json:"mode"`
		Type         string `json:"type"`
		ProviderName string `json:"provider_name"`
		Deposed      string `json:"deposed"`
		Change       *struct {
			Actions      []string        `json:"actions"`
			Before       json.RawMessage `json:"before"`
			After        json.RawMessage `json:"after"`
			ReplacePaths json.RawMessage `json:"replace_paths"`
		} `json:"change"`
	}
	if err := json.Unmarshal(raw, &rc); err != nil || rc.Address == "" || rc.Change == nil {
		return Fact{Address: "(unreadable)", Refusal: "a resource change does not parse"}
	}
	f := Fact{Address: rc.Address, Mode: rc.Mode, Type: rc.Type, Provider: rc.ProviderName, Actions: rc.Change.Actions}
	if rc.Deposed != "" {
		f.Address += " (deposed)"
	}
	if len(f.Actions) == 0 {
		f.Refusal = "the change has no actions"
		return f
	}
	for _, a := range f.Actions {
		if !knownActions[a] {
			f.Refusal = fmt.Sprintf("unknown action %q", truncate(a, 20))
			return f
		}
	}
	// replace_paths is an array of attribute-path arrays: keep only the dotted path text.
	var rp [][]any
	if len(rc.Change.ReplacePaths) > 0 && json.Unmarshal(rc.Change.ReplacePaths, &rp) == nil {
		for _, p := range rp {
			f.ReplacePaths = append(f.ReplacePaths, joinPath(p))
		}
		sort.Strings(f.ReplacePaths)
	}
	f.ChangedPaths = diffPaths(rc.Change.Before, rc.Change.After)
	return f
}

func joinPath(p []any) string {
	parts := make([]string, len(p))
	for i, e := range p {
		parts[i] = fmt.Sprint(e)
	}
	return strings.Join(parts, ".")
}

// diffPaths lists the attribute paths whose value differs between before and after, never the
// values. A document that does not decode yields no paths (the actions still stand).
func diffPaths(before, after json.RawMessage) []string {
	var b, a any
	if len(before) > 0 {
		_ = json.Unmarshal(before, &b)
	}
	if len(after) > 0 {
		_ = json.Unmarshal(after, &a)
	}
	var out []string
	walkDiff("", b, a, &out)
	sort.Strings(out)
	return out
}

func walkDiff(path string, b, a any, out *[]string) {
	bm, bok := b.(map[string]any)
	am, aok := a.(map[string]any)
	if bok && aok {
		keys := map[string]bool{}
		for k := range bm {
			keys[k] = true
		}
		for k := range am {
			keys[k] = true
		}
		for k := range keys {
			p := k
			if path != "" {
				p = path + "." + k
			}
			walkDiff(p, bm[k], am[k], out)
		}
		return
	}
	bb, _ := json.Marshal(b)
	ab, _ := json.Marshal(a)
	if !bytes.Equal(bb, ab) {
		if path == "" {
			path = "(root)"
		}
		*out = append(*out, path)
	}
}

// Canonical is the facts' canonical JSON: fixed field order, sorted facts, no values.
func (p PlanFacts) Canonical() []byte {
	b, _ := json.Marshal(p)
	return b
}

// Digest is the hex sha256 of the canonical form: what an approval binds to.
func (p PlanFacts) Digest() string {
	sum := sha256.Sum256(p.Canonical())
	return hex.EncodeToString(sum[:])
}
