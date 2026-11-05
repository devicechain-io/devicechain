// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package payloaddecode

import (
	"fmt"
	"strings"
	"testing"

	"github.com/google/cel-go/cel"
	"github.com/google/cel-go/common/types"
)

func exprDoc(expr string) string { return frameDoc("v", expr) }

func compiles(t *testing.T, expr string) error {
	t.Helper()
	_, err := Compile(mustDoc(t, `{"kind":"CEL","envVersion":1,"input":"JSON","sections":[{"name":"s","outputs":[{"key":"v","expr":`+fmt.Sprintf("%q", expr)+`}]}]}`), nil)
	return err
}

// A7: there are no loops. The comprehension macros and matches() do not compile; has() does.
func TestNoLoopsNoRegex(t *testing.T) {
	for _, e := range []string{
		`double([1, 2].map(x, x))`,
		`double(json.a.map(x, x))`,
		`size([1].filter(x, x > 0))`,
		`json.a.all(x, x > 0) ? 1 : 0`,
		`json.a.exists(x, x > 0) ? 1 : 0`,
		`json.a.exists_one(x, x > 0) ? 1 : 0`,
		`"abc".matches("a") ? 1 : 0`,
		`matches("abc", "a") ? 1 : 0`,
		`json.s.matches("a") ? 1 : 0`,
	} {
		if err := compiles(t, e); err == nil {
			t.Errorf("%s must not compile", e)
		}
	}
	for _, e := range []string{`has(json.a) ? 1 : 0`, `has(json.a.b) ? 1 : 0`} {
		if err := compiles(t, e); err != nil {
			t.Errorf("%s must compile: %v", e, err)
		}
	}
}

func TestOverloadAllowlist(t *testing.T) {
	refused := []string{
		// strings and bytes
		`"a" + "b" == "ab" ? 1 : 0`,
		`size("abc")`,
		`string(1) == "1" ? 1 : 0`,
		`int("1")`,
		`double("1")`,
		`frame == frame ? 1 : 0`,
		`frame < frame ? 1 : 0`,
		`frame + frame == frame ? 1 : 0`,
		`frame[0]`,
		`size(frame) + size(frame + frame)`,
		`"abc".size()`,
		`frame.size()`,
		`json.a.startsWith("x") ? 1 : 0`,
		`json.a.contains("x") ? 1 : 0`,
		// dyn / computed access
		`double(json[json.k])`,
		`double(json[string(1)])`,
		`json.a == json.b ? 1 : 0`,
		`double(json.a[0])`,
		// literals and constructors
		`size([1, 2]) `,
		`[1, 2] == [1, 2] ? 1 : 0`,
		`{"a": 1}.a`,
		`u8(b"ab", 0)`,
		`null == null ? 1 : 0`,
		// time, type
		`timestamp("2020-01-01T00:00:00Z") == timestamp("2020-01-01T00:00:00Z") ? 1 : 0`,
		`duration("1s") == duration("1s") ? 1 : 0`,
		`type(1) == int ? 1 : 0`,
		`dyn(1)`,
		// member call syntax, unknown functions
		`json.a.foo()`,
		`random()`,
		`now()`,
		// too many `in` entries, non-literal `in` entries
		`u8(frame,0) in [1,2,3,4,5,6,7,8,9,10,11,12,13,14,15,16,17] ? 1 : 0`,
		`u8(frame,0) in [u8(frame,1)] ? 1 : 0`,
		`u8(frame,0) in json.list ? 1 : 0`,
		// uint arithmetic is not part of the language
		`int(uint(1) + uint(2))`,
		// a let-less identifier
		`double(nothing)`,
	}
	for _, e := range refused {
		if err := compiles(t, e); err == nil {
			t.Errorf("%s must be refused", e)
		}
	}
	accepted := []string{
		`u8(frame, 0) * 2 + 1`,
		`u8(frame, 0) % 3`,
		`-u8(frame, 0)`,
		`u8(frame, 0) > 3 && u8(frame, 1) <= 9 || !(u8(frame, 2) != 4) ? 1 : 0`,
		`double(u8(frame,0)) < 2.5 ? 1.0 : 2.0`,
		`uint(u8(frame,0)) > uint(3) ? 1 : 0`,
		`u8(frame, 0) > uint(3) ? 1 : 0`,
		`json.type == "x" ? 1 : 0`,
		`"x" != json.type ? 1 : 0`,
		`json.type in ["a", "b"] ? 1 : 0`,
		`u8(frame, 0) in [1, 2, 3] ? 1 : 0`,
		`double(json.a.b.c)`,
		`double(json["a"]["b"])`,
		`double(json.a) / 10.0`,
		`bool(json.flag) ? 1 : 0`,
		`double(decoded.x) + double(link.rssi)`,
		`math.clamp(double(json.a), 0.0, 100.0)`,
		`math.pow(2.0, 3) + math.sqrt(4)`,
		`size(slice(frame, 1, 3))`,
		`u8(hex.decode("0a0b"), 1)`,
		`u8(hex.decode(json.payload), 1)`,
		`u16be(base64.decode(json.payload), 0)`,
		`bit(frame, 0, 1) == 1 ? 1 : 0`,
	}
	for _, e := range accepted {
		if err := compiles(t, e); err != nil {
			t.Errorf("%s must compile: %v", e, err)
		}
	}
}

func TestStringLiteralsOnlyWhereBounded(t *testing.T) {
	if err := compiles(t, `u8(hex.decode(json.p), 0)`); err != nil {
		t.Fatal(err)
	}
	// Two non-literal strings compared: unbounded cost, refused.
	if err := compiles(t, `hex.decode(json.a) == hex.decode(json.b) ? 1 : 0`); err == nil {
		t.Fatal("bytes equality must be refused")
	}
}

func TestParserLimits(t *testing.T) {
	long := strings.Repeat("1 + ", 600) + "1"
	if err := compiles(t, long); err == nil {
		t.Fatal("an expression over the size cap must be refused")
	}
	nested := strings.Repeat("(", 40) + "1" + strings.Repeat(")", 40)
	if err := compiles(t, nested); err == nil {
		t.Fatal("an expression nested past the recursion limit must be refused")
	}
	if err := compiles(t, "1 +"); err == nil {
		t.Fatal("a syntax error must be refused")
	}
}

func TestDocumentStructureIssues(t *testing.T) {
	cases := map[string]string{
		"a duplicate let name":        `{"kind":"CEL","envVersion":1,"input":"FRAME","let":[{"name":"a","expr":"u8(frame,0)"},{"name":"a","expr":"u8(frame,1)"}],"sections":[{"name":"s","outputs":[{"key":"v","expr":"a"}]}]}`,
		"a reserved let name":         `{"kind":"CEL","envVersion":1,"input":"FRAME","let":[{"name":"json","expr":"u8(frame,0)"}],"sections":[{"name":"s","outputs":[{"key":"v","expr":"1"}]}]}`,
		"a let named like a function": `{"kind":"CEL","envVersion":1,"input":"FRAME","let":[{"name":"u8","expr":"u8(frame,0)"}],"sections":[{"name":"s","outputs":[{"key":"v","expr":"1"}]}]}`,
		"a let reading a later let":   `{"kind":"CEL","envVersion":1,"input":"FRAME","let":[{"name":"a","expr":"b"},{"name":"b","expr":"u8(frame,0)"}],"sections":[{"name":"s","outputs":[{"key":"v","expr":"1"}]}]}`,
		"no sections":                 `{"kind":"CEL","envVersion":1,"input":"FRAME","sections":[]}`,
		"a bad input kind":            `{"kind":"CEL","envVersion":1,"input":"XML","sections":[{"name":"s","outputs":[{"key":"v","expr":"1"}]}]}`,
		"a non-bool when":             `{"kind":"CEL","envVersion":1,"input":"FRAME","sections":[{"name":"s","when":"fPort","outputs":[{"key":"v","expr":"1"}]}]}`,
		"a string output":             `{"kind":"CEL","envVersion":1,"input":"JSON","sections":[{"name":"s","outputs":[{"key":"v","expr":"json.a"}]}]}`,
		"a dyn output":                `{"kind":"CEL","envVersion":1,"input":"JSON","sections":[{"name":"s","outputs":[{"key":"v","expr":"json.a"}]}]}`,
		"a bytes output":              `{"kind":"CEL","envVersion":1,"input":"FRAME","sections":[{"name":"s","outputs":[{"key":"v","expr":"frame"}]}]}`,
		"a map that is not a map":     `{"kind":"CEL","envVersion":1,"input":"FRAME","sections":[{"name":"s","outputs":[{"map":"u8(frame,0)","maxKeys":4}]}]}`,
		"a map without maxKeys":       `{"kind":"CEL","envVersion":1,"input":"FRAME","sections":[{"name":"s","outputs":[{"map":"lpp.decode(frame)"}]}]}`,
		"a key and a map":             `{"kind":"CEL","envVersion":1,"input":"FRAME","sections":[{"name":"s","outputs":[{"key":"k","map":"lpp.decode(frame)","maxKeys":4}]}]}`,
		"a reserved prefix":           `{"kind":"CEL","envVersion":1,"input":"FRAME","sections":[{"name":"s","outputs":[{"map":"lpp.decode(frame)","prefix":"lorawan.","maxKeys":4}]}]}`,
		"an ignore with outputs":      `{"kind":"CEL","envVersion":1,"input":"FRAME","sections":[{"name":"s","ignore":true,"outputs":[{"key":"v","expr":"1"}]}]}`,
		"an empty section":            `{"kind":"CEL","envVersion":1,"input":"FRAME","sections":[{"name":"s"}]}`,
		"a duplicate section":         `{"kind":"CEL","envVersion":1,"input":"FRAME","sections":[{"name":"s","ignore":true},{"name":"s","ignore":true}]}`,
		"too many sections":           `{"kind":"CEL","envVersion":1,"input":"FRAME","sections":[` + strings.TrimSuffix(strings.Repeat(`{"ignore":true},`, MaxSections+1), ",") + `]}`,
		"65 readings":                 `{"kind":"CEL","envVersion":1,"input":"FRAME","sections":[{"name":"s","outputs":[{"map":"lpp.decode(frame)","maxKeys":64},{"key":"k","expr":"1"}]}]}`,
		"a vector with no name":       `{"kind":"CEL","envVersion":1,"input":"FRAME","sections":[{"name":"s","outputs":[{"key":"v","expr":"1"}]}],"vectors":[{"input":{"frameHex":"00"},"expect":{}}]}`,
		"a vector with bad hex":       `{"kind":"CEL","envVersion":1,"input":"FRAME","sections":[{"name":"s","outputs":[{"key":"v","expr":"1"}]}],"vectors":[{"name":"x","input":{"frameHex":"zz"},"expect":{}}]}`,
	}
	for name, js := range cases {
		d, err := ParseDocument([]byte(js))
		if err != nil {
			t.Errorf("%s: parse: %v", name, err)
			continue
		}
		if _, err := Compile(d, nil); err == nil {
			t.Errorf("%s: must be refused", name)
		}
	}
}

// The error names every issue by expression path, in one pass.
func TestCompileErrorsNameExpressionPaths(t *testing.T) {
	ce := compileErr(t, `{"kind":"CEL","envVersion":1,"input":"FRAME",
	  "let":[{"name":"a","expr":"u8(frame, "}],
	  "sections":[{"name":"s","when":"u8(frame,0)","outputs":[{"key":"v","expr":"u8(frame, 0, 1)"},{"key":"w","expr":"nope(1)"}]}]}`, nil)
	msg := ce.Error()
	for _, p := range []string{"let[0].expr", "sections[0].when", "sections[0].outputs[0].expr", "sections[0].outputs[1].expr"} {
		if !strings.Contains(msg, p) {
			t.Errorf("error does not name %s: %s", p, msg)
		}
	}
}

var loopExprs = []string{
	`[1, 2].map(x, x)`, `json.a.map(x, x)`, `[1].filter(x, x > 0)`, `json.a.all(x, x > 0)`,
	`json.a.exists(x, x > 0)`, `json.a.exists_one(x, x > 0)`, `"abc".matches("a")`, `matches("abc", "a")`,
}

// A7, fence one: the environment itself has no comprehension macro and no regex function, so
// the CHECKER refuses them before the walker is ever reached.
func TestEnvAloneRefusesLoops(t *testing.T) {
	env, err := baseEnv()
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range loopExprs {
		if _, iss := env.Compile(e); iss == nil || iss.Err() == nil {
			t.Errorf("the environment alone must refuse %s", e)
		}
	}
	if _, iss := env.Compile(`has(json.a)`); iss != nil && iss.Err() != nil {
		t.Errorf("has() must compile: %v", iss.Err())
	}
}

// A7, fence two: the AST walker refuses them even if the environment is built with every
// default macro and the whole standard library (a later cel-go release, or a mistake here).
func TestWalkerAloneRefusesLoops(t *testing.T) {
	env, err := cel.NewEnv(
		cel.Variable(VarJSON, cel.MapType(cel.StringType, cel.DynType)),
		cel.Variable("x", cel.IntType),
	)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range loopExprs {
		a, iss := env.Compile(e)
		if iss != nil && iss.Err() != nil {
			t.Fatalf("the default environment should accept %s: %v", e, iss.Err())
		}
		w := &walker{ast: a.NativeRep(), lets: map[string]*types.Type{}}
		if err := w.check(a.NativeRep().Expr()); err == nil {
			t.Errorf("the walker must refuse %s", e)
		}
	}
}
