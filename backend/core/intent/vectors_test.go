// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package intent

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// vectorFile is testdata/vectors.json. Every expected value in it is a fixed literal, written
// once and checked by hand (the value, observation, spec and inputs digests were recomputed with
// an independent SHA-256 over the documented byte layouts). Nothing regenerates it: a change to
// any canonical byte or digest format must fail here and be made deliberately, together with a
// new CompilerBuild, because stored plans and approvals are keyed by these bytes.
type vectorFile struct {
	CompilerBuild string `json:"compilerBuild"`
	Specs         []struct {
		Name       string `json:"name"`
		Input      string `json:"input"`
		Canonical  string `json:"canonical"`
		SpecDigest string `json:"specDigest"`
	} `json:"specs"`
	ValueDigests []struct {
		ValueType string `json:"valueType"`
		Value     string `json:"value"`
		Digest    string `json:"digest"`
	} `json:"valueDigests"`
	Observations []struct {
		Name string `json:"name"`
		Rows []struct {
			ID          int64   `json:"id"`
			Token       string  `json:"token"`
			Declared    *string `json:"declared"`
			Present     bool    `json:"present"`
			ValueType   string  `json:"valueType"`
			ValueDigest string  `json:"valueDigest"`
		} `json:"rows"`
		Digest string `json:"digest"`
	} `json:"observations"`
	Inputs []struct {
		SpecDigest        string `json:"specDigest"`
		ObservationDigest string `json:"observationDigest"`
		InstantBucket     int64  `json:"instantBucket"`
		CompilerBuild     string `json:"compilerBuild"`
		Digest            string `json:"digest"`
	} `json:"inputs"`
}

func loadVectors(t testing.TB) vectorFile {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "vectors.json"))
	if err != nil {
		t.Fatal(err)
	}
	var v vectorFile
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&v); err != nil {
		t.Fatal(err)
	}
	if len(v.Specs) < 8 || len(v.ValueDigests) < 8 || len(v.Observations) < 2 || len(v.Inputs) < 2 {
		t.Fatalf("testdata/vectors.json is missing vectors: %d specs, %d values, %d observations, %d inputs",
			len(v.Specs), len(v.ValueDigests), len(v.Observations), len(v.Inputs))
	}
	return v
}

func TestPinnedVectors(t *testing.T) {
	v := loadVectors(t)
	if v.CompilerBuild != CompilerBuild {
		t.Errorf("CompilerBuild = %q; the vectors pin %q. Change both together, deliberately", CompilerBuild, v.CompilerBuild)
	}
	for _, tc := range v.Specs {
		s, err := Parse([]byte(tc.Input))
		if err != nil {
			t.Errorf("%s: Parse: %v", tc.Name, err)
			continue
		}
		canonical, err := Canonical(s)
		if err != nil || string(canonical) != tc.Canonical {
			t.Errorf("%s: canonical bytes moved:\n got %s\nwant %s (%v)", tc.Name, canonical, tc.Canonical, err)
		}
		if got, err := SpecDigest(s); err != nil || got != tc.SpecDigest {
			t.Errorf("%s: SpecDigest = %s, %v; want %s", tc.Name, got, err, tc.SpecDigest)
		}
	}
	for _, tc := range v.ValueDigests {
		if got, err := ValueDigest(tc.ValueType, tc.Value); err != nil || got != tc.Digest {
			t.Errorf("ValueDigest(%s, %q) = %s, %v; want %s", tc.ValueType, tc.Value, got, err, tc.Digest)
		}
	}
	for _, tc := range v.Observations {
		rows := make([]Row, 0, len(tc.Rows))
		for _, r := range tc.Rows {
			rows = append(rows, Row{DeviceID: r.ID, Token: r.Token, DeclaredValueType: r.Declared,
				Present: r.Present, ValueType: r.ValueType, ValueDigest: r.ValueDigest})
		}
		if got, err := ObservationDigest(rows); err != nil || got != tc.Digest {
			t.Errorf("%s: ObservationDigest = %s, %v; want %s", tc.Name, got, err, tc.Digest)
		}
	}
	for i, tc := range v.Inputs {
		if got, err := InputsDigest(tc.SpecDigest, tc.ObservationDigest, tc.InstantBucket, tc.CompilerBuild); err != nil || got != tc.Digest {
			t.Errorf("inputs vector %d: InputsDigest = %s, %v; want %s", i, got, err, tc.Digest)
		}
	}
}
