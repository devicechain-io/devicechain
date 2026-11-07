// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"sort"
	"strings"
)

// graph is the merged triples of every file a pack is built from, indexed by
// IRI subject. Blank nodes are parsed (so the reader stays honest about the
// grammar) but never queried: nothing a pack records hangs off one.
type graph struct {
	by map[string][]Triple
}

func newGraph() *graph { return &graph{by: map[string][]Triple{}} }

func (g *graph) add(ts []Triple) {
	for _, t := range ts {
		if t.S.Kind == nodeIRI {
			g.by[t.S.Value] = append(g.by[t.S.Value], t)
		}
	}
}

// objs returns the objects of (s, p) in document order.
func (g *graph) objs(s, p string) []Node {
	var out []Node
	for _, t := range g.by[s] {
		if t.P.Value == p {
			out = append(out, t.O)
		}
	}
	return out
}

// iris returns the distinct IRI objects of (s, p), sorted.
func (g *graph) iris(s, p string) []string {
	set := map[string]struct{}{}
	for _, o := range g.objs(s, p) {
		if o.Kind == nodeIRI {
			set[o.Value] = struct{}{}
		}
	}
	return sortedKeys(set)
}

func (g *graph) types(s string) map[string]bool {
	out := map[string]bool{}
	for _, o := range g.objs(s, rdfNS+"type") {
		if o.Kind == nodeIRI {
			out[o.Value] = true
		}
	}
	return out
}

// subClassOfStar reports whether s is ancestor or reaches it through
// rdfs:subClassOf. It terminates on cyclic data.
func (g *graph) subClassOfStar(s, ancestor string) bool {
	seen := map[string]bool{s: true}
	queue := []string{s}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		if cur == ancestor {
			return true
		}
		for _, p := range g.iris(cur, rdfsNS+"subClassOf") {
			if !seen[p] {
				seen[p] = true
				queue = append(queue, p)
			}
		}
	}
	return false
}

// subjectsIn lists the IRI subjects inside ns, sorted.
func (g *graph) subjectsIn(ns string) []string {
	var out []string
	for s := range g.by {
		if strings.HasPrefix(s, ns) && len(s) > len(ns) {
			out = append(out, s)
		}
	}
	sort.Strings(out)
	return out
}

// label picks a display label for s from the given predicates, in order:
// English first, then untagged, then the lowest language tag; ties by value.
func (g *graph) label(s string, preds ...string) string {
	for _, p := range preds {
		var best *Node
		rank := func(n Node) int {
			switch n.Lang {
			case "en":
				return 0
			case "":
				return 1
			}
			return 2
		}
		os := g.objs(s, p)
		for i := range os {
			n := os[i]
			if n.Kind != nodeLiteral || strings.TrimSpace(n.Value) == "" {
				continue
			}
			if best == nil || rank(n) < rank(*best) ||
				(rank(n) == rank(*best) && (n.Lang < best.Lang || (n.Lang == best.Lang && n.Value < best.Value))) {
				best = &os[i]
			}
		}
		if best != nil {
			return strings.TrimSpace(best.Value)
		}
	}
	return ""
}

func (g *graph) flag(s, p string) bool {
	for _, o := range g.objs(s, p) {
		if o.Kind == nodeLiteral && o.Value == "true" {
			return true
		}
	}
	return false
}

func sortedKeys(m map[string]struct{}) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
