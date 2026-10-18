// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package calcfield

import (
	"errors"
	"fmt"
	"math"
	"math/rand"
	"sort"
	"strings"
	"sync"
	"testing"
)

// A property check of NewPlan and Plan.Eval against an independent reference: random profiles
// of up to 32 calculated metrics, some acyclic and some with rings, each compared with a
// reachability closure (what is a cycle), a longest-path recursion (depth), a smallest-key
// Kahn order (evaluation order) and a direct evaluation of the formulas (values and skips).

type refNode struct {
	key      string
	typ      Type
	deps     []int  // calculated metrics read, in expression order
	guarded  []bool // per dep: read through has()
	measured string // the measured metric read, unguarded
}

type refProfile struct{ nodes []refNode }

func (rp refProfile) expr(i int) string {
	n := rp.nodes[i]
	terms := make([]string, 0, len(n.deps)+2)
	for d, j := range n.deps {
		read := rp.nodes[j].key
		if rp.nodes[j].typ == Integer {
			read = "double(" + read + ")"
		}
		if n.guarded[d] {
			read = fmt.Sprintf("(has(m.%s) ? %s : 7.0)", rp.nodes[j].key, read)
		}
		terms = append(terms, read)
	}
	terms = append(terms, n.measured, "1.0")
	sum := strings.Join(terms, " + ")
	if n.typ == Integer {
		return "int(" + sum + ")"
	}
	return sum
}

func (rp refProfile) defs() []Definition {
	out := make([]Definition, len(rp.nodes))
	for i := range rp.nodes {
		out[i] = Definition{Key: rp.nodes[i].key, Expression: rp.expr(i), Type: rp.nodes[i].typ}
	}
	return out
}

func randomProfile(rng *rand.Rand, acyclic bool) refProfile {
	n := 1 + rng.Intn(32)
	used := map[string]bool{}
	nodes := make([]refNode, n)
	for i := range nodes {
		var k string
		for k == "" || used[k] {
			b := []byte{'x'}
			for l := 1 + rng.Intn(3); l > 0; l-- {
				b = append(b, byte('a'+rng.Intn(26)))
			}
			k = string(b)
		}
		used[k] = true
		nodes[i].key = k
		nodes[i].typ = Double
		if rng.Intn(4) == 0 {
			nodes[i].typ = Integer
		}
		nodes[i].measured = planMeasured[rng.Intn(3)].Key // the three DOUBLE ones
	}
	// rank: an acyclic profile only reads metrics of a lower rank, which is not key order.
	rank := rng.Perm(n)
	for i := range nodes {
		want := rng.Intn(4)
		for tries := 0; tries < 8 && len(nodes[i].deps) < want; tries++ {
			j := rng.Intn(n)
			if acyclic && rank[j] >= rank[i] {
				continue
			}
			if !acyclic && j == i && rng.Intn(4) != 0 {
				continue
			}
			dup := false
			for _, d := range nodes[i].deps {
				dup = dup || d == j
			}
			if dup {
				continue
			}
			nodes[i].deps = append(nodes[i].deps, j)
			nodes[i].guarded = append(nodes[i].guarded, rng.Intn(3) == 0)
		}
	}
	return refProfile{nodes: nodes}
}

// reach[i][j]: j is reachable from i by one or more reads.
func (rp refProfile) reach() [][]bool {
	n := len(rp.nodes)
	r := make([][]bool, n)
	for i := range r {
		r[i] = make([]bool, n)
		for _, j := range rp.nodes[i].deps {
			r[i][j] = true
		}
	}
	for k := 0; k < n; k++ {
		for i := 0; i < n; i++ {
			if r[i][k] {
				for j := 0; j < n; j++ {
					if r[k][j] {
						r[i][j] = true
					}
				}
			}
		}
	}
	return r
}

func (rp refProfile) index() map[string]int {
	m := map[string]int{}
	for i, n := range rp.nodes {
		m[n.key] = i
	}
	return m
}

func TestPlanPropertyAgainstAReference(t *testing.T) {
	rng := rand.New(rand.NewSource(20261018))
	iters := 1500
	if testing.Short() {
		iters = 200
	}
	var acyclicSeen, cyclicSeen, deepSeen, guardSeen int
	for it := 0; it < iters; it++ {
		rp := randomProfile(rng, it%2 == 0)
		defs := rp.defs()
		p, err := NewPlan(planMeasured, defs)
		if err != nil {
			t.Fatalf("iter %d: %v", it, err)
		}
		idx := rp.index()
		reach := rp.reach()
		cyclic := make([]bool, len(rp.nodes))
		anyCycle := false
		for i := range cyclic {
			cyclic[i] = reach[i][i]
			anyCycle = anyCycle || cyclic[i]
		}

		// Determinism: the same metrics, declared in any order, give the same plan.
		shuffled := append([]Definition(nil), defs...)
		rng.Shuffle(len(shuffled), func(a, b int) { shuffled[a], shuffled[b] = shuffled[b], shuffled[a] })
		q, err := NewPlan(planMeasured, shuffled)
		if err != nil {
			t.Fatal(err)
		}
		if a, b := planKeys(p), planKeys(q); strings.Join(a, ",") != strings.Join(b, ",") {
			t.Fatalf("iter %d: order depends on declaration order: %v vs %v", it, a, b)
		}
		if a, b := fmt.Sprint(p.Problems()), fmt.Sprint(q.Problems()); a != b {
			t.Fatalf("iter %d: problems depend on declaration order:\n%s\n%s", it, a, b)
		}

		// No compile errors: every formula here is within the language and the ceiling.
		disabled := make([]bool, len(rp.nodes))
		pos := make([]int, len(rp.nodes))
		for s := 0; s < p.Len(); s++ {
			st := p.Step(s)
			i := idx[st.Key]
			pos[i] = s
			var ce *CompileError
			if errors.As(st.Err, &ce) {
				t.Fatalf("iter %d: %s does not compile: %v (%s)", it, st.Key, st.Err, rp.expr(i))
			}
			disabled[i] = st.Formula == nil
			if (st.Formula == nil) != (st.Err != nil) {
				t.Fatalf("iter %d: %s: Formula and Err disagree", it, st.Key)
			}
		}
		// Order: every runnable metric follows every metric it reads.
		for i, n := range rp.nodes {
			if disabled[i] {
				continue
			}
			for _, j := range n.deps {
				if !disabled[j] && pos[j] >= pos[i] {
					t.Fatalf("iter %d: %s runs at %d before %s at %d which it reads", it, n.key, pos[i], rp.nodes[j].key, pos[j])
				}
			}
		}

		if anyCycle {
			cyclicSeen++
			if len(p.Problems()) == 0 {
				t.Fatalf("iter %d: a profile with a cycle has no problem", it)
			}
			for s := 0; s < p.Len(); s++ {
				st := p.Step(s)
				var cyc *CycleError
				if !errors.As(st.Err, &cyc) {
					continue
				}
				i := idx[st.Key]
				if !cyclic[i] {
					t.Fatalf("iter %d: %s is disabled as a cycle but is on none", it, st.Key)
				}
				// The named ring is a real ring: each key reads the next.
				for k := 0; k+1 < len(cyc.Path); k++ {
					a, b := idx[cyc.Path[k]], idx[cyc.Path[k+1]]
					reads := false
					for _, d := range rp.nodes[a].deps {
						reads = reads || d == b
					}
					if !reads {
						t.Fatalf("iter %d: ring %v: %s does not read %s", it, cyc.Path, cyc.Path[k], cyc.Path[k+1])
					}
				}
				if cyc.Path[0] != cyc.Path[len(cyc.Path)-1] {
					t.Fatalf("iter %d: ring %v is not closed", it, cyc.Path)
				}
			}
			// What still runs is acyclic: no runnable metric reaches itself through runnable ones.
			for i := range rp.nodes {
				if disabled[i] {
					continue
				}
				seen := map[int]bool{}
				var walk func(k int) bool
				walk = func(k int) bool {
					for _, j := range rp.nodes[k].deps {
						if disabled[j] {
							continue
						}
						if j == i {
							return true
						}
						if !seen[j] {
							seen[j] = true
							if walk(j) {
								return true
							}
						}
					}
					return false
				}
				if walk(i) {
					t.Fatalf("iter %d: %s still runs on a ring", it, rp.nodes[i].key)
				}
			}
			evalNoPanic(t, p)
			continue
		}

		acyclicSeen++
		// Depth: the longest chain, itself included.
		depth := make([]int, len(rp.nodes))
		var measure func(i int) int
		measure = func(i int) int {
			if depth[i] == 0 {
				best := 0
				for _, j := range rp.nodes[i].deps {
					best = max(best, measure(j))
				}
				depth[i] = best + 1
			}
			return depth[i]
		}
		for i := range rp.nodes {
			measure(i)
			st := p.Step(pos[i])
			if st.Depth != depth[i] {
				t.Fatalf("iter %d: %s depth %d, reference %d", it, st.Key, st.Depth, depth[i])
			}
			var de *DepthError
			tooDeep := errors.As(st.Err, &de)
			if tooDeep != (depth[i] > MaxChainDepth) {
				t.Fatalf("iter %d: %s depth %d, DepthError %v", it, st.Key, depth[i], st.Err)
			}
			if tooDeep {
				deepSeen++
				if len(de.Chain) != depth[i] || de.Chain[0] != st.Key {
					t.Fatalf("iter %d: %s chain %v for depth %d", it, st.Key, de.Chain, depth[i])
				}
			}
			if st.Err != nil && !tooDeep {
				t.Fatalf("iter %d: %s: unexpected %v", it, st.Key, st.Err)
			}
		}
		// Order: Kahn, smallest ready key first.
		var want []string
		done := make([]bool, len(rp.nodes))
		for len(want) < len(rp.nodes) {
			best := -1
			for i, n := range rp.nodes {
				if done[i] {
					continue
				}
				ready := true
				for _, j := range n.deps {
					ready = ready && done[j]
				}
				if ready && (best < 0 || n.key < rp.nodes[best].key) {
					best = i
				}
			}
			done[best] = true
			want = append(want, rp.nodes[best].key)
		}
		if got := planKeys(p); strings.Join(got, ",") != strings.Join(want, ",") {
			t.Fatalf("iter %d: order %v, reference %v", it, got, want)
		}
		// Feeds: exactly the runnable metrics some metric reads.
		read := make([]bool, len(rp.nodes))
		for _, n := range rp.nodes {
			for _, j := range n.deps {
				read[j] = true
			}
		}
		for i := range rp.nodes {
			if got, wantFeed := p.Step(pos[i]).feed >= 0, read[i] && !disabled[i]; got != wantFeed {
				t.Fatalf("iter %d: %s feed %v, want %v", it, rp.nodes[i].key, got, wantFeed)
			}
		}

		// Values, over several samples on ONE reused sample (Reset between) and on pooled ones.
		reused := p.Env().Sample()
		for k := 0; k < 4; k++ {
			vals := map[string]float64{}
			for _, in := range planMeasured[:3] {
				if rng.Intn(5) != 0 {
					vals[in.Key] = float64(rng.Intn(2001)-1000) / 8
				}
			}
			refVal := map[int]float64{}
			refHas := map[int]bool{}
			var refEval func(i int)
			refEval = func(i int) {
				if _, ok := refHas[i]; ok {
					return
				}
				refHas[i] = false
				if disabled[i] {
					return
				}
				n := rp.nodes[i]
				sum := 0.0
				for d, j := range n.deps {
					refEval(j)
					switch {
					case refHas[j]:
						sum += refVal[j]
					case n.guarded[d]:
						sum += 7.0
					default:
						return
					}
				}
				mv, ok := vals[n.measured]
				if !ok {
					return
				}
				sum = sum + mv + 1.0
				if n.typ == Integer {
					sum = math.Trunc(sum)
				}
				refVal[i], refHas[i] = sum, true
				if n.guarded != nil {
					for _, g := range n.guarded {
						if g {
							guardSeen++
						}
					}
				}
			}
			for _, s := range []*Sample{reused, p.Env().Sample()} {
				for key, v := range vals {
					if _, err := s.Set(key, FormatDouble(v)); err != nil {
						t.Fatal(err)
					}
				}
				for step := 0; step < p.Len(); step++ {
					i := idx[p.Step(step).Key]
					refEval(i)
					r := p.Eval(s, step)
					switch {
					case disabled[i]:
						if r.Skip != SkipEvalError {
							t.Fatalf("iter %d: disabled %s gave %+v", it, rp.nodes[i].key, r)
						}
					case refHas[i]:
						want := FormatDouble(refVal[i])
						if rp.nodes[i].typ == Integer {
							want = FormatInt(int64(refVal[i]))
						}
						if r.Value != want {
							t.Fatalf("iter %d: %s = %q (%v), reference %q; %s", it, rp.nodes[i].key, r.Value, r.Skip, want, rp.expr(i))
						}
					default:
						if r.Skip != SkipMissingInput {
							t.Fatalf("iter %d: %s = %+v, reference missing_input", it, rp.nodes[i].key, r)
						}
					}
				}
				if s == reused {
					s.Reset()
				} else {
					s.Release()
				}
			}
		}
		reused.Release()
	}
	t.Logf("acyclic %d, cyclic %d, too-deep steps %d, guarded reads %d", acyclicSeen, cyclicSeen, deepSeen, guardSeen)
	if acyclicSeen == 0 || cyclicSeen == 0 || deepSeen == 0 || guardSeen == 0 {
		t.Fatal("the generator did not cover every case")
	}
}

func evalNoPanic(t *testing.T, p *Plan) {
	t.Helper()
	s := p.Env().Sample()
	defer s.Release()
	for _, in := range planMeasured[:3] {
		_, _ = s.Set(in.Key, "1")
	}
	for i := 0; i < p.Len(); i++ {
		r := p.Eval(s, i)
		if p.Step(i).Formula == nil && r.Skip != SkipEvalError {
			t.Fatalf("disabled %s gave %+v", p.Step(i).Key, r)
		}
	}
}

// Many goroutines evaluating one plan, each on its own pooled sample, never see another's
// values (run with -race).
func TestPlanConcurrentEvaluationIsIsolated(t *testing.T) {
	p, err := NewPlan(planMeasured, append(kwChain(), def("code", "int(pf * 100.0)", Integer)))
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	errs := make(chan string, 64)
	for g := 0; g < 16; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for n := 0; n < 500; n++ {
				s := p.Env().Sample()
				v := float64(100 + g)
				_, _ = s.Set("voltage", FormatDouble(v))
				_, _ = s.Set("current", "10")
				if n%3 != 0 {
					_, _ = s.Set("pf_in", "0.5")
				}
				got := map[string]Result{}
				for i := 0; i < p.Len(); i++ {
					got[p.Step(i).Key] = p.Eval(s, i)
				}
				s.Release()
				if want := FormatDouble(v * 10 / 1000.0); got["kva"].Value != want {
					errs <- fmt.Sprintf("g%d: kva %q want %q", g, got["kva"].Value, want)
					return
				}
				if n%3 == 0 && got["kw"].Skip != SkipMissingInput {
					errs <- fmt.Sprintf("g%d: kw leaked %+v", g, got["kw"])
					return
				}
			}
		}(g)
	}
	wg.Wait()
	close(errs)
	var all []string
	for e := range errs {
		all = append(all, e)
	}
	sort.Strings(all)
	if len(all) > 0 {
		t.Fatal(strings.Join(all, "\n"))
	}
}

// The bound on an Env's inputs is on the MEASURED metrics, as it was before a plan also
// declared the calculated ones: a profile with the most measured metrics a formula may read
// still compiles its calculated metrics, chained or not.
func TestPlanDeclaresCalculatedMetricsBeyondTheMeasuredBound(t *testing.T) {
	measured := make([]Input, MaxInputs)
	for i := range measured {
		measured[i] = Input{Key: fmt.Sprintf("m%03d", i), Type: Double}
	}
	defs := []Definition{def("sum", "m000 + m255", Double), def("twice", "sum * 2.0", Double)}
	p, err := NewPlan(measured, defs)
	if err != nil {
		t.Fatalf("a profile of %d measured metrics cannot be planned: %v", MaxInputs, err)
	}
	if probs := p.Problems(); len(probs) != 0 {
		t.Fatal(probs)
	}
	if _, err := NewPlan(append(measured, Input{Key: "over", Type: Double}), defs); !errors.Is(err, ErrInvalidInputs) {
		t.Fatalf("one measured metric over the bound: %v", err)
	}
}

// A ring the walk enters away from its smallest key is still named from its smallest key.
func TestPlanNamesARingFromItsSmallestKey(t *testing.T) {
	p, err := NewPlan(planMeasured, []Definition{
		def("a", "z + voltage", Double), def("z", "y + voltage", Double), def("y", "z + voltage", Double)})
	if err != nil {
		t.Fatal(err)
	}
	var ce *CycleError
	for _, e := range p.Problems() {
		if errors.As(e, &ce) {
			break
		}
	}
	if ce == nil || strings.Join(ce.Path, " -> ") != "y -> z -> y" {
		t.Fatalf("ring %v, want y -> z -> y (problems %v)", ce, p.Problems())
	}
}
