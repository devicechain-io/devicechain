// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package calcfield

import (
	"fmt"
	"slices"
	"sort"
	"strings"
)

// MaxChainDepth is the longest chain of calculated metrics one profile may publish. A
// formula that reads only measured metrics has depth 1; one that reads a depth-3 calculated
// metric has depth 4, the most there is. Each link is one more formula evaluated per sample, so
// the bound is on per-reading work as much as on legibility: kW -> kVA -> power factor is a
// chain of three.
const MaxChainDepth = 4

// CycleError is a set of calculated metrics that read each other in a ring. Path starts and
// ends at the same key, and each key reads the next (`a -> b -> a`: a reads b, b reads a). A
// metric that reads its own key is the ring `a -> a`.
type CycleError struct{ Path []string }

func (e *CycleError) Error() string {
	return fmt.Sprintf("calculated metrics read each other in a cycle, %s (each reads the next); "+
		"a formula cannot depend on itself, directly or through other calculated metrics", strings.Join(e.Path, " -> "))
}

// DepthError is a calculated metric at the end of a chain longer than MaxChainDepth. Chain
// is the longest such chain, the metric first, each key reading the next.
type DepthError struct {
	Key   string
	Depth int
	Chain []string
}

func (e *DepthError) Error() string {
	return fmt.Sprintf("calculated metric %q ends a chain of %d calculated metrics, %s (each reads the next), "+
		"above the maximum of %d", e.Key, e.Depth, strings.Join(e.Chain, " -> "), MaxChainDepth)
}

// Step is one calculated metric of a Plan.
type Step struct {
	// Key is the metric's key.
	Key string
	// Formula is the compiled formula; nil when the metric cannot run, with Err saying why:
	// it did not compile, it is part of a cycle, or it ends a chain that is too deep.
	Formula *Formula
	// Err is why Formula is nil.
	Err error
	// Depth is the length of the longest chain of calculated metrics ending here, itself
	// included: 1 for a formula that reads only measured metrics. Zero when it never ran
	// through the graph (it did not compile or is in a cycle).
	Depth int

	// feed is the Env position the metric's value is written to for the formulas that read
	// it, or -1 when none does, which is every metric of a profile without chaining.
	feed int
	// deps are the keys of the calculated metrics the formula reads, sorted.
	deps []string
	// carry are the measured Env positions whose presence in a sample makes the step APPLICABLE
	// to it: the measured inputs the formula names plus, transitively, those of every
	// calculated metric it reads. Nil when the step is always applicable: it has no input it
	// requires, or reads no measured metric at all (a constants-only formula).
	carry []int
	// measuredIn is the transitive measured inputs whether or not the step is gated on them,
	// for the steps that read it.
	measuredIn []int
}

// Plan is a profile version's calculated metrics, compiled together: the dependency graph
// between them and the order to evaluate them in.
//
// A calculated metric may read another calculated metric. The Plan declares every calculated
// metric as an input of one Env, after the measured ones, compiles each formula against it,
// and orders the formulas topologically, so a formula runs after everything it reads. Eval
// then writes each value it computes — as its declared type, the exact string the resolved
// event carries — into the sample, where the formulas that read it find it: a downstream
// formula sees the value detection and the live stream see, not a wider intermediate. (The
// measurement store then rounds a DOUBLE to StoredDecimals places, as it rounds any value;
// a chain is not recomputed from the rounded row.)
//
// A metric that produced no value for the sample (a skip) leaves its slot absent, so a
// formula that reads it without a presence guard skips as [SkipMissingInput] and one that
// guards it with has() runs; nothing is ever read as a default.
//
// A Plan is immutable after NewPlan and safe for concurrent use. A profile whose formulas
// read only measured metrics has no edges: every step is evaluated in key order and no value
// is written back, which is the evaluation that predates chaining.
type Plan struct {
	env   *Env
	steps []Step // topological order, ties broken by key
}

// NewPlan compiles the calculated definitions against the measured inputs. measured must be
// valid for NewEnv (unique keys, valid types); definitions with an invalid type, no key, or a
// key an earlier definition already has are not declared (a caller reports a repeated key
// itself), so a formula that names one fails to compile as an undeclared reference. A
// definition whose key is a measured metric's is refused with that reason.
//
// The error is only for measured inputs that cannot be declared. A definition that cannot
// run — it does not compile, it is in a cycle, it ends a chain deeper than MaxChainDepth —
// becomes a Step with a nil Formula and its Err, so a resolver can run the rest and the
// publish gate can report every problem (Problems).
func NewPlan(measured []Input, calculated []Definition) (*Plan, error) {
	inputs := slices.Clone(measured)
	inputs = slices.Grow(inputs, len(calculated))
	isMeasured := make(map[string]bool, len(measured))
	for _, in := range measured {
		isMeasured[in.Key] = true
	}
	declared := make(map[string]bool, len(calculated))
	defs := make([]Definition, 0, len(calculated))
	for _, d := range calculated {
		switch {
		case d.Key == "" || !d.Type.valid() || declared[d.Key]:
			continue
		case !isMeasured[d.Key]:
			inputs = append(inputs, Input{Key: d.Key, Type: d.Type})
		}
		// A key a measured metric has is kept, undeclared, so compiling it reports the collision.
		declared[d.Key] = true
		defs = append(defs, d)
	}
	env, err := newEnv(inputs, len(measured))
	if err != nil {
		return nil, err
	}
	sort.SliceStable(defs, func(a, b int) bool { return defs[a].Key < defs[b].Key })

	steps := make([]Step, len(defs))
	at := make(map[string]int, len(defs))
	for i, d := range defs {
		steps[i] = Step{Key: d.Key, feed: -1}
		if isMeasured[d.Key] {
			_, steps[i].Err = env.Compile(d) // always refused: the key is a measured metric's
			continue
		}
		at[d.Key] = i
		steps[i].Formula, steps[i].Err = env.Compile(d)
		if f := steps[i].Formula; f != nil {
			for _, j := range f.inputs {
				if j >= env.measured {
					steps[i].deps = append(steps[i].deps, env.inputs[j].Key)
				}
			}
			sort.Strings(steps[i].deps)
		}
	}

	p := &Plan{env: env}
	markCycles(steps, at)
	markDepths(steps, at)

	for i := range steps {
		for _, k := range steps[i].deps {
			if j, ok := at[k]; ok && steps[j].Formula != nil {
				steps[j].feed, _ = env.Index(k)
			}
		}
	}
	p.steps = topological(steps, at)
	p.markCarry()
	return p, nil
}

// markCarry fills each step's carry set. p.steps is in dependency order, so a step's
// calculated inputs already have theirs.
func (p *Plan) markCarry() {
	byKey := make(map[string]int, len(p.steps))
	for i := range p.steps {
		byKey[p.steps[i].Key] = i
	}
	for i := range p.steps {
		st := &p.steps[i]
		if st.Formula == nil {
			continue
		}
		var in []int
		for _, j := range st.Formula.inputs {
			if j < p.env.measured {
				in = append(in, j)
			}
		}
		for _, k := range st.deps {
			if d, ok := byKey[k]; ok {
				in = append(in, p.steps[d].measuredIn...)
			}
		}
		slices.Sort(in)
		st.measuredIn = slices.Compact(in)
		if len(st.Formula.required) > 0 {
			st.carry = st.measuredIn
		}
	}
}

// markCycles finds every ring of calculated metrics reading each other and disables each
// metric on one: its Formula is dropped, its Err is the ring, and its edges are removed, so
// what remains is acyclic. Every ring has a back edge in a depth-first walk in key order,
// and the whole stack path of each back edge is disabled, so none survives.
func markCycles(steps []Step, at map[string]int) {
	const (
		white = iota
		grey
		black
	)
	colour := make([]int, len(steps))
	var stack []int
	var cycles []*CycleError
	var visit func(i int)
	visit = func(i int) {
		colour[i] = grey
		stack = append(stack, i)
		for _, k := range steps[i].deps {
			j, ok := at[k]
			if !ok {
				continue
			}
			switch colour[j] {
			case white:
				visit(j)
			case grey:
				from := slices.Index(stack, j)
				path := make([]string, 0, len(stack)-from+1)
				for _, s := range stack[from:] {
					path = append(path, steps[s].Key)
				}
				path = append(path, steps[j].Key)
				cycles = append(cycles, &CycleError{Path: rotateToSmallest(path)})
			}
		}
		stack = stack[:len(stack)-1]
		colour[i] = black
	}
	for i := range steps {
		if colour[i] == white && steps[i].Formula != nil {
			visit(i)
		}
	}
	for _, c := range cycles {
		for _, k := range c.Path[:len(c.Path)-1] {
			s := &steps[at[k]]
			if s.Err == nil {
				s.Err = c
			}
			s.Formula, s.deps = nil, nil
		}
	}
}

// rotateToSmallest renders a closed path starting from its smallest key, so the same ring is
// named the same way whichever metric the walk reached first.
func rotateToSmallest(path []string) []string {
	ring := path[:len(path)-1]
	lo := 0
	for i, k := range ring {
		if k < ring[lo] {
			lo = i
		}
	}
	out := make([]string, 0, len(path))
	out = append(out, ring[lo:]...)
	out = append(out, ring[:lo]...)
	return append(out, out[0])
}

// markDepths sets each runnable step's Depth and disables those deeper than MaxChainDepth,
// with the longest chain that makes them so.
func markDepths(steps []Step, at map[string]int) {
	depth := make([]int, len(steps))
	deepest := make([]int, len(steps)) // the dependency the longest chain runs through, or -1
	var measure func(i int) int
	measure = func(i int) int {
		if depth[i] != 0 {
			return depth[i]
		}
		best, via := 0, -1
		for _, k := range steps[i].deps {
			if j, ok := at[k]; ok && steps[j].Formula != nil {
				if d := measure(j); d > best {
					best, via = d, j
				}
			}
		}
		depth[i], deepest[i] = best+1, via
		return depth[i]
	}
	for i := range steps {
		if steps[i].Formula != nil {
			steps[i].Depth = measure(i)
		}
	}
	for i := range steps {
		if steps[i].Formula == nil || steps[i].Depth <= MaxChainDepth {
			continue
		}
		var chain []string
		for j := i; j >= 0; j = deepest[j] {
			chain = append(chain, steps[j].Key)
		}
		steps[i].Err = &DepthError{Key: steps[i].Key, Depth: steps[i].Depth, Chain: chain}
		// Disabled after every depth is measured, so a deeper metric still names the whole chain.
	}
	for i := range steps {
		if _, tooDeep := steps[i].Err.(*DepthError); tooDeep {
			steps[i].Formula = nil
		}
	}
}

// topological orders steps so every metric follows the ones it reads, and otherwise by key.
// The edges are acyclic (markCycles removed the rings). The order is a function of the
// metrics alone, never of declaration order or map iteration.
func topological(steps []Step, at map[string]int) []Step {
	remaining := make([]int, len(steps)) // unevaluated dependencies of each step
	readers := make([][]int, len(steps))
	for i := range steps {
		for _, k := range steps[i].deps {
			if j, ok := at[k]; ok {
				remaining[i]++
				readers[j] = append(readers[j], i)
			}
		}
	}
	// steps is sorted by key, so the smallest ready index is the smallest ready key.
	out := make([]Step, 0, len(steps))
	done := make([]bool, len(steps))
	for len(out) < len(steps) {
		next := -1
		for i := range steps {
			if !done[i] && remaining[i] == 0 {
				next = i
				break
			}
		}
		if next < 0 {
			panic("calcfield: the calculated metrics' dependency graph still has a cycle") // markCycles removed them all
		}
		done[next] = true
		out = append(out, steps[next])
		for _, r := range readers[next] {
			remaining[r]--
		}
	}
	return out
}

// Applicable reports whether the i'th step is applicable to s: s carries at least one of the
// measured metrics the step depends on, directly or through the calculated metrics it reads
// (a present value that did not parse counts as carried; it is a real signal, and the step
// reports it as invalid_input). A sample that carries NONE of them is not this formula's: it is
// not evaluated and not a skip. A step that requires no input, or reads no measured metric (a
// formula over device constants alone), is applicable to every sample. Allocation-free.
func (p *Plan) Applicable(s *Sample, i int) bool {
	carry := p.steps[i].carry
	if len(carry) == 0 {
		return true
	}
	for _, j := range carry {
		if s.vals[j] != nil || s.bad[j] != SkipNone {
			return true
		}
	}
	return false
}

// Env is the environment every formula of the plan reads its samples from.
func (p *Plan) Env() *Env { return p.env }

// Len is how many calculated metrics the plan evaluates, runnable or not.
func (p *Plan) Len() int { return len(p.steps) }

// Step returns the i'th step in evaluation order.
func (p *Plan) Step(i int) *Step { return &p.steps[i] }

// Chained reports whether any calculated metric reads another, which is the only case in
// which evaluating one writes anything back to the sample.
func (p *Plan) Chained() bool {
	for i := range p.steps {
		if p.steps[i].feed >= 0 {
			return true
		}
	}
	return false
}

// Formulas returns the compiled formulas in evaluation order, for the profile-wide checks
// (CheckProfileCost, CheckProfileConstants). The summed static cost of a chain is the sum of
// its formulas: each runs once per sample, in order, so the sum is the per-sample bound.
func (p *Plan) Formulas() []*Formula {
	out := make([]*Formula, 0, len(p.steps))
	for i := range p.steps {
		if f := p.steps[i].Formula; f != nil {
			out = append(out, f)
		}
	}
	return out
}

// Problems returns why each metric that cannot run cannot, in key order.
func (p *Plan) Problems() []error {
	var out []error
	for _, i := range p.byKey() {
		if s := &p.steps[i]; s.Err != nil {
			out = append(out, s.Err)
		}
	}
	return out
}

// byKey is the step indices in key order.
func (p *Plan) byKey() []int {
	idx := make([]int, len(p.steps))
	for i := range idx {
		idx[i] = i
	}
	sort.Slice(idx, func(a, b int) bool { return p.steps[idx[a]].Key < p.steps[idx[b]].Key })
	return idx
}

// Eval evaluates the i'th step against s, which must have been filled with the measured
// values, with every earlier step already evaluated through Eval (in order, on the same
// sample). It returns what Formula.Eval does and, when another formula reads this metric,
// writes the value into s as the declared type — or clears it when the metric produced none.
// A step that cannot run reports SkipEvalError with the reason.
func (p *Plan) Eval(s *Sample, i int) Result {
	st := &p.steps[i]
	if st.Formula == nil {
		return Result{Skip: SkipEvalError, Err: st.Err}
	}
	r := st.Formula.Eval(s)
	if st.feed >= 0 {
		if r.OK() {
			_ = s.SetAt(st.feed, r.Value) // the string just formatted from this type always parses
		} else {
			s.clearAt(st.feed)
		}
	}
	return r
}

// SingleFormulaPlan wraps one already-compiled formula as a one-step Plan over env, so a caller
// that holds a Formula (a draft being previewed) evaluates it through the same Plan.Eval as a
// whole profile does. The step is not compiled again: it evaluates exactly the Formula it is
// given, has no dependencies, and writes nothing back to the sample.
func SingleFormulaPlan(env *Env, key string, f *Formula) *Plan {
	st := Step{Key: key, Formula: f, Depth: 1, feed: -1}
	if len(f.required) > 0 {
		for _, j := range f.inputs {
			if j < env.measured {
				st.carry = append(st.carry, j)
			}
		}
	}
	return &Plan{env: env, steps: []Step{st}}
}

// StepIndex is the position, in evaluation order, of the calculated metric key, for Eval.
func (p *Plan) StepIndex(key string) (int, bool) {
	for i := range p.steps {
		if p.steps[i].Key == key {
			return i, true
		}
	}
	return 0, false
}

// Closure returns keys together with every calculated metric they read, directly or through
// others, in evaluation order (a metric follows everything it reads) with each key once. It is
// what must be evaluated to compute keys correctly: evaluating the keys alone would feed a
// chained metric an absent input.
//
// It refuses, naming the metric, a key the plan does not have and any metric in the closure
// that cannot run (it does not compile, is in a cycle, or ends a chain deeper than
// MaxChainDepth): a closure with a broken member would compute a plausible-looking value from a
// missing input. Cycles cannot reach the walk (NewPlan removes every ring's edges); the visited
// set bounds it regardless.
func (p *Plan) Closure(keys []string) ([]string, error) {
	need := make(map[string]struct{}, len(keys))
	var visit func(key, reader string) error
	visit = func(key, reader string) error {
		if _, ok := need[key]; ok {
			return nil
		}
		i, ok := p.StepIndex(key)
		if !ok {
			if reader == "" {
				return fmt.Errorf("calculated metric %q is not in the plan", key)
			}
			return fmt.Errorf("calculated metric %q reads %q, which is not in the plan", reader, key)
		}
		st := &p.steps[i]
		if st.Formula == nil {
			if reader != "" {
				return fmt.Errorf("calculated metric %q reads %q, which cannot run: %w", reader, key, st.Err)
			}
			return fmt.Errorf("calculated metric %q cannot run: %w", key, st.Err)
		}
		need[key] = struct{}{}
		for _, d := range st.deps {
			if err := visit(d, key); err != nil {
				return err
			}
		}
		return nil
	}
	for _, k := range keys {
		if err := visit(k, ""); err != nil {
			return nil, err
		}
	}
	out := make([]string, 0, len(need))
	for i := range p.steps {
		if _, ok := need[p.steps[i].Key]; ok {
			out = append(out, p.steps[i].Key)
		}
	}
	return out, nil
}
