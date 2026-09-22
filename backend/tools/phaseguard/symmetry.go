// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package phaseguard

import (
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
	"slices"
	"sort"
	"strings"

	"golang.org/x/tools/go/packages"
)

// lifecycleManagerType is the core type whose verbs mark a component as handled. It is
// named here rather than in each Symmetry because there is only one of it: every
// component in this tree drives its lifecycle through a `lifecycle core.LifecycleManager`
// FIELD and a delegating method, never by embedding, so the verb call always sits inside
// a method whose receiver names the component.
const lifecycleManagerType = "LifecycleManager"

// Symmetry is a COMPLETENESS constraint over one service's own wiring, and it asks a
// different question from a Rule.
//
// A Rule asks "is this symbol reachable from a phase it is not allowed to run in" — a
// question about one named thing, answered across the whole program at once. This asks
// "is the set of components this service STARTS the same as the set it STOPS" — a
// question with no symbol list in it, whose answer is a set difference, and which is
// meaningless outside the boundary of one service's main package.
//
// 🔴 THE PLAN THIS IMPLEMENTS SAID G9 WAS "ONE MORE RULE PLUS A DIRECTIONAL CHECK". IT
// IS NOT, AND THE DIFFERENCE IS THE WHOLE POINT OF THE GAP. Every existing rule is
// satisfied by a tree in which a service starts seven components and stops six: each
// individual Stop it does make is in the right phase, no watched symbol appears where it
// must not, and the count of stops on the stop path is comfortably above any floor. What
// is wrong is the component that is ABSENT, and nothing keyed on the presence of a
// symbol can see an absence. That is why it is a set difference and why it has to be
// scoped per service — the missing stop in one service is trivially supplied by a
// different service stopping a component of the same type, and a whole-program count
// would report clean.
type Symmetry struct {
	Name string
	// From is the phase whose handled components must all be matched; To is the phase
	// that must match them.
	From, To Phase
	// FromVerb and ToVerb are the LifecycleManager methods that mark a component as
	// handled in each phase.
	FromVerb, ToVerb string
	// MinServices is the liveness floor on how many services were read with BOTH
	// callbacks discovered. A service whose callbacks stopped being recognised drops
	// silently out of the set difference and takes its components with it.
	MinServices int
	// MinPairs is the liveness floor on components matched on the To side. It is the
	// positive control: the set difference is empty both when every component is
	// stopped and when the walk found no stops to compare against.
	MinPairs int
	Why      string
	Remedy   string
}

// Symmetries are the completeness constraints this guard enforces.
var Symmetries = []Symmetry{
	{
		Name:     "start-stop-symmetry",
		From:     Start,
		To:       Stop,
		FromVerb: "Start",
		ToVerb:   "Stop",
		// 🔴 A FLOOR WITH ROOM UNDER IT, NOT TODAY'S COUNT. Fifteen packages are
		// compared as this lands and they wire dozens of stopped components between them.
		//
		// Fifteen, not fourteen: the fourteen service mains plus backend/core/main.go,
		// a demo microservice whose four callbacks only log. It wires no components, so
		// it contributes nothing to either side and can never produce a finding — but
		// it IS compared, and the number this guard prints has to be the number it
		// actually means or the next person reconciles it against the service count and
		// concludes one is missing.
		//
		// Ten and twenty-five are low enough that deleting a service, or folding two
		// components into one, never reaches them, and high enough that neither door
		// going blind on its own can clear them: the callbacks are found through the
		// LifecycleCallbacks literal and the components through the call graph out of
		// it, so a failure in either drives both numbers toward zero.
		MinServices: 10,
		MinPairs:    25,
		Why: "a component started on the start path and never stopped on the stop " +
			"path keeps its goroutines, its subscriptions and its database handles " +
			"across a stop, and the lifecycle state machine then refuses its next " +
			"start because it never left Started",
		Remedy: "stop it in the Stopper callback, in the reverse of the order it was " +
			"started in unless there is a reason to differ — and write that reason down",
	},
}

// 🔴 WHY THERE IS NO initialize/terminate SYMMETRY HERE, WHICH IS A DELIBERATE OMISSION
// AND NOT AN OVERSIGHT.
//
// The obvious second constraint — everything Initialized must be Terminated — is WRONG
// in this tree, and measuring it is what showed that. Seven services build their
// consumers inside createNatsComponents, which is not initialization at all: it is the
// callback handed to messaging.NewNatsManager, which the NATS manager invokes on EVERY
// start. Those components are Initialized per start by design and are never Terminated,
// so the constraint would open with at least six findings, all of them correct code.
//
// A guard that ships with exemptions for its own first findings is a guard whose
// exemption list is the real rule, and this repository has already learned that an
// exemption is the one claim nothing reports on. So the axis that is clean is the one
// that is enforced, and the other is left to G6 — the state machine's inability to
// express an initialize-only component — which is where it actually belongs.

// blindSpots is what this constraint CANNOT see, written down because an undeclared
// limitation is the one claim nothing reports on — and because two of these are real
// components in the tree today, not hypotheticals.
//
//  1. A component whose lifecycle verbs do not take a context. sparkplug-ingest's source
//     Manager is `Start()` / `Stop()` with no arguments, driven from the leadership
//     goroutine. It is started and stopped symmetrically today and nothing here watches
//     it. Widening the signature test to catch it would also catch time.Ticker.Stop and
//     every other Start/Stop in the tree, so the signature stays and this is stated.
//  2. A component reached only through an interface method that is NOT itself a
//     lifecycle verb. The walk does not expand interface callees at all — see the
//     reasoning at the edge step — so a service that starts its components by calling
//     some Hook.Kick(ctx) is invisible. Nothing does this. The direction of the loss is
//     the safe one: it can produce a finding to look at, never hide one.
//  3. Several components held in ONE interface-typed collection. event-sources'
//     `EventSources []core.LifecycleComponent` is one identity, so a second collection
//     beside it would fold into the same one. Counted and printed as ViaInterface.
//  4. A component whose start and stop name DIFFERENT package-level variables — an
//     alias, or a reassignment between the two callbacks. This produces a FINDING rather
//     than silence, which is the right direction but is a false positive to recognise.
//  5. The HTTP servers the ingest services and mcp start directly (`httpServer.Start()`),
//     the leadership goroutines, and event-sources' presence tap: none is driven by a
//     lifecycle verb on a package-level variable, so none is watched.
//
// What is NOT on this list, because it was fixed rather than declared: a component that
// owns no core.LifecycleManager. Two of those exist — outbound-connectors' DispatchConsumer
// and event-processing's ReactDispatcher — and an earlier design could not see either.

// SymFinding is one component handled in From's phase and not in To's.
type SymFinding struct {
	Pos       token.Position
	Symmetry  string
	Service   string // the package path of the service whose wiring this is
	Component string
	// In is the function the wiring call sits in — the callback itself, or a helper it
	// reaches.
	In string
	// Handled and Missing are the two verbs, so the message states the constraint that
	// was actually violated rather than a hardcoded pair. A second Symmetry would
	// otherwise print the first one's prose.
	Handled string
	Missing string
	Why     string
}

func (f SymFinding) String() string {
	return fmt.Sprintf("%s:%d:%d: %s: %s calls %s on %s and never calls %s\n    wired in %s",
		f.Pos.Filename, f.Pos.Line, f.Pos.Column, f.Symmetry,
		shortPkg(f.Service), f.Handled, f.Component, f.Missing, f.In)
}

func shortPkg(p string) string {
	return p[strings.LastIndex(p, "/")+1:]
}

// handledAt is where a component was wired: the position of the `X.Verb(ctx)` in the
// service's own source, and the function that call sits in.
type handledAt struct {
	pos   token.Position
	where string
}

// SymResult is what one symmetry constraint saw, INCLUDING how much it saw.
type SymResult struct {
	// Services counts packages where BOTH phases' callbacks were discovered. A
	// package with only one of them is not analysed at all — a set difference against
	// a side that was never read reports every component as missing — and is counted
	// in Partial instead.
	Services int
	Partial  []string
	// Pairs counts components matched on the To side across all services. This is the
	// positive control.
	Pairs int
	// Matched is the evidence behind Pairs, for -show-expected.
	Matched []string
	// ViaInterface counts components identified by an INTERFACE rather than by the
	// variable they are wired into. It is printed on every run, not just under
	// -show-expected, because it is the size of a declared blind spot: several
	// components held in one interface-typed collection are ONE identity, so a service
	// that starts two and stops one of them reports clean. A limitation nothing reports
	// is indistinguishable from a limitation that does not exist.
	ViaInterface int
	Findings     []SymFinding
	// Anonymous names verb references the identity model could not key on because the
	// function holding them has no receiver. It is an INSTRUMENT failure, not a
	// finding: every such site is a component this guard silently stopped tracking.
	Anonymous []string
}

// symmetryFindings answers one Symmetry across every service in the loaded program.
func (s *scanner) symmetryFindings(sym Symmetry) SymResult {
	res := SymResult{}

	// Deterministic service order, so a failure prints the same way twice.
	pkgs := make([]*packages.Package, 0, len(s.cb))
	for p := range s.cb {
		pkgs = append(pkgs, p)
	}
	sort.Slice(pkgs, func(i, j int) bool { return pkgs[i].PkgPath < pkgs[j].PkgPath })

	for _, p := range pkgs {
		// 🔴 ONLY A SERVICE'S OWN main PACKAGE, AND THE SCOPE IS PART OF THE QUESTION.
		// This asks whether a SERVICE stops what it starts, and a service is the thing
		// that builds a LifecycleCallbacks and hands it to core.NewMicroservice — which
		// happens in package main, once per service. A LifecycleCallbacks literal
		// anywhere else is a library helper or a harness building one side of the pair
		// on purpose, and treating a package that declares only a Starter as a service
		// that forgot its Stopper would make every such helper an instrument failure.
		//
		// The filter cannot quietly swallow a real service: the count of packages it
		// did compare is a floor, so services disappearing from the set fails the run.
		if p.Name != "main" {
			continue
		}
		from, hasFrom := s.cb[p][sym.From]
		to, hasTo := s.cb[p][sym.To]
		if !hasFrom && !hasTo {
			continue
		}
		if !hasFrom || !hasTo || len(from) == 0 || len(to) == 0 {
			// 🔴 NOT SILENTLY SKIPPED. One side missing means the comparison cannot be
			// made, and a set difference against an unread side would report every
			// component in the service. Say so and let the floor decide.
			res.Partial = append(res.Partial, fmt.Sprintf(
				"%s: %d %s and %d %s callback entry point(s)",
				p.PkgPath, len(from), sym.From, len(to), sym.To))
			continue
		}
		res.Services++

		started, anonStart := s.handledIn(p, sym.From, sym.FromVerb)
		stopped, anonStop := s.handledIn(p, sym.To, sym.ToVerb)
		res.Anonymous = append(res.Anonymous, anonStart...)
		res.Anonymous = append(res.Anonymous, anonStop...)

		for id := range stopped {
			res.Pairs++
			if id.tn != nil {
				res.ViaInterface++
			}
			res.Matched = append(res.Matched, fmt.Sprintf("%s stops %s",
				shortPkg(p.PkgPath), id))
		}

		for id, at := range started {
			if _, ok := stopped[id]; ok {
				continue
			}
			res.Findings = append(res.Findings, SymFinding{
				Pos:       at.pos,
				Symmetry:  sym.Name,
				Service:   p.PkgPath,
				Component: id.String(),
				In:        at.where,
				Handled:   sym.FromVerb,
				Missing:   sym.ToVerb,
				Why:       sym.Why,
			})
		}
	}

	sort.Strings(res.Matched)
	sort.Strings(res.Partial)
	// One unnameable site is reached once per phase walked, so it arrives twice. Report
	// it once: a duplicated instrument error reads like two defects.
	sort.Strings(res.Anonymous)
	res.Anonymous = slices.Compact(res.Anonymous)
	sort.Slice(res.Findings, func(i, j int) bool {
		a, b := res.Findings[i], res.Findings[j]
		if a.Service != b.Service {
			return a.Service < b.Service
		}
		return a.Component < b.Component
	})
	return res
}

// typeNameLabel renders a named type as pkg.Name, with the import path cut to its last
// element — enough to tell processor.StateProcessor from processor.NotificationProcessor
// without printing a module path on every line.
func typeNameLabel(tn *types.TypeName) string {
	pkg := ""
	if tn.Pkg() != nil {
		pkg = shortPkg(tn.Pkg().Path()) + "."
	}
	return pkg + tn.Name()
}

// typeLabel renders a variable's type the same way, through the pointer a component is
// almost always held behind.
func typeLabel(t types.Type) string {
	if named, ok := deref(t).(*types.Named); ok {
		return typeNameLabel(named.Obj())
	}
	return t.String()
}

// handledIn walks the call graph out of ONE service's callback entry points for a phase
// and reports every component the service handles on that path.
//
// 🔴 A COMPONENT IS RECOGNISED AT THE CALL SITE, AND THE EARLIER DESIGN THAT LOOKED FOR
// core.LifecycleManager WAS BLIND TO THE COMPONENTS THAT MATTER MOST.
//
// The first version watched `(core.LifecycleManager).Stop` and identified the component
// by walking back up to the variable that reached it. That models the framework, and the
// framework is not what a service wires. Two components in this tree — outbound-connectors'
// DispatchConsumer and event-processing's ReactDispatcher — hold a worker pool, a reader
// goroutine and a WaitGroup, expose Start and Stop with the lifecycle signature, and own
// no LifecycleManager at all. Deleting either one's Stop from its service left the guard
// reporting every matched component and exit 0. They are precisely the goroutine-holding
// components this constraint's own rationale is written about, and the model could not
// see them.
//
// So the question asked here is the one a reader of main.go asks: the service named a
// variable and called a lifecycle verb on it. Anything with that shape is a component,
// whatever it delegates to underneath — which also means the walk never has to descend
// into core to find out, and the component's identity never has to be carried down a call
// chain and back up.
//
// What it still cannot see is stated in `blindSpots` below rather than left to be
// discovered.
func (s *scanner) handledIn(p *packages.Package, ph Phase, verb string) (map[componentID]handledAt, []string) {
	out := map[componentID]handledAt{}
	var anon []string

	seen := map[any]bool{}
	var queue []*node
	seeds := map[any]bool{}
	for _, e := range s.cb[p][ph] {
		seeds[e.key()] = true
		if seen[e.key()] {
			continue
		}
		seen[e.key()] = true
		queue = append(queue, e)
	}

	// 🔴 EVERY OTHER ENTRY POINT IS A BARRIER, WHICH IS STRICTER THAN THE RULE
	// TRAVERSAL'S BARRIER AND HAS TO BE. A Rule asks a question about the whole program,
	// so its walk may wander into another service and still be answering the question.
	// This walk is scoped to one service BY CONSTRUCTION: the moment it crosses into
	// another service's callback, or into a component's own ExecuteStop, the components
	// it attributes to this service stop being this service's. So it stops at any entry
	// point it did not start from — including the same phase's callbacks in another
	// package.
	barrier := map[any]bool{}
	for _, es := range s.entries {
		for _, e := range es {
			if !seeds[e.key()] {
				barrier[e.key()] = true
			}
		}
	}

	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		if cur.pkg == nil || cur.body == nil {
			continue
		}
		info := cur.pkg.TypesInfo
		consumed := map[*ast.Ident]bool{}

		ast.Inspect(cur.body, func(n ast.Node) bool {
			if lit, ok := n.(*ast.FuncLit); ok {
				if s.classified[lit] {
					return false
				}
				return true
			}
			id, sel := identOf(n)
			if id == nil {
				return true
			}
			if sel != nil {
				consumed[sel.Sel] = true
			} else if consumed[id] {
				return true
			}
			fn, _ := info.Uses[id].(*types.Func)
			if fn == nil {
				return true
			}
			fn = fn.Origin()

			if isLifecycleVerb(fn, verb) {
				comp, ok := componentOf(info, sel, fn)
				if !ok {
					anon = append(anon, fmt.Sprintf(
						"%s: %s is called in %s on something no name reaches — not a "+
							"package-level variable and not an interface",
						s.fset.Position(nodePos(n, sel)), verb, cur.name))
					return true
				}
				record(out, comp, s.fset.Position(nodePos(n, sel)), cur.name)
				// 🔴 AND THE WALK STOPS. Descending would enter the component's own
				// implementation, and through LifecycleManager.transition it would reach
				// Component.ExecuteStop by INTERFACE — whose class-hierarchy expansion is
				// every ExecuteStop in the loaded program. One service would be credited
				// with handling every component in the workspace.
				return true
			}

			// 🔴 NO CLASS-HIERARCHY EXPANSION ON THIS WALK AT ALL, NOT EVEN FOR A
			// METHOD THAT IS NOT A VERB. The Rule traversal expands an interface
			// callee to every type in the program that implements it, which is the
			// right approximation for "could this run in a phase it must not" —
			// over-approximating there can only invent findings. A set difference
			// inverts that: a phantom edge on the STOP side credits the service with
			// stopping something it never touches, and the difference comes back empty.
			//
			// It is not hypothetical. A Stopper that ranges over a []Hook calling
			// h.Kick(ctx) expands to EVERY Kick in the program, including one on a type
			// nothing ever puts in that slice; if that method calls Missed.Stop(ctx),
			// Missed is recorded as stopped and its real omission is hidden. Measured
			// as a fixture, not imagined.
			//
			// What this costs is stated in blindSpots: a component started or stopped
			// only through an interface method that is NOT itself a lifecycle verb is
			// invisible to this walk. Nothing in the tree does that, and the direction
			// of the loss is the safe one — it can only produce a finding to look at,
			// never hide one.
			if sig, _ := fn.Type().(*types.Signature); sig != nil && sig.Recv() != nil {
				if _, isIface := sig.Recv().Type().Underlying().(*types.Interface); isIface {
					return true
				}
			}
			for _, target := range s.resolve(fn) {
				next, ok := s.decls[target]
				if !ok || seen[next.key()] || barrier[next.key()] {
					continue
				}
				seen[next.key()] = true
				queue = append(queue, next)
			}
			return true
		})
	}
	return out, anon
}

// isLifecycleVerb reports whether a callee is the named lifecycle verb.
//
// The SIGNATURE is checked, not just the name, and that is what keeps the widened
// call-site model from matching everything: `ticker.Stop()` and sparkplug's
// `Manager.Start()` take no context and are not this. See `blindSpots` — the second of
// those is a real component this deliberately does not watch.
func isLifecycleVerb(fn *types.Func, verb string) bool {
	if fn.Name() != verb {
		return false
	}
	sig, _ := fn.Type().(*types.Signature)
	return sig != nil && sig.Recv() != nil && isPhaseSignature(sig)
}

// componentOf names the thing a lifecycle verb was called on.
//
// A service wires its components into PACKAGE-LEVEL VARIABLES, and those are the same
// objects in the start callback and the stop callback, which is what makes the set
// difference meaningful. Identifying by TYPE instead is not equivalent and was measured
// wrong: purge.Coordinator and deadletters.Sweeper both embed *core.PeriodicTask and
// declare no Stop of their own, so by type they are one thing and dropping one of them
// leaves the difference empty. Two services wire exactly that pair.
//
// The fallback is the INTERFACE, for the loop shape — event-sources iterates
// []core.LifecycleComponent, where the element is a local whose object differs between
// the two callbacks and could never match itself.
//
// Anything else returns false and is reported as an instrument failure rather than
// guessed at, because a component counted on neither side reads exactly like one that is
// correctly stopped.
func componentOf(info *types.Info, sel *ast.SelectorExpr, fn *types.Func) (componentID, bool) {
	if v := wiredVar(info, sel); !v.zero() {
		return v, true
	}
	sig, _ := fn.Type().(*types.Signature)
	if sig != nil && sig.Recv() != nil {
		if _, isIface := sig.Recv().Type().Underlying().(*types.Interface); isIface {
			if named, ok := deref(sig.Recv().Type()).(*types.Named); ok {
				return componentID{tn: named.Obj()}, true
			}
		}
	}
	return componentID{}, false
}

// componentID is one wired component: the package-level variable a service holds it in,
// or — when it is reached through an interface and no variable names it — that interface.
// It is a comparable struct so it can key the set difference directly.
type componentID struct {
	v  *types.Var
	tn *types.TypeName
}

func (c componentID) zero() bool { return c.v == nil && c.tn == nil }

func (c componentID) String() string {
	if c.v != nil {
		return c.v.Name() + " (" + typeLabel(c.v.Type()) + ")"
	}
	if c.tn != nil {
		return typeNameLabel(c.tn)
	}
	return "?"
}

// wiredVar reports the PACKAGE-LEVEL variable a selector is rooted at, which is how a
// service names the components it wires. A local — the loop variable in
// `for _, source := range EventSources` — is deliberately not one: the start and stop
// callbacks each declare their own, so two locals with the same spelling are different
// objects and would never match each other, and a difference against itself is empty.
func wiredVar(info *types.Info, sel *ast.SelectorExpr) componentID {
	if sel == nil {
		return componentID{}
	}
	base, ok := unparen(sel.X).(*ast.Ident)
	if !ok {
		return componentID{}
	}
	v, _ := info.Uses[base].(*types.Var)
	if v == nil || v.Pkg() == nil || v.Parent() != v.Pkg().Scope() {
		return componentID{}
	}
	return componentID{v: v}
}

func record(out map[componentID]handledAt, id componentID, pos token.Position, where string) {
	if _, dup := out[id]; dup {
		return
	}
	out[id] = handledAt{pos: pos, where: where}
}
