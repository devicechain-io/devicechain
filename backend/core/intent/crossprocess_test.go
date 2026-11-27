// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package intent

import (
	"encoding/json"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// digestChildEnv, when set, names the file a re-executed test binary writes its digests to.
const digestChildEnv = "DC_INTENT_DIGEST_CHILD_OUT"

type digestRecord struct {
	Canonical   string `json:"canonical"`
	SpecDigest  string `json:"specDigest"`
	ValueDigest string `json:"valueDigest"`
	Observation string `json:"observation"`
	Inputs      string `json:"inputs"`
	Codes       []Code `json:"codes"`
}

// randomSpec builds a valid spec from r: every field varies, including values full of
// characters that JSON escapes, non-ASCII text and every value type.
func randomSpec(r *rand.Rand) Spec {
	token := func() string {
		const first = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
		const rest = first + "-_"
		b := []byte{first[r.IntN(len(first))]}
		for n := r.IntN(40); n > 0; n-- {
			b = append(b, rest[r.IntN(len(rest))])
		}
		return string(b)
	}
	text := func() string {
		alphabet := []rune("az09 \"\\/\n\t\x1f\x7féü☃😀<>&: ")
		var b strings.Builder
		for n := r.IntN(60); n > 0; n-- {
			b.WriteRune(alphabet[r.IntN(len(alphabet))])
		}
		return b.String()
	}
	var v Value
	switch r.IntN(6) {
	case 0:
		v = Value{Unset: true}
	case 1:
		v = Value{ValueType: ValueTypeLong, Value: ptr(jsonNumber(r.Int64() - r.Int64()))}
	case 2:
		v = Value{ValueType: ValueTypeDouble, Value: ptr(jsonFloat(r.NormFloat64() * 1e6))}
	case 3:
		v = Value{ValueType: ValueTypeBoolean, Value: ptr([]string{"true", "false", "1", "F"}[r.IntN(4)])}
	case 4:
		doc, _ := json.Marshal(map[string]any{"k": text(), "n": r.IntN(1000), "l": []any{true, nil, text()}})
		v = Value{ValueType: ValueTypeJSON, Value: ptr(string(doc))}
	default:
		v = Value{ValueType: ValueTypeString, Value: ptr(text())}
	}
	return Spec{
		Target:  Target{GroupToken: token(), GroupVersion: 1 + r.Int64N(1<<31-1)},
		Point:   Point{ConfigKey: token()},
		Value:   v,
		Class:   []Class{ClassSchedule, ClassOptimization}[r.IntN(2)],
		Ceiling: Ceiling{MaxDevices: 1 + r.Int64N(MaxDevicesPerIntent)},
		Reconcile: Reconcile{
			MinIntervalSeconds:           MinIntervalSeconds + r.Int64N(MaxIntervalSeconds-MinIntervalSeconds+1),
			MaxLinesPerReconcile:         1 + r.Int64N(MaxLinesPerReconcile),
			MaxActuationsPerDevicePerDay: 1 + r.Int64N(MaxActuationsPerDevicePerDay),
			ManualDwellSeconds:           r.Int64N(MaxManualDwellSeconds + 1),
		},
	}
}

func jsonNumber(n int64) string  { b, _ := json.Marshal(n); return string(b) }
func jsonFloat(f float64) string { b, _ := json.Marshal(f); return string(b) }

// digestReport computes every digest over a fixed corpus: the pinned vectors plus 300 random
// specs from a fixed seed, each with a random observation of its own.
func digestReport(t testing.TB) []digestRecord {
	t.Helper()
	var specs []Spec
	for _, v := range loadVectors(t).Specs {
		specs = append(specs, mustParse(t, v.Input))
	}
	r := rand.New(rand.NewPCG(20261127, 1))
	for i := 0; i < 300; i++ {
		specs = append(specs, randomSpec(r))
	}
	types := []string{ValueTypeString, ValueTypeLong, ValueTypeDouble, ValueTypeBoolean, ValueTypeJSON}
	out := make([]digestRecord, 0, len(specs))
	for _, s := range specs {
		canonical, err := Canonical(s)
		if err != nil {
			t.Fatalf("Canonical(%+v): %v", s, err)
		}
		specDigest, err := SpecDigest(s)
		if err != nil {
			t.Fatal(err)
		}
		want, err := IntendedValue(s, 1, epoch)
		if err != nil {
			t.Fatal(err)
		}
		rows := make([]Row, 0, 20)
		id := int64(0)
		for n := r.IntN(20); n > 0; n-- {
			id += 1 + r.Int64N(1000)
			row := Row{DeviceID: id, Token: "d" + jsonNumber(id)}
			if r.IntN(5) > 0 {
				row.DeclaredValueType = ptr(types[r.IntN(len(types))])
			}
			switch r.IntN(3) {
			case 0:
			case 1:
				if !want.Unset {
					row.Present, row.ValueType, row.ValueDigest = true, want.ValueType, want.ValueDigest
					break
				}
				fallthrough
			default:
				row.Present, row.ValueType, row.ValueDigest = true, ValueTypeString, vd(t, ValueTypeString, jsonNumber(id))
			}
			rows = append(rows, row)
		}
		observation, err := ObservationDigest(rows)
		if err != nil {
			t.Fatal(err)
		}
		bucket, err := InstantBucket(s, epoch)
		if err != nil {
			t.Fatal(err)
		}
		inputs, err := InputsDigest(specDigest, observation, bucket, CompilerBuild)
		if err != nil {
			t.Fatal(err)
		}
		s.Ceiling.MaxDevices = MaxDevicesPerIntent // the random ceiling is not under test here
		codes := make([]Code, 0, len(rows))
		for _, row := range rows {
			code, err := Classify(s, row, epoch)
			if err != nil {
				t.Fatal(err)
			}
			codes = append(codes, code)
		}
		out = append(out, digestRecord{Canonical: string(canonical), SpecDigest: specDigest,
			ValueDigest: want.ValueDigest, Observation: observation, Inputs: inputs, Codes: codes})
	}
	return out
}

// TestDigestHelperProcess is the child half of TestDigestsAreStableAcrossProcesses. It does
// nothing unless the parent re-executed the test binary with digestChildEnv set.
func TestDigestHelperProcess(t *testing.T) {
	path := os.Getenv(digestChildEnv)
	if path == "" {
		return
	}
	raw, err := json.Marshal(digestReport(t))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestDigestsAreStableAcrossProcesses compiles the same corpus in separate processes (a
// re-execution of this test binary, so map iteration order, hash seeds and allocation all differ
// from this process) and requires every canonical byte, digest and code to agree.
func TestDigestsAreStableAcrossProcesses(t *testing.T) {
	if os.Getenv(digestChildEnv) != "" {
		t.Skip("running as the child")
	}
	want := digestReport(t)
	for i, procs := range []string{"1", "4"} {
		out := filepath.Join(t.TempDir(), "digests.json")
		cmd := exec.Command(os.Args[0], "-test.run=^TestDigestHelperProcess$", "-test.count=1")
		cmd.Env = append(os.Environ(), digestChildEnv+"="+out, "GOMAXPROCS="+procs)
		if combined, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("child %d: %v\n%s", i, err, combined)
		}
		raw, err := os.ReadFile(out)
		if err != nil {
			t.Fatalf("child %d wrote no digests: %v", i, err)
		}
		var got []digestRecord
		if err := json.Unmarshal(raw, &got); err != nil {
			t.Fatal(err)
		}
		if len(got) != len(want) {
			t.Fatalf("child %d reported %d records, this process %d", i, len(got), len(want))
		}
		for j := range want {
			if !reflect.DeepEqual(got[j], want[j]) {
				t.Errorf("child %d record %d differs:\n child %+v\n here  %+v", i, j, got[j], want[j])
			}
		}
	}
}
