// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package payloaddecode

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"flag"
	"fmt"
	"math/rand"
	"os"
	"runtime"
	"strings"
	"testing"
)

var updateGolden = flag.Bool("update", false, "rewrite testdata/determinism.golden")

// outcome renders a decode as one line: every reading in order, or the error, plus the cost.
func outcome(res *Result, derr *DecodeError) string {
	if derr != nil {
		return "ERR " + derr.Error() + fmt.Sprintf(" cost=%d", derr.Cost)
	}
	var b strings.Builder
	b.WriteString("OK section=" + res.Section)
	if res.Ignored {
		b.WriteString(" ignored")
	}
	for _, r := range res.Readings {
		b.WriteString(" " + r.Key + "=" + r.Value)
	}
	for _, s := range res.Skipped {
		b.WriteString(" skip:" + s.Key + ":" + s.Reason)
	}
	fmt.Fprintf(&b, " cost=%d", res.Cost)
	return b.String()
}

func shortHash(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:8])
}

// floatHeavy are documents chosen for the operations whose result could differ by an ulp
// between architectures: float reads, division, sqrt, integer powers, rounding, and a long
// chain of dependent double arithmetic.
var floatHeavy = []string{
	frameDoc(
		"a", "f32be(frame, 0)", "b", "f64le(frame, 2)", "c", "f32le(frame, 1) / 3.0",
		"d", "math.sqrt(math.abs(f64be(frame, 0)))", "e", "math.pow(f32be(frame, 4) + 1.0, 5)",
		"f", "math.round(f32be(frame, 0) * 100.0) / 100.0", "g", "double(i32be(frame, 0)) / 7.0 * 3.0 - 0.1",
		"h", "double(u32le(frame, 0)) * 1.1 * 1.3 / 0.7 + double(i16be(frame, 2)) / 3.0",
		"i", "math.floor(double(i24be(frame, 0)) / 9.0) + math.ceil(f32be(frame, 0))",
		"j", "math.clamp(f64be(frame, 0), -1.0e300, 1.0e300) / 3.0",
	),
	`{"kind":"CEL","envVersion":1,"input":"FRAME","sections":[{"name":"s","outputs":[{"map":"lpp.decode(frame)","prefix":"p.","maxKeys":64}]}]}`,
}

// determinismCorpus is every (document, input) pair the golden covers.
func determinismCorpus() (docs []string, inputs []Input) {
	g := &exprGen{r: rand.New(rand.NewSource(1105))}
	for i := 0; i < 150; i++ {
		g.lets, g.maps, g.safe = nil, nil, i%2 == 0
		docs = append(docs, randomDoc(g))
	}
	docs = append(docs, floatHeavy...)
	inputs = propertyInputs()
	rnd := rand.New(rand.NewSource(5))
	for i := 0; i < 3; i++ {
		b := make([]byte, 24)
		rnd.Read(b)
		inputs = append(inputs, Input{Frame: b, LoRa: &LoRa{FPort: int64(i), FCnt: 1}})
	}
	inputs = append(inputs, frameOf("4048f5c3400921fb54442d18"), frameOf("3f8000007f7fffff00800000"), frameOf("01670110"+"026865"+"037103e8fc1801f4"))
	return
}

func computeGolden() []string {
	docs, inputs := determinismCorpus()
	var lines []string
	for i, js := range docs {
		d, err := ParseDocument([]byte(js))
		if err != nil {
			panic(err)
		}
		p, err := Compile(d, nil)
		if err != nil {
			lines = append(lines, fmt.Sprintf("%03d refused %s", i, shortHash(err.Error())))
			continue
		}
		for k, in := range inputs {
			res, derr := p.Decode(in)
			lines = append(lines, fmt.Sprintf("%03d.%02d %s", i, k, shortHash(outcome(res, derr))))
		}
	}
	return lines
}

// A9: the readings are a pure function of (document, input). The golden is committed, was
// written on amd64, and is compared here on whatever architecture runs the tests: CI runs it
// on amd64 and arm64, so a result that depends on the CPU (an FMA fusion, a libm difference)
// fails one of them.
func TestDeterminismGolden(t *testing.T) {
	got := computeGolden()
	const path = "testdata/determinism.golden"
	if *updateGolden {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(strings.Join(got, "\n")+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("wrote %d lines for %s", len(got), runtime.GOARCH)
		return
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var want []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		want = append(want, strings.TrimSpace(sc.Text()))
	}
	if len(want) != len(got) {
		t.Fatalf("%s: golden has %d lines, this run produced %d", runtime.GOARCH, len(want), len(got))
	}
	bad := 0
	docs, inputs := determinismCorpus()
	for i := range want {
		if want[i] != got[i] {
			if bad++; bad <= 5 {
				idx := 0
				fmt.Sscanf(want[i], "%d", &idx)
				t.Errorf("%s differs from the golden at %q (got %q)\ndocument:\n%s", runtime.GOARCH, want[i], got[i], docs[idx])
				_ = inputs
			}
		}
	}
	if bad > 0 {
		t.Fatalf("%d of %d outcomes differ on %s", bad, len(want), runtime.GOARCH)
	}
}

// A9: a thousand runs of every float-heavy document over the same inputs are byte-identical.
func TestDeterminismThousandRuns(t *testing.T) {
	_, inputs := determinismCorpus()
	runs := 1000
	if testing.Short() {
		runs = 100
	}
	for di, js := range floatHeavy {
		p := mustCompile(t, js, nil)
		for k, in := range inputs {
			var first string
			for r := 0; r < runs; r++ {
				res, derr := p.Decode(in)
				o := outcome(res, derr)
				if r == 0 {
					first = o
				} else if o != first {
					t.Fatalf("doc %d input %d run %d differs:\n%s\n%s", di, k, r, o, first)
				}
			}
		}
	}
}
