// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package payloaddecode

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"github.com/devicechain-io/dc-microservice/calcfield"
)

// testMetrics is a Metrics over a fixed set of declared metrics, with the storable-magnitude
// rule the resolver applies and an optional extra validator.
type testMetrics struct {
	types map[string]calcfield.Type
	extra func(key, value string) error
}

func (m testMetrics) MetricType(k string) (calcfield.Type, bool) { t, ok := m.types[k]; return t, ok }

func (m testMetrics) ValidateMeasurement(k, v string) error {
	if m.extra != nil {
		return m.extra(k, v)
	}
	return nil
}

func tm(kv ...any) testMetrics {
	m := testMetrics{types: map[string]calcfield.Type{}}
	for i := 0; i < len(kv); i += 2 {
		m.types[kv[i].(string)] = kv[i+1].(calcfield.Type)
	}
	return m
}

func mustDoc(t testing.TB, js string) *Document {
	t.Helper()
	d, err := ParseDocument([]byte(js))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	return d
}

func mustCompile(t testing.TB, js string, m Metrics) *Plan {
	t.Helper()
	p, err := Compile(mustDoc(t, js), m)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	return p
}

func compileErr(t testing.TB, js string, m Metrics) *CompileError {
	t.Helper()
	_, err := Compile(mustDoc(t, js), m)
	if err == nil {
		t.Fatalf("expected a compile error")
	}
	var ce *CompileError
	if !errors.As(err, &ce) {
		t.Fatalf("expected *CompileError, got %T: %v", err, err)
	}
	return ce
}

// frameDoc builds a single-section FRAME decoder from key/expr pairs.
func frameDoc(rows ...string) string {
	outs := ""
	for i := 0; i < len(rows); i += 2 {
		if i > 0 {
			outs += ","
		}
		outs += fmt.Sprintf(`{"key":%q,"expr":%q}`, rows[i], rows[i+1])
	}
	return fmt.Sprintf(`{"kind":"CEL","envVersion":1,"input":"FRAME","sections":[{"name":"s","outputs":[%s]}]}`, outs)
}

func frameOf(h string) Input {
	b, err := hex.DecodeString(h)
	if err != nil {
		panic(err)
	}
	return Input{Frame: b}
}

func decodeOK(t testing.TB, p *Plan, in Input) *Result {
	t.Helper()
	r, err := p.Decode(in)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	return r
}

func readingsMap(r *Result) map[string]string {
	m := map[string]string{}
	for _, rd := range r.Readings {
		m[rd.Key] = rd.Value
	}
	return m
}

func mustJSON(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}
