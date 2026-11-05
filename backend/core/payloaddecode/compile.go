// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package payloaddecode

import (
	"fmt"
	"strings"
	"sync"

	"github.com/google/cel-go/cel"
	"github.com/google/cel-go/checker"
	"github.com/google/cel-go/common/types"

	"github.com/devicechain-io/dc-microservice/calcfield"
)

// Metrics is the profile's declared metrics, as the decoder sees them. The device-management
// resolution implements it; a nil Metrics turns the strict-key check, type-aware formatting
// and output validation off (used by tools that have no profile).
type Metrics interface {
	// MetricType is the type of a declared, MEASURED, non-calculated metric. A calculated
	// metric reports false: it cannot be a decoder output, because the resolver drops a
	// device-sent calculated key.
	MetricType(key string) (calcfield.Type, bool)
	// ValidateMeasurement is the resolver's own validation of one stored value (type, storable
	// magnitude, min/max, enum). It runs inside the decode step, so a bad output is a decode
	// failure, never a retried resolve failure.
	ValidateMeasurement(key, value string) error
}

// expr is one compiled expression.
type expr struct {
	prog cel.Program
	path string
	// cost is the static worst-case cost; it is also the program's runtime CostLimit.
	cost uint64
}

type row struct {
	path    string // sections[i].outputs[j]
	key     string
	value   *expr // scalar value or map source
	when    *expr
	isMap   bool
	prefix  string
	maxKeys int
	typ     calcfield.Type
	typed   bool
}

type section struct {
	name   string
	path   string
	when   *expr
	ignore bool
	rows   []row
}

type letDef struct {
	name string
	slot int
	expr *expr
}

// Plan is a compiled decoder: immutable and safe for concurrent use. Compile once per
// published profile version, Decode for every uplink.
type Plan struct {
	input    InputKind
	lets     []letDef
	sections []section
	slots    map[string]int
	metrics  Metrics
	vectors  []Vector

	usesJSON, usesDecoded bool
	maxReadings           int
	// composedCost is the composition of the expression estimates (see Compile); estimatedCost
	// adds the worst-case parse of the JSON inputs the decoder reads.
	composedCost, estimatedCost uint64

	pool sync.Pool
}

const (
	slotFrame = iota
	slotFPort
	slotFCnt
	slotJSON
	slotDecoded
	slotLink
	firstLetSlot
)

// MaxReadings is the decoder's static output count: the maximum over sections of its scalar
// rows plus its map rows' maxKeys. The ingest charge is computed from it before any decode.
func (p *Plan) MaxReadings() int { return p.maxReadings }

// EstimatedCost is the static worst-case cost of a whole decode: the composition in Compile
// plus the worst-case parse (ParseCost of MaxJSONBytes) of each JSON input the decoder reads.
// It is admission data. A caller that holds the real uplink, as ingest does, charges
// CostFor(input) instead, which prices the parse by the actual size.
func (p *Plan) EstimatedCost() uint64 { return p.estimatedCost }

// CostFor is the static worst-case cost of decoding in: the composition plus the parse of the
// JSON inputs the decoder reads, at their actual sizes. It is at most EstimatedCost, and a
// successful or failed Decode of in never spends more than it.
func (p *Plan) CostFor(in Input) uint64 {
	c := p.composedCost
	if p.usesJSON {
		c += ParseCost(len(in.JSON))
	}
	if p.usesDecoded {
		c += ParseCost(len(in.Decoded))
	}
	return c
}

// InputKind is the input an uplink must carry for the decoder to run.
func (p *Plan) InputKind() InputKind { return p.input }

type compiler struct {
	issues []Issue
	costs  []*CostError
}

func (c *compiler) issue(path, format string, args ...any) {
	c.issues = append(c.issues, Issue{Path: path, Message: fmt.Sprintf(format, args...)})
}

// Compile parses nothing: it takes an already-parsed document. It fails closed — a document
// that does not compile, or that is over a ceiling, is a *CompileError listing every issue by
// expression path. An unsupported kind is ErrUnsupportedDecoderKind and an unknown function
// library version is ErrEnvUnsupported; both match with errors.Is.
func Compile(doc *Document, metrics Metrics) (*Plan, error) {
	if doc == nil {
		return nil, &CompileError{Issues: []Issue{{Message: "no decoder document"}}}
	}
	switch doc.Kind {
	case KindCEL:
	case KindWASM:
		return nil, fmt.Errorf("%w: WASM decoders are not available in this build", ErrUnsupportedDecoderKind)
	default:
		return nil, &CompileError{Issues: []Issue{{Path: "kind", Message: fmt.Sprintf("unknown decoder kind %q", doc.Kind)}}}
	}
	if doc.EnvVersion != EnvVersion {
		return nil, fmt.Errorf("%w: the document names version %d, this build knows version %d", ErrEnvUnsupported, doc.EnvVersion, EnvVersion)
	}
	c := &compiler{}
	p := &Plan{input: doc.Input, metrics: metrics, vectors: doc.Vectors, slots: map[string]int{
		VarFrame: slotFrame, VarFPort: slotFPort, VarFCnt: slotFCnt, VarJSON: slotJSON, VarDecoded: slotDecoded, VarLink: slotLink,
	}}
	switch doc.Input {
	case InputFrame, InputJSON, InputDecoded:
	default:
		c.issue("input", "input must be FRAME, JSON or DECODED, not %q", doc.Input)
	}
	if len(doc.Let) > MaxLets {
		c.issue("let", "%d let bindings is above the maximum of %d", len(doc.Let), MaxLets)
	}
	if len(doc.Sections) == 0 {
		c.issue("sections", "a decoder needs at least one section")
	}
	if len(doc.Sections) > MaxSections {
		c.issue("sections", "%d sections is above the maximum of %d", len(doc.Sections), MaxSections)
	}
	if len(doc.Vectors) > MaxVectors {
		c.issue("vectors", "%d vectors is above the maximum of %d", len(doc.Vectors), MaxVectors)
	}
	if c.fatal() {
		return nil, c.err()
	}

	env, err := baseEnv()
	if err != nil {
		return nil, err
	}
	est := &estimator{letSizes: map[string]checker.SizeEstimate{}}
	letTypes := map[string]*types.Type{}
	var total uint64

	// Lets, in order: each sees only the lets before it.
	for i, l := range doc.Let {
		path := fmt.Sprintf("let[%d]", i)
		name := l.Name
		switch {
		case len(name) == 0 || len(name) > MaxNameBytes || !identifierPattern.MatchString(name):
			c.issue(path+".name", "%q is not a valid name (letters, digits and _, not starting with a digit)", name)
		case isReserved(name):
			c.issue(path+".name", "%q is reserved", name)
		case hasSlot(p.slots, name):
			c.issue(path+".name", "%q is declared twice", name)
		}
		e, a, ok := c.compileExpr(env, est, letTypes, path+".expr", l.Expr, p)
		if !ok {
			continue
		}
		t := a.OutputType()
		if isKind(t, types.ListKind, types.NullTypeKind, types.TypeKind) {
			c.issue(path+".expr", "a let cannot hold a %s", t)
			continue
		}
		slot := firstLetSlot + len(p.lets)
		if hasSlot(p.slots, name) || name == "" {
			continue
		}
		p.slots[name] = slot
		p.lets = append(p.lets, letDef{name: name, slot: slot, expr: e})
		total += e.cost
		letTypes[name] = t
		if t.Kind() == types.BytesKind {
			est.letSizes[name] = checker.SizeEstimate{Min: 0, Max: bytesBound(a.NativeRep().Expr(), est.letSizes)}
		}
		next, err := env.Extend(cel.Variable(name, t))
		if err != nil {
			c.issue(path+".name", "%v", err)
			continue
		}
		env = next
	}

	var worst uint64
	seenSection := map[string]bool{}
	for si, s := range doc.Sections {
		spath := fmt.Sprintf("sections[%d]", si)
		sec := section{name: s.Name, path: spath, ignore: s.Ignore}
		if len(s.Name) > MaxNameBytes {
			c.issue(spath+".name", "the name is longer than %d bytes", MaxNameBytes)
		}
		if s.Name != "" {
			if seenSection[s.Name] {
				c.issue(spath+".name", "the section name %q is used twice", s.Name)
			}
			seenSection[s.Name] = true
		}
		if s.When != "" {
			e, a, ok := c.compileExpr(env, est, letTypes, spath+".when", s.When, p)
			if ok && !a.OutputType().IsExactType(cel.BoolType) {
				c.issue(spath+".when", "a condition must produce a bool, not %s", a.OutputType())
			} else if ok {
				sec.when = e
				total += e.cost
			}
		}
		if s.Ignore && len(s.Outputs) > 0 {
			c.issue(spath+".outputs", "an ignore section stores nothing and must have no outputs")
		}
		if !s.Ignore && len(s.Outputs) == 0 {
			c.issue(spath+".outputs", "a section needs at least one output, or ignore: true")
		}
		var secCost uint64
		readings := 0
		scalarKeys := map[string]bool{}
		for oi, o := range s.Outputs {
			opath := fmt.Sprintf("%s.outputs[%d]", spath, oi)
			r := row{path: opath}
			isMap := o.Map != ""
			switch {
			case isMap && (o.Key != "" || o.Expr != ""):
				c.issue(opath, "a row is either key + expr or a map, not both")
				continue
			case !isMap && (o.Key == "" || o.Expr == ""):
				c.issue(opath, "a row needs a key and an expr, or a map")
				continue
			}
			if o.When != "" {
				e, a, ok := c.compileExpr(env, est, letTypes, opath+".when", o.When, p)
				if ok && !a.OutputType().IsExactType(cel.BoolType) {
					c.issue(opath+".when", "a condition must produce a bool, not %s", a.OutputType())
				} else if ok {
					r.when = e
					secCost += e.cost
				}
			}
			if isMap {
				r.isMap, r.prefix, r.maxKeys = true, o.Prefix, o.MaxKeys
				if o.MaxKeys < 1 || o.MaxKeys > MaxDecoderReadings {
					c.issue(opath+".maxKeys", "maxKeys must be 1 to %d", MaxDecoderReadings)
				}
				if len(o.Prefix) > MaxKeyBytes {
					c.issue(opath+".prefix", "the prefix is longer than %d bytes", MaxKeyBytes)
				}
				if strings.HasPrefix(o.Prefix, ReservedKeyPrefix) {
					c.issue(opath+".prefix", "the %q prefix is reserved for link metrics", ReservedKeyPrefix)
				}
				e, a, ok := c.compileExpr(env, est, letTypes, opath+".map", o.Map, p)
				if ok {
					t := a.OutputType()
					// A JSON sub-object (json.values) is dynamically typed; it must be a map when read.
					if !(t.Kind() == types.DynKind || (t.Kind() == types.MapKind && isKind(t.Parameters()[0], types.StringKind))) {
						c.issue(opath+".map", "a map output must produce a map with string keys, not %s", t)
					} else {
						r.value = e
						secCost += e.cost + uint64(max(o.MaxKeys, 0))
					}
				}
				readings += max(o.MaxKeys, 0)
			} else {
				r.key = o.Key
				c.checkKey(opath+".key", o.Key)
				if scalarKeys[o.Key] {
					c.issue(opath+".key", "the key %q is written twice in this section", o.Key)
				}
				scalarKeys[o.Key] = true
				if metrics != nil {
					if t, ok := metrics.MetricType(o.Key); ok {
						r.typ, r.typed = t, true
					} else {
						c.issue(opath+".key", "%q is not a declared measured metric of this profile (a calculated metric cannot be a decoder output)", o.Key)
					}
				}
				e, a, ok := c.compileExpr(env, est, letTypes, opath+".expr", o.Expr, p)
				if ok {
					if !isKind(a.OutputType(), types.IntKind, types.DoubleKind, types.BoolKind) {
						c.issue(opath+".expr", "an output must produce an int, a double or a bool, not %s%s", a.OutputType(), convHint(a.OutputType()))
					} else {
						r.value = e
						secCost += e.cost
					}
				}
				readings++
			}
			sec.rows = append(sec.rows, r)
		}
		if readings > MaxDecoderReadings {
			c.issue(spath+".outputs", "the section can emit %d readings, above the maximum of %d", readings, MaxDecoderReadings)
		}
		p.maxReadings = max(p.maxReadings, readings)
		worst = max(worst, secCost)
		p.sections = append(p.sections, sec)
	}
	// The composition: every let, every section guard (first-match dispatch can evaluate them
	// all), and the most expensive section's rows.
	p.composedCost = total + worst
	p.estimatedCost = p.composedCost
	if p.usesJSON {
		p.estimatedCost += ParseCost(MaxJSONBytes)
	}
	if p.usesDecoded {
		p.estimatedCost += ParseCost(MaxJSONBytes)
	}

	for i, v := range doc.Vectors {
		vpath := fmt.Sprintf("vectors[%d]", i)
		if v.Name == "" || len(v.Name) > MaxNameBytes {
			c.issue(vpath+".name", "a vector needs a name of 1 to %d bytes", MaxNameBytes)
		}
		if _, err := v.Input.toInput(); err != nil {
			c.issue(vpath+".input", "%v", err)
		}
	}

	if p.composedCost > DecoderCostCeiling && len(c.issues) == 0 {
		ce := &CostError{Estimated: p.composedCost, Ceiling: DecoderCostCeiling}
		c.costs = append(c.costs, ce)
		c.issue("", "%s", ce.Error())
	}
	if len(c.issues) > 0 {
		return nil, c.err()
	}
	p.pool.New = func() any { return &state{vals: make([]any, firstLetSlot+len(p.lets))} }
	return p, nil
}

func (c *compiler) fatal() bool { return len(c.issues) > 0 }

func (c *compiler) err() error {
	return &CompileError{Issues: c.issues, Costs: c.costs}
}

func isReserved(name string) bool {
	if _, ok := reservedNames[name]; ok {
		return true
	}
	_, fn := byName[name]
	return fn
}

func convHint(t *types.Type) string {
	if t.Kind() == types.DynKind {
		return "; wrap it in double(...), int(...) or bool(...)"
	}
	return ""
}

func (c *compiler) checkKey(path, key string) {
	switch {
	case len(key) > MaxKeyBytes:
		c.issue(path, "the key is longer than %d bytes", MaxKeyBytes)
	case strings.HasPrefix(key, ReservedKeyPrefix):
		c.issue(path, "the %q prefix is reserved for link metrics, which the platform appends", ReservedKeyPrefix)
	}
}

// compileExpr parses, type-checks, allowlists, cost-estimates and plans one expression. ok is
// false when an issue was recorded.
func (c *compiler) compileExpr(env *cel.Env, est *estimator, letTypes map[string]*types.Type, path, src string, p *Plan) (*expr, *cel.Ast, bool) {
	if src == "" {
		c.issue(path, "the expression is empty")
		return nil, nil, false
	}
	if len(src) > MaxExpressionBytes {
		c.issue(path, "the expression is %d bytes, above the maximum of %d", len(src), MaxExpressionBytes)
		return nil, nil, false
	}
	a, iss := env.Compile(src)
	if iss != nil && iss.Err() != nil {
		c.issue(path, "%s", strings.TrimSpace(iss.Err().Error()))
		return nil, nil, false
	}
	w := &walker{ast: a.NativeRep(), lets: letTypes}
	if err := w.check(a.NativeRep().Expr()); err != nil {
		c.issue(path, "%v", err)
		return nil, nil, false
	}
	p.usesJSON = p.usesJSON || w.usesJSON
	p.usesDecoded = p.usesDecoded || w.usesDecoded
	ce, err := env.EstimateCost(a, est)
	if err != nil {
		c.issue(path, "estimate cost: %v", err)
		return nil, nil, false
	}
	maxCost := ce.Max + w.dynSelects
	if maxCost > ExprCostCeiling {
		cerr := &CostError{Path: path, Estimated: maxCost, Ceiling: ExprCostCeiling}
		c.costs = append(c.costs, cerr)
		c.issue(path, "estimated cost %d is above the ceiling of %d", maxCost, ExprCostCeiling)
		return nil, nil, false
	}
	prog, err := env.Program(a, cel.CostTracking(actualCost{}), cel.CostLimit(maxCost))
	if err != nil {
		c.issue(path, "build program: %v", err)
		return nil, nil, false
	}
	return &expr{prog: prog, path: path, cost: maxCost}, a, true
}

func hasSlot(m map[string]int, k string) bool { _, ok := m[k]; return ok }
